package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/profile"
)

// --- fake platform -------------------------------------------------------

// fakeInput records every injected event instead of touching the machine.
type fakeInput struct {
	platform.Unsupported

	mu       sync.Mutex
	keys     [][]platform.Key
	modes    []platform.KeyMode
	texts    []string
	clicks   []platform.MouseButton
	moves    [][2]int
	absolute [][2]float64
	scrolls  [][2]int
	// err, when set, is returned by every call so failure paths are testable.
	err error
	// failOn, when non-empty, fails only the calls whose key or text matches.
	// It exists because a blanket error cannot express "the first step fails and
	// the second succeeds", which is exactly what on_error=continue means.
	failOn string
	// delay simulates a slow operation for the cancellation tests.
	delay time.Duration
}

func (f *fakeInput) Key(ctx context.Context, k platform.Key, m platform.KeyMode) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.keys = append(f.keys, []platform.Key{k})
	f.modes = append(f.modes, m)
	return nil
}

func (f *fakeInput) Shortcut(ctx context.Context, keys []platform.Key, m platform.KeyMode) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.failOn != "" {
		for _, k := range keys {
			if string(k) == f.failOn {
				return errors.New("injected failure on " + f.failOn)
			}
		}
	}
	f.keys = append(f.keys, keys)
	f.modes = append(f.modes, m)
	return nil
}

func (f *fakeInput) Text(ctx context.Context, text string, interval time.Duration) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.failOn != "" && text == f.failOn {
		return errors.New("injected failure on text " + f.failOn)
	}
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeInput) MouseClick(ctx context.Context, btn platform.MouseButton, count int) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for range count {
		f.clicks = append(f.clicks, btn)
	}
	return nil
}

func (f *fakeInput) MouseMove(ctx context.Context, dx, dy int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.moves = append(f.moves, [2]int{dx, dy})
	return nil
}

func (f *fakeInput) MouseMoveAbsolute(ctx context.Context, x, y float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.absolute = append(f.absolute, [2]float64{x, y})
	return nil
}

func (f *fakeInput) MouseScroll(ctx context.Context, dx, dy int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.scrolls = append(f.scrolls, [2]int{dx, dy})
	return nil
}

func (f *fakeInput) wait(ctx context.Context) error {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return ctx.Err()
}

func (f *fakeInput) shortcuts() [][]platform.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]platform.Key, len(f.keys))
	copy(out, f.keys)
	return out
}

// fakeShell records commands and returns a canned result.
type fakeShell struct {
	platform.Unsupported

	mu       sync.Mutex
	scripts  []platform.ScriptOptions
	commands []platform.CommandOptions
	result   platform.ExecResult
	err      error
	// block, when non-zero, makes the call wait so cancellation can be tested.
	block time.Duration
}

func (f *fakeShell) RunScript(ctx context.Context, o platform.ScriptOptions) (platform.ExecResult, error) {
	if f.block > 0 {
		select {
		case <-ctx.Done():
			return platform.ExecResult{Cancelled: true}, ctx.Err()
		case <-time.After(f.block):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, o)
	return f.result, f.err
}

func (f *fakeShell) RunCommand(ctx context.Context, o platform.CommandOptions) (platform.ExecResult, error) {
	if f.block > 0 {
		select {
		case <-ctx.Done():
			return platform.ExecResult{Cancelled: true}, ctx.Err()
		case <-time.After(f.block):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, o)
	return f.result, f.err
}

func (f *fakeShell) SupportedInterpreters() []string { return []string{"sh", "bash"} }

// fakeLauncher records launches.
type fakeLauncher struct {
	platform.Unsupported

	mu        sync.Mutex
	opened    []string
	urls      []string
	folders   []string
	terminals []platform.TerminalOptions
	err       error
}

func (f *fakeLauncher) Open(ctx context.Context, target string, o platform.LaunchOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.opened = append(f.opened, target)
	return nil
}

func (f *fakeLauncher) OpenURL(ctx context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.urls = append(f.urls, url)
	return nil
}

func (f *fakeLauncher) OpenFolder(ctx context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.folders = append(f.folders, path)
	return nil
}

func (f *fakeLauncher) OpenTerminal(ctx context.Context, o platform.TerminalOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.terminals = append(f.terminals, o)
	return nil
}

// fakeMedia records media verbs.
type fakeMedia struct {
	platform.Unsupported
	mu    sync.Mutex
	verbs []string
}

func (f *fakeMedia) record(v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verbs = append(f.verbs, v)
	return nil
}

func (f *fakeMedia) Play(ctx context.Context) error      { return f.record("play") }
func (f *fakeMedia) Pause(ctx context.Context) error     { return f.record("pause") }
func (f *fakeMedia) PlayPause(ctx context.Context) error { return f.record("play_pause") }
func (f *fakeMedia) Stop(ctx context.Context) error      { return f.record("stop") }
func (f *fakeMedia) Next(ctx context.Context) error      { return f.record("next") }
func (f *fakeMedia) Previous(ctx context.Context) error  { return f.record("previous") }
func (f *fakeMedia) VolumeUp(ctx context.Context) error  { return f.record("volume_up") }
func (f *fakeMedia) VolumeDown(ctx context.Context) error {
	return f.record("volume_down")
}
func (f *fakeMedia) Mute(ctx context.Context) error { return f.record("mute") }
func (f *fakeMedia) SetVolume(ctx context.Context, p int) error {
	return f.record("volume_set")
}

// fakeSound records played files, so the sound action can be tested without a
// speaker. volumeIgnored makes it behave like the Windows adapter, which cannot
// set a level, so the detail wording is exercised too.
type fakeSound struct {
	platform.Unsupported

	mu            sync.Mutex
	played        []string
	volumes       []int
	blocking      []bool
	err           error
	volumeIgnored bool
}

func (f *fakeSound) PlayFile(ctx context.Context, path string, volume int, blocking bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.played = append(f.played, path)
	f.volumes = append(f.volumes, volume)
	f.blocking = append(f.blocking, blocking)
	return nil
}

func (f *fakeSound) SoundAvailable() bool { return true }

func (f *fakeSound) VolumeIgnored() bool { return f.volumeIgnored }

// fakePower reports configurable capabilities.
type fakePower struct {
	platform.Unsupported
	caps  platform.PowerCaps
	calls []string
	mu    sync.Mutex
}

func (f *fakePower) Capabilities() platform.PowerCaps { return f.caps }
func (f *fakePower) record(v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, v)
	return nil
}
func (f *fakePower) Lock(ctx context.Context) error     { return f.record("lock") }
func (f *fakePower) Sleep(ctx context.Context) error    { return f.record("sleep") }
func (f *fakePower) Shutdown(ctx context.Context) error { return f.record("shutdown") }
func (f *fakePower) Restart(ctx context.Context) error  { return f.record("restart") }

// fakeMetrics returns fixed values.
type fakeMetrics struct {
	platform.Unsupported
	values map[string]float64
}

func (f *fakeMetrics) Sample(ctx context.Context) (map[string]float64, error) {
	return f.values, nil
}

func (f *fakeMetrics) Available() []string {
	out := make([]string, 0, len(f.values))
	for k := range f.values {
		out = append(out, k)
	}
	return out
}

// harness bundles an engine with its fakes.
type harness struct {
	eng    *Engine
	input  *fakeInput
	shell  *fakeShell
	launch *fakeLauncher
	media  *fakeMedia
	sound  *fakeSound
	power  *fakePower
	plat   *platform.Platform

	mu     sync.Mutex
	events []Event
	audits []auth.Event

	dir string
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{
		input:  &fakeInput{},
		shell:  &fakeShell{},
		launch: &fakeLauncher{},
		media:  &fakeMedia{},
		sound:  &fakeSound{},
		power:  &fakePower{caps: platform.PowerCaps{Lock: true, Shutdown: true, Restart: true, Sleep: true}},
		dir:    t.TempDir(),
	}
	h.plat = &platform.Platform{
		OSName:   "test",
		Input:    h.input,
		Launcher: h.launch,
		Shell:    h.shell,
		Media:    h.media,
		Sound:    h.sound,
		Power:    h.power,
		Metrics:  &fakeMetrics{values: map[string]float64{"cpu.usage": 12.5}},
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = 4
	}
	if opts.QueueDepth == 0 {
		opts.QueueDepth = 16
	}
	if opts.DefaultTimeout == 0 {
		opts.DefaultTimeout = 2 * time.Second
	}
	if opts.ScriptRoots == nil {
		opts.ScriptRoots = []string{h.dir}
	}
	if opts.SoundsDir == "" {
		opts.SoundsDir = h.dir
	}
	h.eng = New(nil, h.plat, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		opts,
		func(e auth.Event) {
			h.mu.Lock()
			h.audits = append(h.audits, e)
			h.mu.Unlock()
		},
		func(e Event) {
			h.mu.Lock()
			h.events = append(h.events, e)
			h.mu.Unlock()
		})
	reg, err := BuildRegistry(h.plat, "test-host", "0.0.0-test", h.eng)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	h.eng.SetRegistry(reg)
	return h
}

// allScopes is the permissive scope set used by most tests.
func allScopes() auth.ScopeSet {
	return auth.NewScopeSet(auth.AllScopes())
}

// run executes an action with the given parameters.
func (h *harness) run(t *testing.T, actionType string, params string, scopes auth.ScopeSet) (Result, error) {
	t.Helper()
	if scopes == nil {
		scopes = allScopes()
	}
	return h.eng.Execute(context.Background(), Request{
		ExecutionID: NewExecutionID(),
		DeviceID:    "test-device",
		ProfileID:   "p",
		PageID:      "page",
		ButtonID:    "btn",
		ActionType:  actionType,
		Params:      json.RawMessage(params),
		Scopes:      scopes,
	})
}

func (h *harness) capturedEvents() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Event, len(h.events))
	copy(out, h.events)
	return out
}

