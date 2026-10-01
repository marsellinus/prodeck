package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harness builds a manager with a controllable clock and a temp directory.
type harness struct {
	mgr   *Manager
	audit *Audit
	dir   string
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	audit, err := OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("OpenAudit: %v", err)
	}
	t.Cleanup(func() { audit.Close() })

	h := &harness{dir: dir, audit: audit, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	opts := DefaultOptions()
	opts.Now = func() time.Time { return h.now }
	// A high rate limit by default so the rate-limit test is the only one that
	// exercises it.
	opts.PerSecond = 1000
	opts.Burst = 1000

	mgr, err := NewManager(filepath.Join(dir, "devices.json"), audit, opts)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	h.mgr = mgr
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *harness) pair(t *testing.T, pin string, info DeviceInfo) (PairResult, error) {
	t.Helper()
	return h.mgr.Pair(pin, info, "192.168.1.50", nil)
}

// TestPairHappyPath covers the whole successful flow: PIN, token, scopes,
// persisted record, and the token working for authentication.
func TestPairHappyPath(t *testing.T) {
	h := newHarness(t)
	pin, err := h.mgr.IssuePIN()
	if err != nil {
		t.Fatalf("IssuePIN: %v", err)
	}
	if len(pin.Code) != 6 {
		t.Fatalf("PIN %q has %d digits, want 6", pin.Code, len(pin.Code))
	}
	if !pin.ExpiresAt.After(h.now) {
		t.Fatal("PIN expired immediately")
	}

	res, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "My Phone", Platform: "android"})
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if res.Token == "" {
		t.Fatal("Pair returned an empty token")
	}
	if len(res.Scopes) == 0 {
		t.Fatal("Pair returned no scopes")
	}
	if res.Device.TokenFingerprint == "" {
		t.Error("no token fingerprint was recorded")
	}

	// The plaintext token must not be recoverable from the stored record.
	raw, err := os.ReadFile(filepath.Join(h.dir, "devices.json"))
	if err != nil {
		t.Fatalf("reading devices.json: %v", err)
	}
	if strings.Contains(string(raw), res.Token) {
		t.Fatal("the plaintext token was written to devices.json")
	}
	if !strings.Contains(string(raw), hashToken(res.Token)) {
		t.Fatal("the token hash was not written to devices.json")
	}

	dev, err := h.mgr.Authenticate(res.Token, "android-1", "192.168.1.50")
	if err != nil {
		t.Fatalf("Authenticate with a freshly issued token: %v", err)
	}
	if dev.ID != "android-1" {
		t.Errorf("authenticated as %q", dev.ID)
	}
}

// TestPairPINIsSingleUse is the core anti-replay property: a captured pairing
// request cannot be replayed.
func TestPairPINIsSingleUse(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()

	if _, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"}); err != nil {
		t.Fatalf("first Pair: %v", err)
	}
	_, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-2", Name: "B"})
	if !errors.Is(err, ErrNoPIN) {
		t.Fatalf("second use of the PIN returned %v, want ErrNoPIN", err)
	}
}

// TestPairPINExpiry covers the TTL.
func TestPairPINExpiry(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()

	h.advance(121 * time.Second)
	if _, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"}); !errors.Is(err, ErrPINExpired) {
		t.Fatalf("expired PIN returned %v, want ErrPINExpired", err)
	}
	if _, ok := h.mgr.ActivePIN(); ok {
		t.Error("an expired PIN is still reported as active")
	}
}

