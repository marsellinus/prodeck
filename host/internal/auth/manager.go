package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Errors returned by the manager. They are sentinel values so the server can
// map them onto protocol error codes without string matching.
var (
	ErrNoPIN          = errors.New("auth: no pairing PIN is currently active")
	ErrPINExpired     = errors.New("auth: the pairing PIN has expired")
	ErrPINInvalid     = errors.New("auth: the pairing PIN is wrong")
	ErrPINConsumed    = errors.New("auth: the pairing PIN has already been used")
	ErrTooManyTries   = errors.New("auth: too many failed pairing attempts; request a new PIN")
	ErrBadToken       = errors.New("auth: token is invalid")
	ErrDeviceDisabled = errors.New("auth: this device has been disabled on the host")
	ErrDeviceUnknown  = errors.New("auth: unknown device")
	ErrRateLimited    = errors.New("auth: rate limit exceeded")
)

// PIN is an active pairing code.
type PIN struct {
	Code      string    `json:"pin"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Options configures the manager. The zero value is not useful; use DefaultOptions.
type Options struct {
	PINLength     int
	PINValidFor   time.Duration
	MaxAttempts   int
	AttemptWindow time.Duration
	LockoutFor    time.Duration
	PerSecond     float64
	Burst         int
	Now           func() time.Time // injectable for tests
}

// DefaultOptions mirrors config.Default's security-relevant fields.
func DefaultOptions() Options {
	return Options{
		PINLength:     6,
		PINValidFor:   120 * time.Second,
		MaxAttempts:   5,
		AttemptWindow: 10 * time.Minute,
		LockoutFor:    10 * time.Minute,
		PerSecond:     30,
		Burst:         60,
		Now:           time.Now,
	}
}

// Manager is the single entry point for authentication decisions.
type Manager struct {
	opts    Options
	devices *deviceStore
	audit   *Audit
	limit   *limiter

	mu       sync.Mutex
	pin      *PIN
	attempts map[string]*attempt

	adminToken string
}

// attempt tracks failed pairing tries for one source.
type attempt struct {
	count  int
	first  time.Time
	locked time.Time
}

// NewManager opens (or creates) the device store and prepares the manager.
func NewManager(devicesPath string, audit *Audit, opts Options) (*Manager, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PINLength == 0 {
		opts.PINLength = 6
	}
	if opts.PINValidFor == 0 {
		opts.PINValidFor = 120 * time.Second
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 5
	}
	if opts.AttemptWindow == 0 {
		opts.AttemptWindow = 10 * time.Minute
	}
	if opts.LockoutFor == 0 {
		opts.LockoutFor = 10 * time.Minute
	}
	if opts.PerSecond == 0 {
		opts.PerSecond = 30
	}
	if opts.Burst == 0 {
		opts.Burst = 60
	}
	if err := ensurePathWritable(devicesPath); err != nil {
		return nil, err
	}
	ds, err := newDeviceStore(devicesPath)
	if err != nil {
		return nil, err
	}
	tok, err := newAdminToken()
	if err != nil {
		return nil, err
	}
	return &Manager{
		opts:       opts,
		devices:    ds,
		audit:      audit,
		limit:      newLimiter(opts.PerSecond, opts.Burst, opts.Now),
		attempts:   map[string]*attempt{},
		adminToken: tok,
	}, nil
}

// AdminToken is the bearer token for the loopback-only admin API. It is
// regenerated on every start, so it is never a durable credential.
func (m *Manager) AdminToken() string { return m.adminToken }

// VerifyAdminToken checks a presented admin token in constant time.
func (m *Manager) VerifyAdminToken(presented string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(m.adminToken)) == 1
}

// IssuePIN creates a new pairing PIN, replacing any previous one. Replacing
// rather than rejecting is deliberate: a user who runs `mobiledeck pair` twice
// expects the newest code to work.
func (m *Manager) IssuePIN() (PIN, error) {
	code, err := randomDigits(m.opts.PINLength)
	if err != nil {
		return PIN{}, err
	}
	now := m.opts.Now()
	p := PIN{Code: code, ExpiresAt: now.Add(m.opts.PINValidFor)}

	m.mu.Lock()
	m.pin = &p
	// A fresh PIN clears the lockout: the operator has deliberately re-opened
	// pairing, and leaving the lock in place would make the CLI lie.
	m.attempts = map[string]*attempt{}
	m.mu.Unlock()

	m.audit.Record(Event{Kind: EventPINIssued, ExpiresAt: p.ExpiresAt})
	return p, nil
}

// ActivePIN returns the current PIN and whether pairing is open. It is used by
// the admin API so `mobiledeck pair` can print a PIN that a running daemon is
// actually honouring.
func (m *Manager) ActivePIN() (PIN, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pin == nil {
		return PIN{}, false
	}
	if m.opts.Now().After(m.pin.ExpiresAt) {
		m.pin = nil
		return PIN{}, false
	}
	return *m.pin, true
}

// ClosePIN invalidates the current PIN without pairing.
func (m *Manager) ClosePIN() {
	m.mu.Lock()
	m.pin = nil
	m.mu.Unlock()
}

// DeviceInfo is what a client tells the host about itself while pairing.
type DeviceInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	Model    string `json:"model,omitempty"`
}

// PairResult is a successful pairing.
type PairResult struct {
	Device Device  `json:"device"`
	Token  string  `json:"token"`
	Scopes []Scope `json:"scopes"`
}

// Pair validates a PIN and, on success, creates or re-issues the device's
// token. The returned token is the only time the plaintext exists.
func (m *Manager) Pair(pin string, info DeviceInfo, ip string, defaultScopes []Scope) (PairResult, error) {
	now := m.opts.Now()

	if info.ID == "" {
		return PairResult{}, fmt.Errorf("auth: device id must not be empty")
	}
	if err := m.limit.Allow("pair:" + ip); err != nil {
		m.audit.Record(Event{Kind: EventPairAttempt, DeviceID: info.ID, IP: ip, OK: false, Reason: "rate_limited"})
		return PairResult{}, ErrRateLimited
	}
	if err := m.checkLockout(info.ID, ip); err != nil {
		m.audit.Record(Event{Kind: EventPairAttempt, DeviceID: info.ID, IP: ip, OK: false, Reason: "locked_out"})
		return PairResult{}, err
	}

	m.mu.Lock()
	active := m.pin
	m.mu.Unlock()

	switch {
	case active == nil:
		m.audit.Record(Event{Kind: EventPairAttempt, DeviceID: info.ID, IP: ip, OK: false, Reason: "no_pin"})
		return PairResult{}, ErrNoPIN
	case now.After(active.ExpiresAt):
		m.ClosePIN()
		m.audit.Record(Event{Kind: EventPairAttempt, DeviceID: info.ID, IP: ip, OK: false, Reason: "expired"})
		return PairResult{}, ErrPINExpired
	}

	if subtle.ConstantTimeCompare([]byte(pin), []byte(active.Code)) != 1 {
		locked := m.recordFailure(info.ID, ip, now)
		m.audit.Record(Event{Kind: EventPairAttempt, DeviceID: info.ID, IP: ip, OK: false, Reason: "invalid_pin"})
		if locked {
			m.ClosePIN()
			return PairResult{}, ErrTooManyTries
		}
		return PairResult{}, ErrPINInvalid
	}

	// Success: the PIN is single use, so consume it before doing anything that
	// can fail. A PIN that survives a failed device write would be reusable.
	m.ClosePIN()

	if existing, ok := m.devices.Get(info.ID); ok && existing.Disabled {
		m.audit.Record(Event{Kind: EventPairAttempt, DeviceID: info.ID, IP: ip, OK: false, Reason: "device_disabled"})
		return PairResult{}, ErrDeviceDisabled
	}

	token, hash, fingerprint, err := newToken()
	if err != nil {
		return PairResult{}, err
	}

	scopes := defaultScopes
	if len(scopes) == 0 {
		scopes = DefaultScopes()
	}
	// Re-pairing keeps the scopes the operator already granted, so a phone that
	// loses its token does not silently regain (or lose) permissions.
	if existing, ok := m.devices.Get(info.ID); ok && len(existing.Scopes) > 0 {
		scopes = existing.Scopes
	}

	dev := Device{
		ID:               info.ID,
		Name:             sanitizeName(info.Name),
		Platform:         info.Platform,
		Model:            info.Model,
		TokenHash:        hash,
		Scopes:           scopes,
		CreatedAt:        now,
		LastSeen:         now,
		LastIP:           ip,
		TokenFingerprint: fingerprint,
	}
	if existing, ok := m.devices.Get(info.ID); ok {
		dev.CreatedAt = existing.CreatedAt
		dev.Disabled = false
	}
	if err := m.devices.Put(dev); err != nil {
		return PairResult{}, err
	}

	m.audit.Record(Event{Kind: EventPairSuccess, DeviceID: dev.ID, DeviceName: dev.Name, IP: ip, OK: true, Scopes: scopeStrings(scopes)})
	return PairResult{Device: dev, Token: token, Scopes: scopes}, nil
}

// Authenticate verifies a device token. It is the only way a session is
// allowed to exist.
func (m *Manager) Authenticate(token, deviceID, ip string) (Device, error) {
	if token == "" {
		return Device{}, ErrBadToken
	}
	if err := m.limit.Allow("auth:" + ip); err != nil {
		return Device{}, ErrRateLimited
	}
	dev, ok := m.devices.Get(deviceID)
	if !ok {
		// Do not distinguish "unknown device" from "bad token" in the audit
		// reason shown to the client; both are simply unauthenticated.
		m.audit.Record(Event{Kind: EventAuth, DeviceID: deviceID, IP: ip, OK: false, Reason: "unknown_device"})
		return Device{}, ErrDeviceUnknown
	}
	if !verifyToken(token, dev.TokenHash) {
		m.audit.Record(Event{Kind: EventAuth, DeviceID: deviceID, IP: ip, OK: false, Reason: "bad_token"})
		return Device{}, ErrBadToken
	}
	if dev.Disabled {
		m.audit.Record(Event{Kind: EventAuth, DeviceID: deviceID, IP: ip, OK: false, Reason: "disabled"})
		return Device{}, ErrDeviceDisabled
	}
	m.devices.touch(deviceID, ip, m.opts.Now())
	m.audit.Record(Event{Kind: EventAuth, DeviceID: deviceID, IP: ip, OK: true})
	return dev, nil
}

// RateAllow applies the per-device request bucket. It is separate from
// Authenticate so a session can be throttled per message, not only per
// connection.
func (m *Manager) RateAllow(deviceID string) error {
	if err := m.limit.Allow("dev:" + deviceID); err != nil {
		return ErrRateLimited
	}
	return nil
}

// Devices lists paired devices.
func (m *Manager) Devices() []Device { return m.devices.List() }

// Device returns one device.
func (m *Manager) Device(id string) (Device, bool) { return m.devices.Get(id) }

// Revoke removes a device entirely. Its token stops working immediately.
func (m *Manager) Revoke(id string) (bool, error) {
	ok, err := m.devices.Delete(id)
	if err != nil {
		return false, err
	}
	if ok {
		m.audit.Record(Event{Kind: EventDeviceRevoked, DeviceID: id, By: "admin"})
	}
	return ok, nil
}

// Rename changes a device's display name.
func (m *Manager) Rename(id, name string) error {
	dev, ok := m.devices.Get(id)
	if !ok {
		return ErrDeviceUnknown
	}
	dev.Name = sanitizeName(name)
	if err := m.devices.Put(dev); err != nil {
		return err
	}
	m.audit.Record(Event{Kind: EventDeviceUpdated, DeviceID: id, Reason: "rename", By: "admin"})
	return nil
}

// SetScopes replaces a device's scope set.
func (m *Manager) SetScopes(id string, scopes []Scope) error {
	dev, ok := m.devices.Get(id)
	if !ok {
		return ErrDeviceUnknown
	}
	dev.Scopes = scopes
	if err := m.devices.Put(dev); err != nil {
		return err
	}
	m.audit.Record(Event{Kind: EventDeviceUpdated, DeviceID: id, Reason: "scopes", Scopes: scopeStrings(scopes), By: "admin"})
	return nil
}

// SetDisabled enables or disables a device without deleting its record, which
// is what an operator wants for "pause this phone until I find it".
func (m *Manager) SetDisabled(id string, disabled bool) error {
	dev, ok := m.devices.Get(id)
	if !ok {
		return ErrDeviceUnknown
	}
	dev.Disabled = disabled
	if err := m.devices.Put(dev); err != nil {
		return err
	}
	m.audit.Record(Event{Kind: EventDeviceUpdated, DeviceID: id, Reason: "disabled", By: "admin"})
	return nil
}

// checkLockout reports whether a source is currently locked out.
func (m *Manager) checkLockout(deviceID, ip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opts.Now()
	for _, key := range []string{"id:" + deviceID, "ip:" + ip} {
		if a, ok := m.attempts[key]; ok && !a.locked.IsZero() && now.Before(a.locked) {
			return fmt.Errorf("%w (retry after %s)", ErrTooManyTries, a.locked.Sub(now).Round(time.Second))
		}
	}
	return nil
}

// recordFailure counts a failed attempt and reports whether it triggered a
// lockout. Both the device id and the source IP are tracked, so neither a
// single device guessing many PINs nor many devices from one host get through.
func (m *Manager) recordFailure(deviceID, ip string, now time.Time) (locked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, key := range []string{"id:" + deviceID, "ip:" + ip} {
		a, ok := m.attempts[key]
		if !ok || now.Sub(a.first) > m.opts.AttemptWindow {
			a = &attempt{first: now}
			m.attempts[key] = a
		}
		a.count++
		if a.count >= m.opts.MaxAttempts {
			a.locked = now.Add(m.opts.LockoutFor)
			locked = true
		}
	}
	return locked
}

// sanitizeName keeps a device name printable and bounded: it ends up in log
// lines and in the CLI listing, so control characters are stripped.
func sanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" {
		return "Unnamed device"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func scopeStrings(scopes []Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}

func newAdminToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate admin token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