func (h *harness) capturedAudits() []auth.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]auth.Event, len(h.audits))
	copy(out, h.audits)
	return out
}

// --- registry ------------------------------------------------------------

// TestRegistryHasNoDuplicateTypes is the invariant that keeps registration
// order from mattering: two packages claiming one type is always a bug.
func TestRegistryHasNoDuplicateTypes(t *testing.T) {
	h := newHarness(t, Options{})
	types := h.eng.Registry().Types()
	seen := make(map[string]bool, len(types))
	for _, ty := range types {
		if seen[ty] {
			t.Fatalf("action type %q is registered twice", ty)
		}
		seen[ty] = true
	}
	if len(types) < 30 {
		t.Errorf("only %d action types are registered; the catalogue is much larger", len(types))
	}
}

// TestRegistryRejectsDuplicates covers the guard itself.
func TestRegistryRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&noop{Base: NewBase("noop", auth.ScopeNone)}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(&noop{Base: NewBase("noop", auth.ScopeNone)}); err == nil {
		t.Fatal("Register accepted a duplicate type")
	}
	if err := r.Register(nil); err == nil {
		t.Fatal("Register accepted nil")
	}
}

// TestEveryCatalogueTypeIsRegistered pins the contract between the docs and the
// code: every action named in docs/PROTOCOL.md §10 must exist here, so the
// documentation cannot drift from the implementation.
func TestEveryCatalogueTypeIsRegistered(t *testing.T) {
	h := newHarness(t, Options{})
	reg := h.eng.Registry()

	// This list is transcribed from the catalogue table in docs/PROTOCOL.md §10.
	want := []string{
		"keyboard.shortcut", "keyboard.text", "keyboard.key",
		"mouse.click", "mouse.move", "mouse.move_absolute", "mouse.scroll",
		"launch_application", "open_url", "open_folder", "open_terminal",
		"run_script", "run_command",
		"media.play", "media.pause", "media.play_pause", "media.stop",
		"media.next", "media.previous", "media.now_playing",
		"volume.up", "volume.down", "volume.mute", "volume.set",
		"sound.play",
		"system.stats", "system.info", "system.lock", "system.sleep",
		"system.shutdown", "system.restart",
		"deck.open_page", "deck.back", "deck.change_profile", "deck.notify",
		"delay", "macro", "noop",
	}
	for _, ty := range want {
		if !reg.Has(ty) {
			t.Errorf("the catalogue lists %q but this host does not provide it", ty)
		}
	}
}

// TestEveryActionDeclaresItsScope checks that no action can be registered
// without a deliberate scope decision. ScopeNone is a valid answer for
// navigation and pure-flow actions; an empty string is not, because it would be
// indistinguishable from "forgot to decide".
func TestEveryActionDeclaresItsScope(t *testing.T) {
	h := newHarness(t, Options{})
	for _, info := range h.eng.Registry().Descriptions() {
		switch auth.Scope(info.Scope) {
		case auth.ScopeKeyboard, auth.ScopeMouse, auth.ScopeMedia, auth.ScopeApps,
			auth.ScopeScripts, auth.ScopeSystemRead, auth.ScopeSystemPower,
			auth.ScopeProfilesWrite, auth.ScopePlugins, auth.ScopeNone:
		default:
			t.Errorf("action %q declares the unknown scope %q", info.Type, info.Scope)
		}
	}
}