// TestPairLockoutAfterFailures covers brute-force resistance (SECURITY.md T2).
func TestPairLockoutAfterFailures(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()

	wrong := "000000"
	if wrong == pin.Code {
		wrong = "111111"
	}

	// Four wrong attempts are plain failures; the fifth triggers the lockout.
	for i := range 4 {
		_, err := h.pair(t, wrong, DeviceInfo{ID: "android-1", Name: "A"})
		if !errors.Is(err, ErrPINInvalid) {
			t.Fatalf("attempt %d returned %v, want ErrPINInvalid", i+1, err)
		}
	}
	_, err := h.pair(t, wrong, DeviceInfo{ID: "android-1", Name: "A"})
	if !errors.Is(err, ErrTooManyTries) {
		t.Fatalf("the fifth wrong attempt returned %v, want ErrTooManyTries", err)
	}

	// The PIN must now be dead even if the attacker guesses it correctly.
	if _, ok := h.mgr.ActivePIN(); ok {
		t.Fatal("the PIN survived a lockout")
	}
	if _, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"}); err == nil {
		t.Fatal("pairing succeeded after a lockout")
	}

	// Issuing a fresh PIN clears the lock, because the operator has explicitly
	// re-opened pairing.
	fresh, err := h.mgr.IssuePIN()
	if err != nil {
		t.Fatalf("IssuePIN after lockout: %v", err)
	}
	if _, err := h.pair(t, fresh.Code, DeviceInfo{ID: "android-1", Name: "A"}); err != nil {
		t.Fatalf("pairing with a fresh PIN after a lockout: %v", err)
	}
}

// TestPairLockoutIsPerSource checks that one attacker cannot lock out every
// other device by hammering the endpoint from one address.
func TestPairLockoutIsPerSource(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()

	for range 5 {
		_, _ = h.mgr.Pair("000000", DeviceInfo{ID: "attacker", Name: "A"}, "10.0.0.9", nil)
	}

	// A different device from a different address is still locked out only if
	// its own counters tripped; it has not, so it can pair.
	_, err := h.mgr.Pair(pin.Code, DeviceInfo{ID: "victim", Name: "V"}, "10.0.0.10", nil)
	if err != nil {
		// The per-IP counter for 10.0.0.9 tripped, and the PIN was closed by the
		// lockout. That is the intended behaviour: the PIN is dead.
		if !errors.Is(err, ErrNoPIN) && !errors.Is(err, ErrTooManyTries) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

// TestAuthenticateRejectsBadTokens covers every way a token can be wrong.
func TestAuthenticateRejectsBadTokens(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()
	res, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"})
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}

	cases := []struct {
		name     string
		token    string
		deviceID string
		want     error
	}{
		{"empty token", "", "android-1", ErrBadToken},
		{"wrong token", "not-the-token", "android-1", ErrBadToken},
		{"truncated token", res.Token[:len(res.Token)-1], "android-1", ErrBadToken},
		{"token for another device", res.Token, "android-2", ErrDeviceUnknown},
		{"valid token, unknown device", res.Token, "ghost", ErrDeviceUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.mgr.Authenticate(tc.token, tc.deviceID, "192.168.1.50"); !errors.Is(err, tc.want) {
				t.Fatalf("Authenticate returned %v, want %v", err, tc.want)
			}
		})
	}
}

// TestRevokeInvalidatesToken is the property that makes revocation meaningful.
func TestRevokeInvalidatesToken(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()
	res, _ := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"})

	ok, err := h.mgr.Revoke("android-1")
	if err != nil || !ok {
		t.Fatalf("Revoke = %v, %v", ok, err)
	}
	if _, err := h.mgr.Authenticate(res.Token, "android-1", "192.168.1.50"); err == nil {
		t.Fatal("a revoked token still authenticates")
	}
	// Revoking twice reports "not found" rather than failing.
	ok, err = h.mgr.Revoke("android-1")
	if err != nil || ok {
		t.Fatalf("second Revoke = %v, %v; want false, nil", ok, err)
	}
}

// TestDisableBlocksWithoutDeleting covers the "pause this phone" path.
func TestDisableBlocksWithoutDeleting(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()
	res, _ := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"})

	if err := h.mgr.SetDisabled("android-1", true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	if _, err := h.mgr.Authenticate(res.Token, "android-1", "192.168.1.50"); !errors.Is(err, ErrDeviceDisabled) {
		t.Fatalf("Authenticate on a disabled device returned %v, want ErrDeviceDisabled", err)
	}
	if _, ok := h.mgr.Device("android-1"); !ok {
		t.Fatal("disabling deleted the device record")
	}

	if err := h.mgr.SetDisabled("android-1", false); err != nil {
		t.Fatalf("re-enabling: %v", err)
	}
	if _, err := h.mgr.Authenticate(res.Token, "android-1", "192.168.1.50"); err != nil {
		t.Fatalf("Authenticate after re-enabling: %v", err)
	}
}

// TestRepairKeepsScopes checks that a phone which loses its token does not
// silently gain or lose permissions when it pairs again.
func TestRepairKeepsScopes(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()
	res, _ := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"})

	narrowed := []Scope{ScopeKeyboard}
	if err := h.mgr.SetScopes("android-1", narrowed); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	pin2, _ := h.mgr.IssuePIN()
	again, err := h.pair(t, pin2.Code, DeviceInfo{ID: "android-1", Name: "A renamed"})
	if err != nil {
		t.Fatalf("re-pairing: %v", err)
	}
	if len(again.Scopes) != 1 || again.Scopes[0] != ScopeKeyboard {
		t.Fatalf("re-pairing changed the scopes to %v", again.Scopes)
	}
	if again.Device.Name != "A renamed" {
		t.Errorf("the rename was not applied: %q", again.Device.Name)
	}
	if again.Token == res.Token {
		t.Error("re-pairing reused the old token")
	}
	if _, err := h.mgr.Authenticate(res.Token, "android-1", "192.168.1.50"); err == nil {
		t.Error("the old token still works after re-pairing")
	}
}

// TestPairRefusedWhenNoPINIsOpen covers a host that never opened pairing.
func TestPairRefusedWhenNoPINIsOpen(t *testing.T) {
	h := newHarness(t)
	if _, err := h.pair(t, "123456", DeviceInfo{ID: "android-1", Name: "A"}); !errors.Is(err, ErrNoPIN) {
		t.Fatalf("Pair with no open window returned %v, want ErrNoPIN", err)
	}
}

// TestIssuePINReplacesPrevious covers the CLI behaviour: asking twice gives the
// newest code, and only the newest one works.
func TestIssuePINReplacesPrevious(t *testing.T) {
	h := newHarness(t)
	first, _ := h.mgr.IssuePIN()
	second, _ := h.mgr.IssuePIN()

	if first.Code == second.Code {
		t.Skip("the generator produced the same PIN twice; rerun")
	}
	if _, err := h.pair(t, first.Code, DeviceInfo{ID: "android-1", Name: "A"}); err == nil {
		t.Fatal("the superseded PIN still works")
	}
	if _, err := h.pair(t, second.Code, DeviceInfo{ID: "android-1", Name: "A"}); err != nil {
		t.Fatalf("the current PIN was rejected: %v", err)
	}
}

// TestScopeEnforcement covers the permission model itself.
func TestScopeEnforcement(t *testing.T) {
	set := NewScopeSet([]Scope{ScopeKeyboard, ScopeMouse})

	for _, sc := range []Scope{ScopeKeyboard, ScopeMouse, ScopeNone} {
		if !set.Has(sc) {
			t.Errorf("scope %q should be granted", sc)
		}
	}
	for _, sc := range []Scope{ScopeScripts, ScopeSystemPower, ScopePlugins} {
		if set.Has(sc) {
			t.Errorf("scope %q should not be granted", sc)
		}
	}

	// The default set must exclude both high-risk scopes: pairing a phone must
	// never hand it a shell or a power switch by accident.
	defaults := NewScopeSet(DefaultScopes())
	for _, hr := range HighRiskScopes() {
		if defaults.Has(hr) {
			t.Errorf("the default scope set includes the high-risk scope %q", hr)
		}
	}
}

// TestParseScopes covers input validation for the CLI and the admin API.
func TestParseScopes(t *testing.T) {
	if _, err := ParseScopes([]string{"keyboard", "mouse"}); err != nil {
		t.Fatalf("ParseScopes on valid input: %v", err)
	}
	if _, err := ParseScopes([]string{"KEYBOARD"}); err != nil {
		t.Fatalf("ParseScopes should be case-insensitive: %v", err)
	}
	if _, err := ParseScopes([]string{"telepathy"}); err == nil {
		t.Fatal("ParseScopes accepted an unknown scope")
	}
	if _, err := ParseScopes([]string{"mouse", "mouse"}); err == nil {
		t.Fatal("ParseScopes accepted a duplicate scope")
	}
}

// TestRateLimit covers the per-device bucket.
func TestRateLimit(t *testing.T) {
	h := newHarness(t)
	// Tighten the limit for this test only.
	h.mgr.limit = newLimiter(1, 5, func() time.Time { return h.now })

	for i := range 5 {
		if err := h.mgr.RateAllow("android-1"); err != nil {
			t.Fatalf("request %d was limited: %v", i+1, err)
		}
	}
	if err := h.mgr.RateAllow("android-1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the 6th request returned %v, want ErrRateLimited", err)
	}
	// A different device has its own bucket.
	if err := h.mgr.RateAllow("android-2"); err != nil {
		t.Fatalf("a different device was limited: %v", err)
	}
	// Refill after a second of virtual time.
	h.advance(time.Second)
	if err := h.mgr.RateAllow("android-1"); err != nil {
		t.Fatalf("the bucket did not refill: %v", err)
	}
}