// TestRiskActionsCarryHighRiskScopes is a security invariant: the actions that
// can run code or power off the machine must not be reachable with a default
// pairing.
func TestRiskActionsCarryHighRiskScopes(t *testing.T) {
	h := newHarness(t, Options{})
	reg := h.eng.Registry()

	for _, ty := range []string{"run_script", "run_command"} {
		a, ok := reg.Lookup(ty)
		if !ok {
			t.Fatalf("%s is not registered", ty)
		}
		if a.Scope() != auth.ScopeScripts {
			t.Errorf("%s requires scope %q, want %q", ty, a.Scope(), auth.ScopeScripts)
		}
	}
	for _, ty := range []string{"system.shutdown", "system.restart", "system.sleep"} {
		a, ok := reg.Lookup(ty)
		if !ok {
			t.Fatalf("%s is not registered", ty)
		}
		if a.Scope() != auth.ScopeSystemPower {
			t.Errorf("%s requires scope %q, want %q", ty, a.Scope(), auth.ScopeSystemPower)
		}
	}
}

// --- scope enforcement ---------------------------------------------------

// TestScopeEnforcementBlocksExecution is the central security property: a
// missing scope must stop the action before it has any effect, not after.
func TestScopeEnforcementBlocksExecution(t *testing.T) {
	cases := []struct {
		actionType string
		params     string
		needScope  auth.Scope
	}{
		{"keyboard.shortcut", `{"keys":["CTRL","C"]}`, auth.ScopeKeyboard},
		{"mouse.click", `{}`, auth.ScopeMouse},
		{"media.play", `{}`, auth.ScopeMedia},
		{"launch_application", `{"target":"x"}`, auth.ScopeApps},
		{"run_command", `{"command":"echo hi"}`, auth.ScopeScripts},
		{"system.shutdown", `{"confirm":true}`, auth.ScopeSystemPower},
	}

	for _, tc := range cases {
		t.Run(tc.actionType, func(t *testing.T) {
			h := newHarness(t, Options{})
			// Grant everything except the scope under test.
			scopes := make([]auth.Scope, 0, len(auth.AllScopes()))
			for _, s := range auth.AllScopes() {
				if s != tc.needScope {
					scopes = append(scopes, s)
				}
			}

			_, err := h.run(t, tc.actionType, tc.params, auth.NewScopeSet(scopes))
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("Execute returned %v, want ErrForbidden", err)
			}
			if code := ErrorCode(err); code != "forbidden" {
				t.Errorf("ErrorCode = %q, want forbidden", code)
			}

			// Nothing may have happened.
			if n := len(h.input.shortcuts()); n != 0 {
				t.Errorf("the input layer received %d events despite the refusal", n)
			}
			if len(h.shell.commands) != 0 {
				t.Errorf("a command ran despite the refusal")
			}
			if len(h.launch.opened) != 0 {
				t.Errorf("an application was launched despite the refusal")
			}
			if len(h.power.calls) != 0 {
				t.Errorf("a power action ran despite the refusal: %v", h.power.calls)
			}

			// And the refusal must be audited.
			var audited bool
			for _, a := range h.capturedAudits() {
				if a.ActionType == tc.actionType && a.Reason == "forbidden_scope" {
					audited = true
				}
			}
			if !audited {
				t.Error("the refusal was not written to the audit log")
			}
		})
	}
}

// TestNavigationNeedsNoScope documents the deliberate exception: turning a page
// changes nothing on the host, so it must work for a device with no scopes.
func TestNavigationNeedsNoScope(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "deck.notify", `{"message":"hi"}`, auth.NewScopeSet(nil)); err != nil {
		t.Fatalf("deck.notify without scopes: %v", err)
	}
	if _, err := h.run(t, "noop", `{}`, auth.NewScopeSet(nil)); err != nil {
		t.Fatalf("noop without scopes: %v", err)
	}
}

// --- keyboard ------------------------------------------------------------

// TestKeyboardShortcutSendsAChord checks the common case and that the chord is
// sent as one atomic shortcut rather than three separate taps.
func TestKeyboardShortcutSendsAChord(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "keyboard.shortcut", `{"keys":["CTRL","SHIFT","P"]}`, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := h.input.shortcuts()
	if len(got) != 1 {
		t.Fatalf("expected exactly one shortcut call, got %d", len(got))
	}
	want := []platform.Key{"CTRL", "SHIFT", "P"}
	if len(got[0]) != len(want) {
		t.Fatalf("shortcut = %v, want %v", got[0], want)
	}
	for i := range want {
		if got[0][i] != want[i] {
			t.Errorf("key %d = %q, want %q", i, got[0][i], want[i])
		}
	}
	if h.input.modes[0] != platform.KeyPress {
		t.Errorf("mode = %q, want press", h.input.modes[0])
	}
}

// TestKeyboardShortcutPlatformMap covers the platform-varying form, which is how
// one profile works on two operating systems.
func TestKeyboardShortcutPlatformMap(t *testing.T) {
	h := newHarness(t, Options{})

	// The harness platform reports OSName "test", which no map defines, so the
	// action must fail with a clear message rather than sending nothing.
	_, err := h.run(t, "keyboard.shortcut", `{"keys":{"windows":["CTRL","C"],"linux":["CTRL","INSERT"]}}`, nil)
	if err == nil {
		t.Fatal("a platform map with no entry for this host was accepted")
	}
	if !strings.Contains(err.Error(), "no entry for test") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(h.input.shortcuts()) != 0 {
		t.Error("keys were injected despite the failure")
	}

	// With an entry for the host's platform it works.
	h2 := newHarness(t, Options{})
	h2.plat.OSName = "windows"
	if _, err := h2.run(t, "keyboard.shortcut", `{"keys":{"windows":["CTRL","C"]}}`, nil); err != nil {
		t.Fatalf("Execute with a matching platform entry: %v", err)
	}
	if len(h2.input.shortcuts()) != 1 {
		t.Error("the shortcut was not sent")
	}
}

// TestKeyboardShortcutValidation covers the parameter checks.
func TestKeyboardShortcutValidation(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   string
	}{
		{"empty list", `{"keys":[]}`, "at least one key"},
		{"missing keys", `{}`, "keys is required"},
		{"unknown key", `{"keys":["HYPERSPACE"]}`, "unknown key"},
		{"too many keys", `{"keys":["A","B","C","D","E","F","G","H","I"]}`, "at most 8"},
		{"bad mode", `{"keys":["A"],"mode":"slam"}`, "must be press, down or up"},
		{"bad interval", `{"keys":["A"],"interval_ms":-5}`, "interval_ms must be"},
		{"unknown field", `{"keys":["A"],"repet":2}`, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{})
			_, err := h.run(t, "keyboard.shortcut", tc.params, nil)
			if err == nil {
				t.Fatalf("Execute accepted %s", tc.params)
			}
			if !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("error %v is not ErrInvalidParams", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
			if len(h.input.shortcuts()) != 0 {
				t.Error("input was injected despite invalid parameters")
			}
		})
	}
}