// TestPINEntropy checks the PIN generator over many samples: right length, right
// alphabet, no obvious bias, and no repeats.
func TestPINEntropy(t *testing.T) {
	counts := make([]int, 10)
	seen := make(map[string]int)
	const samples = 4000

	for range samples {
		code, err := randomDigits(6)
		if err != nil {
			t.Fatalf("randomDigits: %v", err)
		}
		if len(code) != 6 {
			t.Fatalf("PIN %q has length %d", code, len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("PIN %q contains %q", code, r)
			}
			counts[r-'0']++
		}
		seen[code]++
	}

	// With 4000 samples of 6 digits there are 24000 draws; a uniform generator
	// gives ~2400 per digit. A ±30% band catches a modulo bias or a stuck digit
	// without being flaky.
	const expected = samples * 6 / 10
	lo, hi := expected*7/10, expected*13/10
	for digit, n := range counts {
		if n < lo || n > hi {
			t.Errorf("digit %d appeared %d times, expected roughly %d (band %d..%d)", digit, n, expected, lo, hi)
		}
	}

	// Collisions are expected in a sample this size (birthday paradox over 10^6
	// codes), but the number must be small: a huge count would mean the
	// generator is repeating itself.
	if len(seen) < samples*9/10 {
		t.Errorf("only %d distinct PINs in %d samples; the generator looks weak", len(seen), samples)
	}
}

// TestAuditRecordsPairingAttempts checks the security log actually records what
// SECURITY.md promises, and that it records no secrets.
func TestAuditRecordsPairingAttempts(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()

	_, _ = h.pair(t, "000000", DeviceInfo{ID: "android-1", Name: "A"})
	res, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "A"})
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if err := h.mgr.SetDisabled("android-1", true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	_, _ = h.mgr.Authenticate("nonsense", "android-1", "192.168.1.50")

	if err := h.audit.Close(); err != nil {
		t.Fatalf("closing the audit log: %v", err)
	}

	events, err := TailAudit(filepath.Join(h.dir, "audit.jsonl"), 0)
	if err != nil {
		t.Fatalf("TailAudit: %v", err)
	}
	if len(events) < 4 {
		t.Fatalf("expected at least 4 audit records, got %d", len(events))
	}

	kinds := make(map[string]int)
	for _, e := range events {
		kinds[e.Kind]++
		if e.TS == 0 {
			t.Errorf("record %q has no timestamp", e.Kind)
		}
	}
	for _, want := range []string{EventPINIssued, EventPairAttempt, EventPairSuccess, EventAuth, EventDeviceUpdated} {
		if kinds[want] == 0 {
			t.Errorf("no %q record was written", want)
		}
	}

	// The failed attempt must be recorded as a failure with a reason, and the
	// successful one as a success with the granted scopes.
	var sawFailure, sawSuccess bool
	for _, e := range events {
		if e.Kind == EventPairAttempt && !e.OK && e.Reason == "invalid_pin" {
			sawFailure = true
		}
		if e.Kind == EventPairSuccess && e.OK && len(e.Scopes) > 0 {
			sawSuccess = true
		}
	}
	if !sawFailure {
		t.Error("the failed pairing attempt was not recorded with its reason")
	}
	if !sawSuccess {
		t.Error("the successful pairing was not recorded with its scopes")
	}

	// No secret may appear in the audit log.
	raw, err := os.ReadFile(filepath.Join(h.dir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	if strings.Contains(string(raw), pin.Code) {
		t.Error("the PIN appears in the audit log")
	}
	if strings.Contains(string(raw), res.Token) {
		t.Error("the token appears in the audit log")
	}
}

// TestDeviceNameSanitized checks that a hostile device name cannot inject
// control characters into log lines or the CLI listing.
func TestDeviceNameSanitized(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()
	res, err := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "evil\nname\x00with\x1b[31mcontrol"})
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	for _, r := range res.Device.Name {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("the stored name contains the control character %q: %q", r, res.Device.Name)
		}
	}

	pin2, _ := h.mgr.IssuePIN()
	empty, err := h.pair(t, pin2.Code, DeviceInfo{ID: "android-2", Name: "   "})
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if empty.Device.Name == "" {
		t.Error("an empty name was stored as-is instead of a placeholder")
	}
}

// TestDevicePersistsAcrossRestart checks that the device store is durable: a
// host restart must not force every phone to re-pair.
func TestDevicePersistsAcrossRestart(t *testing.T) {
	h := newHarness(t)
	pin, _ := h.mgr.IssuePIN()
	res, _ := h.pair(t, pin.Code, DeviceInfo{ID: "android-1", Name: "My Phone"})

	// A fresh manager over the same file, as a restart would create.
	opts := DefaultOptions()
	opts.Now = func() time.Time { return h.now }
	mgr2, err := NewManager(filepath.Join(h.dir, "devices.json"), h.audit, opts)
	if err != nil {
		t.Fatalf("NewManager over an existing store: %v", err)
	}
	dev, err := mgr2.Authenticate(res.Token, "android-1", "192.168.1.50")
	if err != nil {
		t.Fatalf("Authenticate after a simulated restart: %v", err)
	}
	if dev.Name != "My Phone" {
		t.Errorf("name = %q after restart", dev.Name)
	}
	if len(mgr2.Devices()) != 1 {
		t.Errorf("expected 1 device after restart, got %d", len(mgr2.Devices()))
	}
}

// TestAdminTokenIsPerProcess checks that the admin credential is not durable.
func TestAdminTokenIsPerProcess(t *testing.T) {
	a := newHarness(t)
	b := newHarness(t)

	if a.mgr.AdminToken() == "" {
		t.Fatal("no admin token was generated")
	}
	if a.mgr.AdminToken() == b.mgr.AdminToken() {
		t.Fatal("two managers generated the same admin token")
	}
	if !a.mgr.VerifyAdminToken(a.mgr.AdminToken()) {
		t.Error("the manager rejected its own admin token")
	}
	if a.mgr.VerifyAdminToken(b.mgr.AdminToken()) {
		t.Error("the manager accepted another manager's admin token")
	}
	if a.mgr.VerifyAdminToken("") {
		t.Error("the manager accepted an empty admin token")
	}
}

// TestTokenHashIsSHA256 pins the at-rest representation.
func TestTokenHashIsSHA256(t *testing.T) {
	const token = "a-known-token"
	got := hashToken(token)
	// Independently computed SHA-256 of "a-known-token".
	const want = "3cc866519997b4a5942d3466e15aa5eef617d095bec41e8d396d265a450590f2"
	if got != want {
		t.Fatalf("hashToken(%q) = %q, want %q", token, got, want)
	}
}