// TestKeyboardTextBounds checks the length limit and the empty case.
func TestKeyboardTextBounds(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "keyboard.text", `{"text":"hello world"}`, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(h.input.texts) != 1 || h.input.texts[0] != "hello world" {
		t.Fatalf("texts = %v", h.input.texts)
	}

	long := strings.Repeat("x", MaxTextBytes+1)
	if _, err := h.run(t, "keyboard.text", `{"text":"`+long+`"}`, nil); err == nil {
		t.Fatal("an over-long text was accepted")
	}
	if _, err := h.run(t, "keyboard.text", `{"text":""}`, nil); err == nil {
		t.Fatal("an empty text was accepted")
	}
}

// --- mouse ---------------------------------------------------------------

// TestMouseClick covers the defaults and the count limit.
func TestMouseClick(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "mouse.click", `{}`, nil); err != nil {
		t.Fatalf("Execute with defaults: %v", err)
	}
	if len(h.input.clicks) != 1 || h.input.clicks[0] != platform.MouseLeft {
		t.Fatalf("clicks = %v, want one left click", h.input.clicks)
	}

	h2 := newHarness(t, Options{})
	if _, err := h2.run(t, "mouse.click", `{"button":"right","count":2}`, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(h2.input.clicks) != 2 {
		t.Fatalf("clicks = %v, want two", h2.input.clicks)
	}

	if _, err := h2.run(t, "mouse.click", `{"count":99}`, nil); err == nil {
		t.Fatal("an absurd click count was accepted")
	}
	if _, err := h2.run(t, "mouse.click", `{"button":"thumb"}`, nil); err == nil {
		t.Fatal("an unknown mouse button was accepted")
	}
}

// TestMouseMoveAbsoluteBounds covers the normalised coordinate range, which is
// what makes a deck work on any monitor layout.
func TestMouseMoveAbsoluteBounds(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "mouse.move_absolute", `{"x":0.5,"y":0.25}`, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(h.input.absolute) != 1 || h.input.absolute[0] != [2]float64{0.5, 0.25} {
		t.Fatalf("absolute moves = %v", h.input.absolute)
	}
	for _, bad := range []string{`{"x":1.5,"y":0.5}`, `{"x":0.5}`, `{"x":-0.1,"y":0.5}`} {
		if _, err := h.run(t, "mouse.move_absolute", bad, nil); err == nil {
			t.Errorf("accepted out-of-range position %s", bad)
		}
	}
}

// TestMouseScrollValidation covers the relative/absolute distinction and the
// notches limit.
func TestMouseScrollValidation(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "mouse.scroll", `{"dy":3}`, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(h.input.scrolls) != 1 || h.input.scrolls[0] != [2]int{0, 3} {
		t.Fatalf("scrolls = %v", h.input.scrolls)
	}
	if _, err := h.run(t, "mouse.scroll", `{}`, nil); err == nil {
		t.Fatal("a scroll with no direction was accepted")
	}
	if _, err := h.run(t, "mouse.scroll", `{"dy":1000}`, nil); err == nil {
		t.Fatal("an absurd scroll amount was accepted")
	}
}

// --- applications --------------------------------------------------------

// TestLaunchApplicationResolvesPlatformTarget covers the platform map form.
func TestLaunchApplicationResolvesPlatformTarget(t *testing.T) {
	h := newHarness(t, Options{})
	h.plat.OSName = "linux"

	if _, err := h.run(t, "launch_application", `{"target":"htop","args":["-d","5"]}`, nil); err != nil {
		t.Fatalf("Execute with a plain target: %v", err)
	}
	if len(h.launch.opened) != 1 || h.launch.opened[0] != "htop" {
		t.Fatalf("opened = %v", h.launch.opened)
	}

	if _, err := h.run(t, "launch_application", `{"target":{"linux":"alacritty","windows":"wt.exe"}}`, nil); err != nil {
		t.Fatalf("Execute with a platform map: %v", err)
	}
	if h.launch.opened[1] != "alacritty" {
		t.Fatalf("resolved target = %q, want alacritty", h.launch.opened[1])
	}

	// A map without an entry for this host is an error, not a silent no-op.
	if _, err := h.run(t, "launch_application", `{"target":{"windows":"wt.exe"}}`, nil); err == nil {
		t.Fatal("a platform map with no matching entry was accepted")
	}
}

// TestOpenURLSanitization is the security-relevant part of open_url: an
// unchecked URL on Windows is an arbitrary-execution primitive.
func TestOpenURLSanitization(t *testing.T) {
	h := newHarness(t, Options{})

	ok := []string{
		`{"url":"https://example.com/a?b=c"}`,
		`{"url":"http://192.168.1.10:8080"}`,
		`{"url":"mailto:someone@example.com"}`,
	}
	for _, params := range ok {
		if _, err := h.run(t, "open_url", params, nil); err != nil {
			t.Errorf("rejected a valid URL %s: %v", params, err)
		}
	}

	bad := []string{
		`{"url":"/etc/passwd"}`,
		`{"url":"C:\\Windows\\System32\\calc.exe"}`,
		`{"url":"file:///etc/shadow"}`,
		`{"url":"javascript:alert(1)"}`,
		`{"url":""}`,
	}
	for _, params := range bad {
		if _, err := h.run(t, "open_url", params, nil); err == nil {
			t.Errorf("accepted a dangerous URL %s", params)
		}
	}
	if len(h.launch.urls) != len(ok) {
		t.Errorf("opened %d URLs, want %d", len(h.launch.urls), len(ok))
	}
}

// --- scripts -------------------------------------------------------------

// TestRunScriptConfinedToRoots is the path-traversal mitigation
// (docs/SECURITY.md T7).
func TestRunScriptConfinedToRoots(t *testing.T) {
	h := newHarness(t, Options{})
	script := filepath.Join(h.dir, "hello.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o700); err != nil {
		t.Fatalf("writing the script: %v", err)
	}

	// A file inside the allowed root works.
	if _, err := h.run(t, "run_script", `{"path":"hello.sh"}`, nil); err != nil {
		t.Fatalf("Execute with an allowed path: %v", err)
	}
	if len(h.shell.scripts) != 1 {
		t.Fatalf("scripts = %v", h.shell.scripts)
	}
	if !strings.HasSuffix(h.shell.scripts[0].Path, "hello.sh") {
		t.Errorf("resolved path = %q", h.shell.scripts[0].Path)
	}

	// Traversal out of the root is refused.
	outside := filepath.Join(filepath.Dir(h.dir), "outside.sh")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\necho nope\n"), 0o700); err != nil {
		t.Fatalf("writing the outside script: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, bad := range []string{"../outside.sh", "../../etc/passwd", "/etc/passwd"} {
		if _, err := h.run(t, "run_script", `{"path":"`+strings.ReplaceAll(bad, `\`, `\\`)+`"}`, nil); err == nil {
			t.Errorf("accepted the path %q", bad)
		}
	}
	if len(h.shell.scripts) != 1 {
		t.Errorf("a refused path still reached the shell: %v", h.shell.scripts)
	}
}

// TestRunScriptSymlinkEscapeRefused covers the subtle case: a link inside an
// allowed root that points outside it must not be followed.
func TestRunScriptSymlinkEscapeRefused(t *testing.T) {
	h := newHarness(t, Options{})
	target := filepath.Join(filepath.Dir(h.dir), "target.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho nope\n"), 0o700); err != nil {
		t.Fatalf("writing the target: %v", err)
	}
	t.Cleanup(func() { os.Remove(target) })

	link := filepath.Join(h.dir, "escape.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	if _, err := h.run(t, "run_script", `{"path":"escape.sh"}`, nil); err == nil {
		t.Fatal("a symlink escaping the allowed root was followed")
	}
}

// TestRunScriptAbsolutePathsOptIn covers the explicit operator escape hatch.
func TestRunScriptAbsolutePathsOptIn(t *testing.T) {
	h := newHarness(t, Options{AllowAbsolutePaths: true})
	outside := filepath.Join(t.TempDir(), "anywhere.sh")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := h.run(t, "run_script", `{"path":"`+strings.ReplaceAll(outside, `\`, `\\`)+`"}`, nil); err != nil {
		t.Fatalf("Execute with --allow-absolute-paths: %v", err)
	}
}

// TestRunScriptRejectsDirectory checks that pointing a button at a directory
// produces a clear error rather than a confusing exec failure.
func TestRunScriptRejectsDirectory(t *testing.T) {
	h := newHarness(t, Options{})
	sub := filepath.Join(h.dir, "adir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := h.run(t, "run_script", `{"path":"adir"}`, nil)
	if err == nil {
		t.Fatal("a directory was accepted as a script")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunScriptExitCodeIsAnError checks that a failing script surfaces as a
// failure with its stderr attached, rather than a silent success.
func TestRunScriptExitCodeIsAnError(t *testing.T) {
	h := newHarness(t, Options{})
	script := filepath.Join(h.dir, "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatalf("writing: %v", err)
	}
	h.shell.result = platform.ExecResult{ExitCode: 3, Stderr: "boom\nmore\n"}

	_, err := h.run(t, "run_script", `{"path":"fail.sh"}`, nil)
	if err == nil {
		t.Fatal("a non-zero exit code was reported as success")
	}
	if !strings.Contains(err.Error(), "exited with code 3") {
		t.Errorf("error %q does not mention the exit code", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not carry the stderr", err)
	}
}

// TestRunCommandValidation covers the shell whitelist.
func TestRunCommandValidation(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "run_command", `{"command":"echo hi"}`, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(h.shell.commands) != 1 || h.shell.commands[0].Command != "echo hi" {
		t.Fatalf("commands = %v", h.shell.commands)
	}
	for _, bad := range []string{`{}`, `{"command":""}`, `{"command":"x","shell":"csh"}`} {
		if _, err := h.run(t, "run_command", bad, nil); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// --- media, system -------------------------------------------------------

// TestMediaVerbsRouteToTheRightCall checks the verb table and the scope.
func TestMediaVerbsRouteToTheRightCall(t *testing.T) {
	h := newHarness(t, Options{})
	verbs := map[string]string{
		"media.play":       "play",
		"media.pause":      "pause",
		"media.play_pause": "play_pause",
		"media.stop":       "stop",
		"media.next":       "next",
		"media.previous":   "previous",
		"volume.up":        "volume_up",
		"volume.down":      "volume_down",
		"volume.mute":      "mute",
	}
	for actionType, want := range verbs {
		h.media.verbs = nil
		if _, err := h.run(t, actionType, `{}`, nil); err != nil {
			t.Fatalf("%s: %v", actionType, err)
		}
		if len(h.media.verbs) != 1 || h.media.verbs[0] != want {
			t.Errorf("%s produced %v, want %q", actionType, h.media.verbs, want)
		}
	}

	if _, err := h.run(t, "volume.set", `{"level":40}`, nil); err != nil {
		t.Fatalf("volume.set: %v", err)
	}
	if _, err := h.run(t, "volume.set", `{"level":101}`, nil); err == nil {
		t.Fatal("volume.set accepted an out-of-range level")
	}
}

// TestSoundPlayConfinementIsThePoint is the security test for sound.play: the
// file parameter becomes a path, so anything that is not a bare name inside the
// sounds directory must be refused before the platform is called.
func TestSoundPlayConfinementIsThePoint(t *testing.T) {
	cases := []struct {
		name   string
		params string
	}{
		{"parent reference", `{"file":"../secret.wav"}`},
		{"absolute path", `{"file":"/etc/passwd.wav"}`},
		{"windows absolute path", `{"file":"C:\\\\Windows\\\\win.wav"}`},
		{"nested path", `{"file":"sub/boom.wav"}`},
		{"empty name", `{"file":""}`},
		{"no extension", `{"file":"boom"}`},
		{"non-audio extension", `{"file":"boom.exe"}`},
		{"unknown field", `{"file":"boom.wav","loud":true}`},
		{"volume too high", `{"file":"boom.wav","volume":101}`},
		{"volume negative", `{"file":"boom.wav","volume":-1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Options{})
			if _, err := h.run(t, "sound.play", tc.params, nil); err == nil {
				t.Fatalf("accepted %s", tc.params)
			}
			if len(h.sound.played) != 0 {
				t.Errorf("a refused file still reached the platform: %v", h.sound.played)
			}
		})
	}
}

// TestSoundPlayResolvesInsideTheDirectory covers the happy path and the two
// defaults that matter: non-blocking, and no volume unless one was asked for.
func TestSoundPlayResolvesInsideTheDirectory(t *testing.T) {
	h := newHarness(t, Options{})
	if err := os.WriteFile(filepath.Join(h.dir, "boom.wav"), []byte("RIFF"), 0o600); err != nil {
		t.Fatalf("writing the sound: %v", err)
	}

	res, err := h.run(t, "sound.play", `{"file":"boom.wav"}`, nil)
	if err != nil {
		t.Fatalf("sound.play: %v", err)
	}
	if len(h.sound.played) != 1 {
		t.Fatalf("played = %v", h.sound.played)
	}
	if filepath.Dir(h.sound.played[0]) != h.dir {
		t.Errorf("resolved path = %q, want it inside %q", h.sound.played[0], h.dir)
	}
	if h.sound.volumes[0] != -1 {
		t.Errorf("volume = %d, want -1 (not asked for)", h.sound.volumes[0])
	}
	if h.sound.blocking[0] {
		t.Error("a press blocked by default")
	}
	if res.Detail != "played boom.wav" {
		t.Errorf("detail = %q", res.Detail)
	}

	// An explicit volume and blocking flag are passed through.
	if _, err := h.run(t, "sound.play", `{"file":"boom.wav","volume":30,"blocking":true}`, nil); err != nil {
		t.Fatalf("sound.play with volume: %v", err)
	}
	if h.sound.volumes[1] != 30 || !h.sound.blocking[1] {
		t.Errorf("volume/blocking = %d/%v, want 30/true", h.sound.volumes[1], h.sound.blocking[1])
	}
}

// TestSoundPlayReportsAnIgnoredVolume checks the honesty requirement: a platform
// that cannot set a level must say so rather than claiming one was applied.
func TestSoundPlayReportsAnIgnoredVolume(t *testing.T) {
	h := newHarness(t, Options{})
	h.sound.volumeIgnored = true
	if err := os.WriteFile(filepath.Join(h.dir, "boom.wav"), []byte("RIFF"), 0o600); err != nil {
		t.Fatalf("writing the sound: %v", err)
	}

	res, err := h.run(t, "sound.play", `{"file":"boom.wav","volume":20}`, nil)
	if err != nil {
		t.Fatalf("sound.play: %v", err)
	}
	if !strings.Contains(res.Detail, "cannot set the volume") {
		t.Errorf("detail = %q, want it to admit the volume was ignored", res.Detail)
	}
}

// TestSoundPlayRefusesASymlinkOutOfTheDirectory covers the subtle escape: a link
// planted in the sounds directory that points at a file elsewhere must not be
// followed, because the name check alone would accept it.
func TestSoundPlayRefusesASymlinkOutOfTheDirectory(t *testing.T) {
	h := newHarness(t, Options{})
	outside := filepath.Join(filepath.Dir(h.dir), "outside.wav")
	if err := os.WriteFile(outside, []byte("RIFF"), 0o600); err != nil {
		t.Fatalf("writing the target: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	if err := os.Symlink(outside, filepath.Join(h.dir, "escape.wav")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := h.run(t, "sound.play", `{"file":"escape.wav"}`, nil); err == nil {
		t.Fatal("a symlink escaping the sounds directory was followed")
	}
}

// TestSoundPlayRefusesAMissingFile checks that a stale reference gives a clear
// error rather than reaching the platform with a path that is not there.
func TestSoundPlayRefusesAMissingFile(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "sound.play", `{"file":"gone.wav"}`, nil); err == nil {
		t.Fatal("a missing file was accepted")
	}
	if len(h.sound.played) != 0 {
		t.Errorf("a missing file reached the platform: %v", h.sound.played)
	}
}

// TestPowerActionsRequireConfirm is the second barrier in front of an
// irreversible action: the scope alone is not enough.
func TestPowerActionsRequireConfirm(t *testing.T) {
	for _, actionType := range []string{"system.shutdown", "system.restart", "system.sleep"} {
		t.Run(actionType, func(t *testing.T) {
			h := newHarness(t, Options{})
			_, err := h.run(t, actionType, `{}`, nil)
			if err == nil {
				t.Fatalf("%s ran without confirm", actionType)
			}
			if !strings.Contains(err.Error(), "confirm") {
				t.Fatalf("error %q does not explain the confirm requirement", err)
			}
			if len(h.power.calls) != 0 {
				t.Fatalf("%s reached the platform without confirm", actionType)
			}

			if _, err := h.run(t, actionType, `{"confirm":true}`, nil); err != nil {
				t.Fatalf("%s with confirm: %v", actionType, err)
			}
			if len(h.power.calls) != 1 {
				t.Fatalf("%s did not reach the platform with confirm: %v", actionType, h.power.calls)
			}
		})
	}
}

// TestLockNeedsNoConfirm checks that the reversible power action stays a
// one-tap button, since requiring confirm for a lock would defeat its purpose.
func TestLockNeedsNoConfirm(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.run(t, "system.lock", `{}`, nil); err != nil {
		t.Fatalf("system.lock: %v", err)
	}
	if len(h.power.calls) != 1 || h.power.calls[0] != "lock" {
		t.Fatalf("power calls = %v", h.power.calls)
	}
}

// TestUnsupportedPowerIsReported checks that a host which cannot power off says
// so, instead of pretending to succeed.
func TestUnsupportedPowerIsReported(t *testing.T) {
	h := newHarness(t, Options{})
	h.power.caps = platform.PowerCaps{Lock: true} // no shutdown

	_, err := h.run(t, "system.shutdown", `{"confirm":true}`, nil)
	if err == nil {
		t.Fatal("a shutdown on a host that cannot shut down was reported as success")
	}
	if !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("error %v is not ErrUnsupported", err)
	}
	if len(h.power.calls) != 0 {
		t.Errorf("the platform was called anyway: %v", h.power.calls)
	}
	if ErrorCode(err) != "unsupported" {
		t.Errorf("ErrorCode = %q, want unsupported", ErrorCode(err))
	}
}

// --- macro ---------------------------------------------------------------

// TestMacroRunsStepsInOrder is the headline macro behaviour.
func TestMacroRunsStepsInOrder(t *testing.T) {
	h := newHarness(t, Options{})
	params := `{
	  "steps": [
	    {"type": "keyboard.shortcut", "params": {"keys": ["CTRL","SHIFT","P"]}},
	    {"type": "delay", "params": {"ms": 5}},
	    {"type": "keyboard.text", "params": {"text": "hello"}},
	    {"type": "keyboard.key", "params": {"key": "ENTER"}}
	  ]
	}`
	res, err := h.run(t, "macro", params, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	out, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("output is %T, want a map", res.Output)
	}
	if out["steps_run"] != 4 {
		t.Errorf("steps_run = %v, want 4", out["steps_run"])
	}

	// Every key-producing step lands in one ordered log: the chord, then the
	// trailing ENTER. Two entries, in that order, is the whole point of a macro.
	keys := h.input.shortcuts()
	if len(keys) != 2 {
		t.Fatalf("expected 2 key events (the chord and ENTER), got %d: %v", len(keys), keys)
	}
	if len(keys[0]) != 3 || keys[0][0] != "CTRL" {
		t.Errorf("the first key event is %v, want the CTRL+SHIFT+P chord", keys[0])
	}
	if len(keys[1]) != 1 || keys[1][0] != "ENTER" {
		t.Errorf("the last key event is %v, want ENTER", keys[1])
	}
	if len(h.input.texts) != 1 || h.input.texts[0] != "hello" {
		t.Errorf("texts = %v", h.input.texts)
	}
}

// TestMacroStepScopeIsChecked is the anti-escalation property: a macro cannot be
// used to run something the device could not run directly.
func TestMacroStepScopeIsChecked(t *testing.T) {
	h := newHarness(t, Options{})
	params := `{"steps":[{"type":"run_command","params":{"command":"rm -rf /"}}]}`

	// The device may use keyboard and macros but not scripts.
	scopes := auth.NewScopeSet([]auth.Scope{auth.ScopeKeyboard})
	_, err := h.run(t, "macro", params, scopes)
	if err == nil {
		t.Fatal("a macro escalated to a scope the device does not hold")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error %v is not ErrForbidden", err)
	}
	if len(h.shell.commands) != 0 {
		t.Fatal("the forbidden step ran anyway")
	}
}

// TestMacroAbortsOnFailureByDefault checks the default stop behaviour.
func TestMacroAbortsOnFailureByDefault(t *testing.T) {
	h := newHarness(t, Options{})
	h.input.failOn = "CTRL"

	params := `{
	  "steps": [
	    {"type": "keyboard.shortcut", "params": {"keys": ["CTRL","C"]}},
	    {"type": "keyboard.text", "params": {"text": "never"}}
	  ]
	}`
	_, err := h.run(t, "macro", params, nil)
	if err == nil {
		t.Fatal("a macro with a failing step reported success")
	}
	if !strings.Contains(err.Error(), "aborted at step 0") {
		t.Errorf("error %q does not name the failing step", err)
	}
	if len(h.input.texts) != 0 {
		t.Error("the macro continued past a failed step")
	}
}

// TestMacroContinuesOnErrorWhenAsked covers the opt-out.
func TestMacroContinuesOnErrorWhenAsked(t *testing.T) {
	h := newHarness(t, Options{})
	// Only the first step fails; the second must still run.
	h.input.failOn = "CTRL"

	params := `{
	  "steps": [
	    {"type": "keyboard.shortcut", "params": {"keys": ["CTRL","C"]}, "on_error": "continue"},
	    {"type": "keyboard.text", "params": {"text": "still here"}}
	  ]
	}`
	res, err := h.run(t, "macro", params, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Output.(map[string]any)
	if out["steps_run"] != 2 {
		t.Errorf("steps_run = %v, want 2", out["steps_run"])
	}
	if len(h.input.texts) != 1 {
		t.Errorf("the second step did not run: %v", h.input.texts)
	}
}

// TestMacroNestingWorks checks that a macro can contain a macro, which is the
// payoff of steps being ordinary actions.
func TestMacroNestingWorks(t *testing.T) {
	h := newHarness(t, Options{})
	inner := `{"type":"macro","params":{"steps":[{"type":"keyboard.key","params":{"key":"ENTER"}}]}}`
	params := `{"steps":[` + inner + `,{"type":"keyboard.text","params":{"text":"after"}}]}`

	if _, err := h.run(t, "macro", params, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(h.input.keys) != 1 {
		t.Errorf("the nested step did not run: %v", h.input.keys)
	}
	if len(h.input.texts) != 1 {
		t.Errorf("the outer step did not run: %v", h.input.texts)
	}
}

// TestMacroDepthBounded checks the recursion guard at run time too, since a
// profile could be built programmatically without going through the validator.
func TestMacroDepthBounded(t *testing.T) {
	h := newHarness(t, Options{MaxMacroDepth: 2})
	step := `{"type":"noop","params":{}}`
	for range 4 {
		step = `{"type":"macro","params":{"steps":[` + step + `]}}`
	}
	if _, err := h.run(t, "macro", step, nil); err == nil {
		t.Fatal("an over-deep macro nesting was accepted")
	}
}

// TestMacroStepValidationHappensBeforeRunning is the important half of
// validation: a macro with a broken step must not run its earlier steps first
// and leave the machine half-configured.
func TestMacroStepValidationHappensBeforeRunning(t *testing.T) {
	h := newHarness(t, Options{})
	params := `{
	  "steps": [
	    {"type": "keyboard.shortcut", "params": {"keys": ["CTRL","C"]}},
	    {"type": "keyboard.shortcut", "params": {"keys": ["NOTAKEY"]}}
	  ]
	}`
	if _, err := h.run(t, "macro", params, nil); err == nil {
		t.Fatal("a macro with an invalid step was accepted")
	}
	if len(h.input.shortcuts()) != 0 {
		t.Fatalf("the first step ran despite the second being invalid: %v", h.input.shortcuts())
	}
}

// TestMacroUnknownStepRefused covers a step naming an action this host lacks.
func TestMacroUnknownStepRefused(t *testing.T) {
	h := newHarness(t, Options{})
	params := `{"steps":[{"type":"obs.start_streaming","params":{}}]}`
	_, err := h.run(t, "macro", params, nil)
	if err == nil {
		t.Fatal("a macro step for an uninstalled action was accepted")
	}
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("error %v is not ErrUnknownAction", err)
	}
}

// TestMacroDelayHonoursCancellation is the cancellation contract for a macro
// step: a long delay must stop promptly when the execution is cancelled.
func TestMacroDelayHonoursCancellation(t *testing.T) {
	h := newHarness(t, Options{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.eng.Execute(ctx, Request{
			ExecutionID: "ex-cancel",
			DeviceID:    "d",
			ActionType:  "macro",
			Params:      json.RawMessage(`{"steps":[{"type":"delay","params":{"ms":60000}}]}`),
			Scopes:      allScopes(),
		})
		done <- err
	}()

	// Give the macro a moment to enter the delay, then cancel it.
	time.Sleep(50 * time.Millisecond)
	if !h.eng.Cancel("ex-cancel") {
		t.Fatal("Cancel did not find the running execution")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled macro reported success")
		}
		if ErrorCode(err) != "cancelled" {
			t.Errorf("ErrorCode = %q, want cancelled", ErrorCode(err))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling a macro delay did not return within 3 seconds")
	}
}

// TestCancelUnknownExecutionIsReported covers the error path.
func TestCancelUnknownExecutionIsReported(t *testing.T) {
	h := newHarness(t, Options{})
	if h.eng.Cancel("ex-does-not-exist") {
		t.Error("Cancel reported success for an unknown execution")
	}
}

// TestConcurrentLimitIsEnforced checks the bounded admission: a full queue is
// answered immediately rather than blocking, which is what keeps one device
// from stalling another.
func TestConcurrentLimitIsEnforced(t *testing.T) {
	h := newHarness(t, Options{MaxConcurrent: 1, QueueDepth: 1})
	h.input.delay = 300 * time.Millisecond

	results := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := h.eng.Execute(context.Background(), Request{
				ExecutionID: NewExecutionID(),
				ActionType:  "keyboard.key",
				Params:      json.RawMessage(`{"key":"A"}`),
				Scopes:      allScopes(),
			})
			results <- err
		}()
	}

	var busy int
	for range 3 {
		if err := <-results; errors.Is(err, ErrBusy) {
			busy++
		}
	}
	if busy == 0 {
		t.Error("the queue never rejected a request; the admission bound is not working")
	}
	if ErrorCode(ErrBusy) != "rate_limited" {
		t.Errorf("ErrBusy maps to %q, want rate_limited", ErrorCode(ErrBusy))
	}
}

// TestUnknownActionIsUnsupported covers the dispatch miss.
func TestUnknownActionIsUnsupported(t *testing.T) {
	h := newHarness(t, Options{})
	_, err := h.run(t, "definitely.not.registered", `{}`, nil)
	if err == nil {
		t.Fatal("an unknown action type was accepted")
	}
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("error %v is not ErrUnknownAction", err)
	}
	if ErrorCode(err) != "unsupported" {
		t.Errorf("ErrorCode = %q, want unsupported", ErrorCode(err))
	}
}

// --- button state --------------------------------------------------------

// TestToggleStateFlipsAndEmits checks the host-side latch and the emitted event.
func TestToggleStateFlipsAndEmits(t *testing.T) {
	h := newHarness(t, Options{})
	prof := testProfile(t, "toggle")
	page := &prof.Pages[0]
	btn := &page.Buttons[0]

	h.eng.ApplyButtonState(prof, page, btn)
	h.eng.ApplyButtonState(prof, page, btn)

	states := h.eng.ButtonStates("p")
	if len(states) != 1 {
		t.Fatalf("expected 1 tracked state, got %d", len(states))
	}
	if states[0].Active == nil {
		t.Fatal("no active flag was recorded")
	}
	// Two flips return to the original value.
	if *states[0].Active {
		t.Error("two toggles did not return to off")
	}

	// Each change must have produced an event.
	events := h.capturedEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 state events, got %d", len(events))
	}
	if events[0].Type != "event.button.state" {
		t.Errorf("event type = %q", events[0].Type)
	}
}

// TestStateCoalescingDropsDuplicates is the property that keeps a counter button
// from flooding the socket: an unchanged value must not be re-sent.
func TestStateCoalescingDropsDuplicates(t *testing.T) {
	h := newHarness(t, Options{})
	value := 42.0

	for range 5 {
		h.eng.SetButtonState(ButtonState{
			ProfileID: "p", PageID: "page", ButtonID: "cpu",
			Type: "progress", Value: &value,
		})
	}
	if n := len(h.capturedEvents()); n != 1 {
		t.Fatalf("expected 1 event for 5 identical states, got %d", n)
	}

	value = 43.0
	h.eng.SetButtonState(ButtonState{
		ProfileID: "p", PageID: "page", ButtonID: "cpu",
		Type: "progress", Value: &value,
	})
	if n := len(h.capturedEvents()); n != 2 {
		t.Fatalf("a changed value was not emitted: %d events", n)
	}
}

// TestRadioGroupIsExclusive checks that exactly one button of a group is on.
func TestRadioGroupIsExclusive(t *testing.T) {
	h := newHarness(t, Options{})
	prof := testProfile(t, "radio")
	page := &prof.Pages[0]
	if len(page.Buttons) < 3 {
		t.Fatalf("the fixture needs three radio buttons, has %d", len(page.Buttons))
	}

	h.eng.ApplyButtonState(prof, page, &page.Buttons[0])
	h.eng.ApplyButtonState(prof, page, &page.Buttons[2])

	on := make([]string, 0, 3)
	for _, st := range h.eng.ButtonStates("p") {
		if st.Active != nil && *st.Active {
			on = append(on, st.ButtonID)
		}
	}
	if len(on) != 1 || on[0] != "r3" {
		t.Fatalf("active radio buttons = %v, want exactly [r3]", on)
	}
}

// TestForgetProfileDropsStates covers the reload path.
func TestForgetProfileDropsStates(t *testing.T) {
	h := newHarness(t, Options{})
	h.eng.SetButtonState(ButtonState{ProfileID: "p", PageID: "page", ButtonID: "b", Type: "status"})
	if len(h.eng.ButtonStates("p")) != 1 {
		t.Fatal("the state was not recorded")
	}
	h.eng.ForgetProfile("p")
	if len(h.eng.ButtonStates("p")) != 0 {
		t.Fatal("the state survived a profile reload")
	}
}

// TestValidateButtonStatesCatchesUnknownMetric covers the profile/host
// cross-check.
func TestValidateButtonStatesCatchesUnknownMetric(t *testing.T) {
	prof := testProfile(t, "telemetry")

	if err := ValidateButtonStates(prof, []string{"cpu.usage"}); err == nil {
		t.Fatal("a telemetry button bound to an unavailable metric was accepted")
	} else if !strings.Contains(err.Error(), "not produced by this host") {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := ValidateButtonStates(prof, []string{"cpu.usage", "mem.used_pct"}); err != nil {
		t.Fatalf("a valid binding was rejected: %v", err)
	}

	// A host that produces no metrics at all is a host limitation, not a
	// profile error.
	if err := ValidateButtonStates(prof, nil); err != nil {
		t.Fatalf("an empty metric list should skip the check: %v", err)
	}
}

// TestErrorCodeMapping pins the mapping from engine errors to protocol codes,
// because the client branches on these strings.
func TestErrorCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrUnknownAction, "unsupported"},
		{ErrForbidden, "forbidden"},
		{ErrInvalidParams, "invalid_argument"},
		{ErrBusy, "rate_limited"},
		{ErrCancelled, "cancelled"},
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "action_failed"},
		{platform.ErrUnsupported, "unsupported"},
		{errors.New("something else"), "action_failed"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := ErrorCode(tc.err); got != tc.want {
			t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// testProfile builds a small profile with one button of the requested state
// kind, used by the state tests.
func testProfile(t *testing.T, stateType string) *profile.Profile {
	t.Helper()
	raw := `{
	  "schema": 1, "id": "p", "name": "P",
	  "icon": {"type": "emoji", "value": "P"},
	  "settings": {"grid": {"columns": 3, "rows": 3}},
	  "pages": [{
	    "id": "page", "name": "Page",
	    "buttons": [
	      {"id":"r1","label":"R1","icon":{"type":"emoji","value":"1"},"cell":{"row":0,"column":0},"state":{"type":"` + stateType + `","group":"g","metric":"mem.used_pct"},"on_press":{"type":"noop","params":{}}},
	      {"id":"r2","label":"R2","icon":{"type":"emoji","value":"2"},"cell":{"row":0,"column":1},"state":{"type":"` + stateType + `","group":"g","metric":"mem.used_pct"},"on_press":{"type":"noop","params":{}}},
	      {"id":"r3","label":"R3","icon":{"type":"emoji","value":"3"},"cell":{"row":0,"column":2},"state":{"type":"` + stateType + `","group":"g","metric":"mem.used_pct"},"on_press":{"type":"noop","params":{}}}
	    ]
	  }]
	}`
	doc, err := profile.Load([]byte(raw), nil)
	if err != nil {
		t.Fatalf("building the test profile: %v", err)
	}
	doc.ApplyDefaults()
	return doc
}
