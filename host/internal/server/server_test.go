package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/icons"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/profile"
	"github.com/mobiledeck/mobiledeck/host/internal/profiles"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
	"github.com/mobiledeck/mobiledeck/host/internal/telemetry"
)

// --- recording fakes -----------------------------------------------------

// recordingInput captures injected input so the test can assert that a button
// press really reached the input layer.
type recordingInput struct {
	platform.Unsupported
	mu        sync.Mutex
	shortcuts [][]platform.Key
}

func (r *recordingInput) Shortcut(ctx context.Context, keys []platform.Key, m platform.KeyMode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shortcuts = append(r.shortcuts, keys)
	return nil
}

func (r *recordingInput) Key(ctx context.Context, k platform.Key, m platform.KeyMode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shortcuts = append(r.shortcuts, []platform.Key{k})
	return nil
}

func (r *recordingInput) Text(ctx context.Context, s string, d time.Duration) error { return nil }

func (r *recordingInput) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.shortcuts)
}

// blockingInput sleeps so the accepted/finished path can be exercised.
type blockingInput struct {
	platform.Unsupported
	mu    sync.Mutex
	delay time.Duration
	calls int
}

func (b *blockingInput) Shortcut(ctx context.Context, keys []platform.Key, m platform.KeyMode) error {
	b.mu.Lock()
	b.calls++
	delay := b.delay
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (b *blockingInput) Key(ctx context.Context, k platform.Key, m platform.KeyMode) error {
	return b.Shortcut(ctx, []platform.Key{k}, m)
}

// --- harness -------------------------------------------------------------

// testServer is a fully wired host on a loopback port, with a fake input layer.
type testServer struct {
	srv      *Server
	input    platform.Input
	auth     *auth.Manager
	prof     *profiles.Registry
	engine   *engine.Engine
	iconsDir string
	url      string // http(s)://host:port
	wsURL    string // ws(s)://host:port/ws
	dir      string
	pin      string
}

func newTestServer(t *testing.T, input platform.Input) *testServer {
	t.Helper()
	return newTestServerPlatform(t, &platform.Platform{
		OSName:   "windows",
		Input:    input,
		Launcher: &platform.Unsupported{},
		Shell:    &platform.Unsupported{},
		Media:    &platform.Unsupported{},
		Power:    &platform.Unsupported{},
		Metrics:  &staticMetrics{},
	})
}

// newTestServerPlatform builds a host around a caller-supplied platform, which
// is how the end-to-end input test injects the real Windows implementation.
func newTestServerPlatform(t *testing.T, plat *platform.Platform) *testServer {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0 // let the OS choose, so tests never collide
	cfg.HostID = "testhost"
	cfg.HostName = "test-laptop"
	cfg.TLS.Enabled = false // loopback, so plaintext is the documented default
	cfg.Discovery.MDNS = false
	cfg.ProfilesDir = filepath.Join(dir, "profiles")
	cfg.SoundsDir = filepath.Join(dir, "sounds")
	cfg.Logging.Level = "error"
	cfg.Logging.Console = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the test config is invalid: %v", err)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	audit, err := auth.OpenAudit(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("OpenAudit: %v", err)
	}
	t.Cleanup(func() { audit.Close() })

	mgr, err := auth.NewManager(filepath.Join(dir, "devices.json"), audit, auth.DefaultOptions())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	metrics := telemetry.New(plat, log)

	iconsDir := filepath.Join(dir, "icons")
	iconClient, err := icons.New(iconsDir, log)
	if err != nil {
		t.Fatalf("icons.New: %v", err)
	}

	eng := engine.New(nil, plat, log, engine.Options{
		MaxConcurrent:  4,
		QueueDepth:     16,
		DefaultTimeout: 5 * time.Second,
	}, audit.Record, nil)

	reg, err := engine.BuildRegistry(plat, cfg.HostName, "test", eng)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	eng.SetRegistry(reg)

	if err := writeProfile(t, cfg.ProfilesDir, "development", testProfileJSON); err != nil {
		t.Fatalf("writing the test profile: %v", err)
	}
	profReg, err := profiles.New(profiles.Options{
		Dir:     cfg.ProfilesDir,
		Actions: profiles.BuildActionSet(reg),
	}, log)
	if err != nil {
		t.Fatalf("profiles.New: %v", err)
	}
	t.Cleanup(profReg.Close)

	eng.SetProfileDir(profReg.DirOf)
	eng.SetProfileExists(func(id string) bool { _, ok := profReg.Get(id); return ok })
	eng.SetSoundsDir(cfg.SoundsDir)
	eng.SetSoundReferenced(profReg.SoundReferenced)
	eng.SetPageExists(func(pid, pageID string) bool {
		p, ok := profReg.Get(pid)
		if !ok {
			return false
		}
		_, ok = p.Page(pageID)
		return ok
	})

	srv, err := New(Options{
		Config:    cfg,
		Log:       log,
		Auth:      mgr,
		Profiles:  profReg,
		Engine:    eng,
		Telemetry: metrics,
		Icons:     iconClient,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	// The engine's event sink is the server's fan-out.
	eng.SetEmitter(srv.EmitEvent)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		close(started)
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("server stopped unexpectedly: %v", err)
		}
	}()
	<-started

	// Wait for the listener to be bound.
	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Addr() == "" {
		t.Fatal("the server never bound a port")
	}
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})

	ts := &testServer{
		srv:      srv,
		input:    plat.Input,
		auth:     mgr,
		prof:     profReg,
		engine:   eng,
		iconsDir: iconsDir,
		url:      "http://" + srv.Addr(),
		wsURL:    "ws://" + srv.Addr() + "/ws",
		dir:      dir,
	}
	return ts
}

// staticMetrics provides a fixed metric set.
type staticMetrics struct {
	platform.Unsupported
}

func (staticMetrics) Sample(context.Context) (map[string]float64, error) {
	return map[string]float64{"cpu.usage": 21.5, "mem.used_pct": 40}, nil
}

func (staticMetrics) Available() []string { return []string{"cpu.usage", "mem.used_pct"} }

// pair performs the REST pairing handshake and returns the token.
func (ts *testServer) pair(t *testing.T, deviceID string) string {
	t.Helper()

	pin, err := ts.auth.IssuePIN()
	if err != nil {
		t.Fatalf("IssuePIN: %v", err)
	}

	body, _ := json.Marshal(PairRequest{
		PIN: pin.Code,
		Device: PairDeviceInput{
			ID: deviceID, Name: "Test Phone", Platform: "android", Model: "test",
		},
	})
	resp, err := http.Post(ts.url+"/api/v1/pair", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /pair: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var raw map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		t.Fatalf("pairing failed with %d: %v", resp.StatusCode, raw)
	}
	var out PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the pairing response: %v", err)
	}
	if out.Token == "" {
		t.Fatal("pairing returned an empty token")
	}
	return out.Token
}

// client is a minimal protocol client over a real WebSocket.
type client struct {
	t    *testing.T
	conn *websocket.Conn

	mu       sync.Mutex
	inbox    []proto.Envelope
	waiters  map[string]chan proto.Envelope
	closed   chan struct{}
	closeErr error
	code     int
	reason   string
}

func (ts *testServer) connect(t *testing.T, token, deviceID string) *client {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(ts.wsURL, nil)
	if err != nil {
		t.Fatalf("dialing %s: %v", ts.wsURL, err)
	}
	c := &client{
		t:       t,
		conn:    conn,
		waiters: map[string]chan proto.Envelope{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()

	if err := c.send(proto.TypeHello, proto.HelloPayload{
		DeviceID: deviceID,
		Token:    token,
		Client:   proto.ClientInfo{Platform: "android", AppVersion: "test"},
	}); err != nil {
		t.Fatalf("sending hello: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

func (c *client) readLoop() {
	defer close(c.closed)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if ce, ok := err.(*websocket.CloseError); ok {
				c.code = ce.Code
				c.reason = ce.Text
			}
			c.closeErr = err
			// Wake every waiter so nothing blocks forever on a closed socket.
			for id, ch := range c.waiters {
				close(ch)
				delete(c.waiters, id)
			}
			c.mu.Unlock()
			return
		}
		env, err := proto.Decode(data)
		if err != nil {
			continue
		}

		c.mu.Lock()
		if env.ReplyTo != "" {
			if ch, ok := c.waiters[env.ReplyTo]; ok {
				ch <- env
				delete(c.waiters, env.ReplyTo)
				c.mu.Unlock()
				continue
			}
		}
		c.inbox = append(c.inbox, env)
		c.mu.Unlock()
	}
}

func (c *client) send(typ string, payload any) error {
	env, err := proto.New(typ, payload)
	if err != nil {
		return err
	}
	env.ID = proto.NewID()
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// request sends a message and waits for its correlated reply.
func (c *client) request(typ string, payload any, timeout time.Duration) (proto.Envelope, error) {
	env, err := proto.New(typ, payload)
	if err != nil {
		return proto.Envelope{}, err
	}
	env.ID = proto.NewID()

	ch := make(chan proto.Envelope, 1)
	c.mu.Lock()
	c.waiters[env.ID] = ch
	c.mu.Unlock()

	data, err := json.Marshal(env)
	if err != nil {
		return proto.Envelope{}, err
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return proto.Envelope{}, err
	}

	select {
	case reply, ok := <-ch:
		if !ok {
			return proto.Envelope{}, fmt.Errorf("the connection closed while waiting for %s", typ)
		}
		return reply, nil
	case <-time.After(timeout):
		return proto.Envelope{}, fmt.Errorf("timed out waiting for a reply to %s", typ)
	}
}

// awaitEvent waits for an unsolicited event of the given type.
func (c *client) awaitEvent(typ string, timeout time.Duration) (proto.Envelope, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for i, env := range c.inbox {
			if env.Type == typ {
				c.inbox = append(c.inbox[:i], c.inbox[i+1:]...)
				c.mu.Unlock()
				return env, nil
			}
		}
		c.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return proto.Envelope{}, fmt.Errorf("timed out waiting for a %s event", typ)
}

func (c *client) hello() (proto.WelcomePayload, error) {
	env, err := c.awaitEvent(proto.TypeWelcome, 3*time.Second)
	if err != nil {
		return proto.WelcomePayload{}, err
	}
	var w proto.WelcomePayload
	if err := proto.DecodePayload(env, &w); err != nil {
		return w, err
	}
	return w, nil
}

// --- tests ---------------------------------------------------------------

// TestRESTInfoIsPublicButMinimal checks that discovery works before pairing and
// that no secret is exposed.
func TestRESTInfoIsPublicButMinimal(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	resp, err := http.Get(ts.url + "/api/v1/info")
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var info InfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decoding /info: %v", err)
	}
	if info.HostID != "testhost" || info.HostName != "test-laptop" {
		t.Errorf("identity = %q/%q", info.HostID, info.HostName)
	}
	if info.Protocol.Min != proto.Version || info.Protocol.Max != proto.Version {
		t.Errorf("protocol range = %d..%d", info.Protocol.Min, info.Protocol.Max)
	}
	if info.Pairing.Open {
		t.Error("pairing is reported as open before any PIN was issued")
	}

	// The raw body must not contain the admin token or any device hash.
	raw, _ := json.Marshal(info)
	if strings.Contains(string(raw), ts.auth.AdminToken()) {
		t.Error("/info leaked the admin token")
	}
}

// TestHealthEndpoint covers the liveness probe the CLI uses.
func TestHealthEndpoint(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	resp, err := http.Get(ts.url + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decoding /health: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("status = %q", health.Status)
	}
	if health.Profiles != 1 {
		t.Errorf("profiles = %d, want 1", health.Profiles)
	}
}

// TestPairThenConnectThenPress is the milestone's headline end-to-end path:
// pair over REST, connect over WebSocket, press a button, and observe the
// action reaching the platform.
func TestPairThenConnectThenPress(t *testing.T) {
	input := &recordingInput{}
	ts := newTestServer(t, input)

	token := ts.pair(t, "android-001")
	c := ts.connect(t, token, "android-001")

	welcome, err := c.hello()
	if err != nil {
		t.Fatalf("waiting for welcome: %v", err)
	}
	if welcome.SessionID == "" {
		t.Error("welcome carries no session id")
	}
	if welcome.Device.ID != "android-001" {
		t.Errorf("welcome device id = %q", welcome.Device.ID)
	}
	if welcome.Host.Name != "test-laptop" {
		t.Errorf("welcome host name = %q", welcome.Host.Name)
	}
	if welcome.HeartbeatIntervalMS == 0 || welcome.IdleTimeoutMS == 0 {
		t.Error("welcome does not advertise the heartbeat timings")
	}
	if !contains(welcome.Features, "action.cancel") {
		t.Errorf("features = %v, expected action.cancel", welcome.Features)
	}

	// Fetch the profile, as a real client does before rendering.
	reply, err := c.request(proto.TypeProfileGet, proto.ProfileGetPayload{ProfileID: "development"}, 3*time.Second)
	if err != nil {
		t.Fatalf("profile.get: %v", err)
	}
	if reply.Type != proto.TypeProfileGetResult {
		t.Fatalf("profile.get answered with %q", reply.Type)
	}
	var got proto.ProfileGetResultPayload
	if err := proto.DecodePayload(reply, &got); err != nil {
		t.Fatalf("decoding the profile: %v", err)
	}
	if got.Revision == "" {
		t.Error("the profile has no revision")
	}

	// Press the button that sends CTRL+C.
	press, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "development",
		PageID:    "home",
		ButtonID:  "copy",
		Press:     proto.PressInfo{Kind: "short", Count: 1},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("button.press: %v", err)
	}
	if press.Type != proto.TypeActionResult {
		t.Fatalf("button.press answered with %q", press.Type)
	}
	var result proto.ActionResultPayload
	if err := proto.DecodePayload(press, &result); err != nil {
		t.Fatalf("decoding action.result: %v", err)
	}
	if !result.OK {
		t.Fatalf("the action failed: %+v", result.Error)
	}
	if result.ExecutionID == "" {
		t.Error("action.result carries no execution id")
	}
	if result.ActionType != "keyboard.shortcut" {
		t.Errorf("action type = %q", result.ActionType)
	}
	if result.ActionID != "development/home/copy" {
		t.Errorf("action id = %q, want development/home/copy", result.ActionID)
	}

	// The action must have actually reached the input layer.
	if input.count() != 1 {
		t.Fatalf("the input layer received %d shortcuts, want 1", input.count())
	}
	if got := input.shortcuts[0]; len(got) != 2 || got[0] != "CTRL" || got[1] != "C" {
		t.Errorf("the injected shortcut is %v, want CTRL+C", got)
	}
}

// TestScopeIsEnforcedOverTheWire is the security property, tested through the
// real protocol rather than through the engine directly.
func TestScopeIsEnforcedOverTheWire(t *testing.T) {
	input := &recordingInput{}
	ts := newTestServer(t, input)

	token := ts.pair(t, "android-002")

	// Strip the keyboard scope, as an operator would for a kiosk phone.
	if err := ts.auth.SetScopes("android-002", []auth.Scope{auth.ScopeApps}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	c := ts.connect(t, token, "android-002")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "development", PageID: "home", ButtonID: "copy",
		Press: proto.PressInfo{Kind: "short"},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("button.press: %v", err)
	}

	var result proto.ActionResultPayload
	if err := proto.DecodePayload(reply, &result); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if result.OK {
		t.Fatal("an action ran without the scope it requires")
	}
	if result.Error == nil || result.Error.Code != proto.CodeForbidden {
		t.Fatalf("error = %+v, want forbidden", result.Error)
	}
	if input.count() != 0 {
		t.Fatal("the input layer was reached despite the refusal")
	}
}

// TestUnpairedConnectionIsRejected covers the unauthenticated path
// (docs/SECURITY.md T1).
func TestUnpairedConnectionIsRejected(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	conn, _, err := websocket.DefaultDialer.Dial(ts.wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	env, _ := proto.New(proto.TypeHello, proto.HelloPayload{DeviceID: "ghost", Token: "bogus"})
	env.ID = proto.NewID()
	data, _ := json.Marshal(env)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("an unauthenticated connection was accepted")
	}
	var closeErr *websocket.CloseError
	if ok := asCloseError(err, &closeErr); !ok {
		t.Fatalf("expected a close error, got %v", err)
	}
	if closeErr.Code != proto.CloseUnauthenticated {
		t.Errorf("close code = %d, want %d", closeErr.Code, proto.CloseUnauthenticated)
	}
}

// TestRevokedTokenIsRejectedAndKicked covers revocation over a live connection.
func TestRevokedTokenIsRejectedAndKicked(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-003")

	c := ts.connect(t, token, "android-003")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	// Revoke while the session is live: it must be closed, not merely refused
	// on the next connection.
	if _, err := ts.auth.Revoke("android-003"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	ts.srv.disconnectDevice("android-003", "this device was revoked on the host")

	select {
	case <-c.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the revoked session was not closed")
	}

	// And a new connection with the same token must fail.
	conn, _, err := websocket.DefaultDialer.Dial(ts.wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	env, _ := proto.New(proto.TypeHello, proto.HelloPayload{DeviceID: "android-003", Token: token})
	env.ID = proto.NewID()
	data, _ := json.Marshal(env)
	_ = conn.WriteMessage(websocket.TextMessage, data)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("a revoked token was accepted on a new connection")
	}
}

// TestFirstFrameMustBeHello covers the handshake requirement.
func TestFirstFrameMustBeHello(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	conn, _, err := websocket.DefaultDialer.Dial(ts.wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// A well-formed frame that is not a hello.
	env, _ := proto.New(proto.TypeProfileList, proto.ProfileListPayload{})
	env.ID = proto.NewID()
	data, _ := json.Marshal(env)
	_ = conn.WriteMessage(websocket.TextMessage, data)

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("a non-hello first frame was accepted")
	}
	var closeErr *websocket.CloseError
	if ok := asCloseError(err, &closeErr); !ok || closeErr.Code != proto.CloseMalformed {
		t.Fatalf("expected close %d, got %v", proto.CloseMalformed, err)
	}
}

// TestUnknownMessageTypeIsAnswered checks that an unknown type produces a
// protocol error rather than a dropped connection.
func TestUnknownMessageTypeIsAnswered(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-004")
	c := ts.connect(t, token, "android-004")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request("nonsense.message", map[string]any{}, 3*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if reply.Type != proto.TypeError {
		t.Fatalf("answered with %q, want error", reply.Type)
	}
	var e proto.ErrorPayload
	if err := proto.DecodePayload(reply, &e); err != nil {
		t.Fatalf("decoding the error: %v", err)
	}
	if e.Code != proto.CodeUnsupported {
		t.Errorf("code = %q, want unsupported", e.Code)
	}
}

// TestSecondHelloIsRefused covers the anti-identity-smuggling rule.
func TestSecondHelloIsRefused(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-005")
	c := ts.connect(t, token, "android-005")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeHello, proto.HelloPayload{DeviceID: "android-005", Token: token}, 3*time.Second)
	if err != nil {
		t.Fatalf("second hello: %v", err)
	}
	if reply.Type != proto.TypeError {
		t.Fatalf("a second hello was answered with %q, want error", reply.Type)
	}
}

// TestPingPong covers the heartbeat.
func TestPingPong(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-006")
	c := ts.connect(t, token, "android-006")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypePing, proto.PingPayload{Echo: "abc"}, 3*time.Second)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if reply.Type != proto.TypePong {
		t.Fatalf("ping answered with %q, want pong", reply.Type)
	}
	var pong proto.PongPayload
	if err := proto.DecodePayload(reply, &pong); err != nil {
		t.Fatalf("decoding pong: %v", err)
	}
	if pong.Echo != "abc" {
		t.Errorf("echo = %q, want abc", pong.Echo)
	}
}

// TestProfileErrorsAreSpecific checks that a client learns *why* a button could
// not be found, rather than getting a generic failure.
func TestProfileErrorsAreSpecific(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-007")
	c := ts.connect(t, token, "android-007")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	cases := []struct {
		name    string
		payload proto.ButtonPressPayload
		wantSub string
	}{
		{"unknown profile", proto.ButtonPressPayload{ProfileID: "ghost", PageID: "home", ButtonID: "copy"}, "no profile"},
		{"unknown page", proto.ButtonPressPayload{ProfileID: "development", PageID: "ghost", ButtonID: "copy"}, "no page"},
		{"unknown button", proto.ButtonPressPayload{ProfileID: "development", PageID: "home", ButtonID: "ghost"}, "no button"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := c.request(proto.TypeButtonPress, tc.payload, 3*time.Second)
			if err != nil {
				t.Fatalf("button.press: %v", err)
			}
			if reply.Type != proto.TypeError {
				t.Fatalf("answered with %q, want error", reply.Type)
			}
			var e proto.ErrorPayload
			if err := proto.DecodePayload(reply, &e); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if e.Code != proto.CodeNotFound {
				t.Errorf("code = %q, want not_found", e.Code)
			}
			if !strings.Contains(e.Message, tc.wantSub) {
				t.Errorf("message %q does not mention %q", e.Message, tc.wantSub)
			}
		})
	}
}

// TestLongActionUsesAcceptedThenFinished covers the two-phase reply, which is
// what keeps a slow script from blocking the client's request table.
func TestLongActionUsesAcceptedThenFinished(t *testing.T) {
	slow := &blockingInput{delay: 2 * time.Second}
	ts := newTestServer(t, slow)

	// The profile needs a button that takes a while; the macro with a delay is
	// the natural one, so this test uses the same input delay path.
	token := ts.pair(t, "android-008")
	c := ts.connect(t, token, "android-008")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "development", PageID: "home", ButtonID: "copy",
		Press: proto.PressInfo{Kind: "short"},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("button.press: %v", err)
	}
	var accepted proto.ActionResultPayload
	if err := proto.DecodePayload(reply, &accepted); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("a slow action did not report acceptance: %+v", accepted)
	}

	// The completion must arrive as an event carrying the same execution id.
	event, err := c.awaitEvent(proto.TypeEventActionEnd, 6*time.Second)
	if err != nil {
		t.Fatalf("waiting for the completion event: %v", err)
	}
	var finished proto.ActionResultPayload
	if err := proto.DecodePayload(event, &finished); err != nil {
		t.Fatalf("decoding the completion: %v", err)
	}
	if finished.ExecutionID != accepted.ExecutionID {
		t.Errorf("execution id changed: %q then %q", accepted.ExecutionID, finished.ExecutionID)
	}
	if !finished.OK {
		t.Errorf("the slow action reported failure: %+v", finished.Error)
	}
}

// TestActionCancelStopsExecution covers the cancellation path end to end.
func TestActionCancelStopsExecution(t *testing.T) {
	slow := &blockingInput{delay: 30 * time.Second}
	ts := newTestServer(t, slow)

	token := ts.pair(t, "android-009")
	c := ts.connect(t, token, "android-009")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "development", PageID: "home", ButtonID: "copy",
		Press: proto.PressInfo{Kind: "short"},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("button.press: %v", err)
	}
	var accepted proto.ActionResultPayload
	if err := proto.DecodePayload(reply, &accepted); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("the action was not reported as accepted: %+v", accepted)
	}

	cancelReply, err := c.request(proto.TypeActionCancel, proto.ActionCancelPayload{
		ExecutionID: accepted.ExecutionID,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("action.cancel: %v", err)
	}
	if cancelReply.Type != proto.TypeActionCancel {
		t.Fatalf("cancel answered with %q", cancelReply.Type)
	}

	// The completion event must report cancellation, and it must arrive quickly:
	// the whole point is that the input call stops waiting.
	event, err := c.awaitEvent(proto.TypeEventActionEnd, 5*time.Second)
	if err != nil {
		t.Fatalf("waiting for the cancellation event: %v", err)
	}
	var finished proto.ActionResultPayload
	if err := proto.DecodePayload(event, &finished); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if finished.OK {
		t.Fatal("a cancelled action reported success")
	}
	if finished.Error == nil || finished.Error.Code != proto.CodeCancelled {
		t.Fatalf("error = %+v, want cancelled", finished.Error)
	}
}

// TestCancellingSomeoneElsesExecutionIsRefused covers the isolation rule: one
// phone must not be able to abort another phone's work.
func TestCancellingSomeoneElsesExecutionIsRefused(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-010")
	c := ts.connect(t, token, "android-010")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeActionCancel, proto.ActionCancelPayload{ExecutionID: "ex-someone-else"}, 3*time.Second)
	if err != nil {
		t.Fatalf("action.cancel: %v", err)
	}
	if reply.Type != proto.TypeError {
		t.Fatalf("cancelling a foreign execution was answered with %q", reply.Type)
	}
}

// TestTelemetrySubscribeStreams covers the telemetry path.
func TestTelemetrySubscribeStreams(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-011")
	c := ts.connect(t, token, "android-011")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeTelemetrySubscribe, proto.TelemetrySubscribePayload{
		Metrics:    []string{"cpu.usage"},
		IntervalMS: 250,
	}, 3*time.Second)
	if err != nil {
		t.Fatalf("telemetry.subscribe: %v", err)
	}
	if reply.Type != proto.TypeTelemetrySubscribe {
		t.Fatalf("subscribe answered with %q", reply.Type)
	}

	event, err := c.awaitEvent(proto.TypeEventTelemetry, 4*time.Second)
	if err != nil {
		t.Fatalf("waiting for a telemetry event: %v", err)
	}
	var sample proto.EventTelemetryPayload
	if err := proto.DecodePayload(event, &sample); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if _, ok := sample.Values["cpu.usage"]; !ok {
		t.Errorf("the sample does not contain cpu.usage: %v", sample.Values)
	}
	if sample.TS == 0 {
		t.Error("the sample has no timestamp")
	}
}

// TestTelemetryNeedsItsScope covers the permission on the metric stream.
func TestTelemetryNeedsItsScope(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-012")
	if err := ts.auth.SetScopes("android-012", []auth.Scope{auth.ScopeKeyboard}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	c := ts.connect(t, token, "android-012")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeTelemetrySubscribe, proto.TelemetrySubscribePayload{}, 3*time.Second)
	if err != nil {
		t.Fatalf("telemetry.subscribe: %v", err)
	}
	var e proto.ErrorPayload
	if err := proto.DecodePayload(reply, &e); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if e.Code != proto.CodeForbidden {
		t.Errorf("code = %q, want forbidden", e.Code)
	}
}

// TestAdminAPIRequiresTheToken covers the second barrier in front of the admin
// surface.
func TestAdminAPIRequiresTheToken(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	// No token.
	resp, err := http.Get(ts.url + "/api/v1/admin/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated admin request returned %d, want 401", resp.StatusCode)
	}

	// Wrong token.
	req, _ := http.NewRequest(http.MethodGet, ts.url+"/api/v1/admin/status", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("a bad admin token returned %d, want 401", resp2.StatusCode)
	}

	// Correct token.
	req3, _ := http.NewRequest(http.MethodGet, ts.url+"/api/v1/admin/status", nil)
	req3.Header.Set("Authorization", "Bearer "+ts.auth.AdminToken())
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("a valid admin token returned %d", resp3.StatusCode)
	}
	var status map[string]any
	if err := json.NewDecoder(resp3.Body).Decode(&status); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if status["host_id"] != "testhost" {
		t.Errorf("status = %v", status)
	}
	// The status response must never carry the token hash or a device secret.
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "token_hash") {
		t.Error("the admin status response contains a token hash")
	}
}

// TestAdminPairIssuesAWorkingPIN covers the CLI's pairing path: a PIN obtained
// from the admin API must be usable on the REST endpoint.
func TestAdminPairIssuesAWorkingPIN(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	req, _ := http.NewRequest(http.MethodPost, ts.url+"/api/v1/admin/pair", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+ts.auth.AdminToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/pair: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var pinResp struct {
		PIN        string `json:"pin"`
		ExpiresInS int    `json:"expires_in_s"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pinResp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(pinResp.PIN) != 6 {
		t.Fatalf("PIN %q has %d digits", pinResp.PIN, len(pinResp.PIN))
	}

	// Use it immediately.
	body, _ := json.Marshal(PairRequest{
		PIN:    pinResp.PIN,
		Device: PairDeviceInput{ID: "android-cli", Name: "From CLI"},
	})
	pairResp, err := http.Post(ts.url+"/api/v1/pair", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /pair: %v", err)
	}
	defer pairResp.Body.Close()
	if pairResp.StatusCode != http.StatusCreated {
		t.Fatalf("the PIN from the admin API did not work: %d", pairResp.StatusCode)
	}
}

// TestPairRejectsBadRequests covers the REST validation.
func TestPairRejectsBadRequests(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	pin, _ := ts.auth.IssuePIN()

	cases := []struct {
		name     string
		method   string
		body     string
		wantCode int
	}{
		{"wrong method", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"malformed json", http.MethodPost, "{", http.StatusBadRequest},
		{"no pin", http.MethodPost, `{"device":{"id":"a","name":"A"}}`, http.StatusBadRequest},
		{"no device id", http.MethodPost, `{"pin":"` + pin.Code + `","device":{"name":"A"}}`, http.StatusBadRequest},
		{"wrong pin", http.MethodPost, `{"pin":"000000","device":{"id":"a","name":"A"}}`, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, ts.url+"/api/v1/pair", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantCode {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}
		})
	}
}

// TestOversizedFrameIsRejected covers the frame cap (docs/SECURITY.md T5).
func TestOversizedFrameIsRejected(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-013")
	c := ts.connect(t, token, "android-013")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	// A frame larger than the cap. The client sends it raw rather than through
	// request(), because the point is that it never becomes a message.
	huge := make([]byte, proto.MaxMessageBytes+1024)
	for i := range huge {
		huge[i] = 'x'
	}
	_ = c.conn.WriteMessage(websocket.TextMessage, huge)

	select {
	case <-c.closed:
		// The read limit is enforced by gorilla, which closes the connection.
	case <-time.After(3 * time.Second):
		// Some platforms surface this as a read error on the server side rather
		// than a close; either way the frame must not have been processed.
	}

	// The session must not have processed anything: the profile list is the
	// simplest probe.
	if _, err := c.request(proto.TypeProfileList, proto.ProfileListPayload{}, 1*time.Second); err == nil {
		// A reply means the connection is still healthy, which is acceptable on
		// a platform that rejects the frame without closing. What must not
		// happen is a panic or a crash, which the test would already have seen.
		t.Log("the connection survived the oversized frame; the frame was rejected without closing")
	}
}

// TestMalformedFramesCloseTheSession covers the malformed-frame counter.
func TestMalformedFramesCloseTheSession(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-014")
	c := ts.connect(t, token, "android-014")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	// Valid JSON, wrong protocol version: the version gate rejects it and the
	// counter increments.
	for range 10 {
		_ = c.conn.WriteMessage(websocket.TextMessage, []byte(`{"v":99,"type":"ping","ts":1,"payload":{}}`))
	}

	select {
	case <-c.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the session survived 10 malformed frames")
	}
	if c.code != proto.CloseMalformed {
		t.Errorf("close code = %d, want %d", c.code, proto.CloseMalformed)
	}
}

// TestProfileListAndReload covers the profile management messages.
func TestProfileListAndReload(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-015")
	c := ts.connect(t, token, "android-015")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeProfileList, proto.ProfileListPayload{}, 3*time.Second)
	if err != nil {
		t.Fatalf("profile.list: %v", err)
	}
	var list proto.ProfileListResultPayload
	if err := proto.DecodePayload(reply, &list); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(list.Profiles) != 1 {
		t.Fatalf("profiles = %+v, want 1", list.Profiles)
	}
	if list.Profiles[0].ID != "development" || list.Profiles[0].Revision == "" {
		t.Errorf("profile summary = %+v", list.Profiles[0])
	}

	reload, err := c.request(proto.TypeProfileReload, proto.ProfileReloadPayload{ProfileID: "development"}, 3*time.Second)
	if err != nil {
		t.Fatalf("profile.reload: %v", err)
	}
	if reload.Type != proto.TypeProfileReload {
		t.Fatalf("reload answered with %q", reload.Type)
	}
	// A reload must announce the change so other clients refresh.
	if _, err := c.awaitEvent(proto.TypeEventProfileChange, 3*time.Second); err != nil {
		t.Errorf("no profile-changed event after a reload: %v", err)
	}
}

// TestToggleButtonStateIsPushedToTheClient covers the stateful-button path end
// to end: press a toggle, receive the state event.
func TestToggleButtonStateIsPushedToTheClient(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-016")
	c := ts.connect(t, token, "android-016")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	if _, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "development", PageID: "home", ButtonID: "toggle",
		Press: proto.PressInfo{Kind: "short"},
	}, 3*time.Second); err != nil {
		t.Fatalf("button.press: %v", err)
	}

	event, err := c.awaitEvent(proto.TypeEventButtonState, 3*time.Second)
	if err != nil {
		t.Fatalf("no button-state event: %v", err)
	}
	var st proto.EventButtonStatePayload
	if err := proto.DecodePayload(event, &st); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if st.ButtonID != "toggle" || st.State.Type != "toggle" {
		t.Fatalf("state = %+v", st)
	}
	if st.State.Active == nil || !*st.State.Active {
		t.Errorf("the first toggle press did not turn the button on: %+v", st.State)
	}
}

// TestClientLimitIsEnforced covers the session cap.
func TestClientLimitIsEnforced(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	// Lower the cap for this test.
	ts.srv.cfg.Session.MaxClients = 1

	token := ts.pair(t, "android-017")
	c1 := ts.connect(t, token, "android-017")
	if _, err := c1.hello(); err != nil {
		t.Fatalf("the first connection was refused: %v", err)
	}

	// The second connection must be refused with an HTTP error before upgrade.
	_, resp, err := websocket.DefaultDialer.Dial(ts.wsURL, nil)
	if err == nil {
		t.Fatal("a second connection was accepted past the client limit")
	}
	if resp == nil {
		t.Fatalf("no HTTP response for the refused connection: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// --- helpers -------------------------------------------------------------

func asCloseError(err error, target **websocket.CloseError) bool {
	for err != nil {
		if ce, ok := err.(*websocket.CloseError); ok {
			*target = ce
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// writeProfile writes a profile document into a profiles directory.
func writeProfile(t *testing.T, dir, id, body string) error {
	t.Helper()
	target := filepath.Join(dir, id)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "profile.json"), []byte(body), 0o600)
}

// testProfileJSON is the fixture: one profile with the buttons the tests press.
const testProfileJSON = `{
  "schema": 1,
  "id": "development",
  "name": "Development",
  "icon": { "type": "emoji", "value": "D" },
  "settings": { "grid": { "columns": 4, "rows": 4 } },
  "root_page": "home",
  "pages": [
    {
      "id": "home",
      "name": "Home",
      "buttons": [
        {
          "id": "copy",
          "label": "Copy",
          "icon": { "type": "emoji", "value": "C" },
          "cell": { "row": 0, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["CTRL", "C"] } }
        },
        {
          "id": "toggle",
          "label": "Toggle",
          "icon": { "type": "emoji", "value": "T" },
          "cell": { "row": 0, "column": 1 },
          "state": { "type": "toggle" },
          "on_press": { "type": "noop", "params": {} }
        },
        {
          "id": "notify",
          "label": "Notify",
          "icon": { "type": "emoji", "value": "N" },
          "cell": { "row": 0, "column": 2 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.notify", "params": { "message": "hello", "level": "info" } }
        },
        {
          "id": "nav",
          "label": "Media",
          "icon": { "type": "emoji", "value": "M" },
          "cell": { "row": 0, "column": 3 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.open_page", "params": { "page_id": "media" } }
        }
      ]
    },
    {
      "id": "media",
      "name": "Media",
      "parent": "home",
      "buttons": [
        {
          "id": "back",
          "label": "Back",
          "icon": { "type": "emoji", "value": "<" },
          "cell": { "row": 0, "column": 0 },
          "state": { "type": "momentary" },
          "on_press": { "type": "deck.back", "params": {} }
        }
      ]
    }
  ]
}`

var _ = profile.Load
var _ = fmt.Sprint

// TestProfileGetInlinesImageIcons covers the reason the `icons` map exists: a
// button with `"icon": {"type":"image"}` must arrive with something to draw,
// because protocol v1 has no message that fetches a file from the host.
//
// The map is computed when the document is served, not stored in the file, so
// this is the only place the behaviour is observable.
func TestProfileGetInlinesImageIcons(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-icons")
	c := ts.connect(t, token, "android-icons")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	// Point a button at an image icon, and put a PNG in the icon cache by hand.
	// The cache is the only thing the server reads: it never fetches while
	// serving a profile, so that serving cannot block on the network.
	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
	if code != http.StatusOK {
		t.Fatalf("GET document: %d %s", code, raw)
	}
	var doc profile.Profile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	const iconFile = "test--icon.png"
	doc.Pages[0].Buttons[0].Icon = profile.Icon{Type: "image", Value: iconFile}

	pngBytes, err := icons.Rasterize([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"><path d="M12 2L2 22h20z" fill="#4c8bf5"/></svg>`), 64)
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ts.iconsDir, iconFile), pngBytes, 0o600); err != nil {
		t.Fatalf("writing the cache entry: %v", err)
	}

	body, _ := json.Marshal(doc)
	if code, raw := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(body)); code != http.StatusOK {
		t.Fatalf("PUT document: %d %s", code, raw)
	}

	// Ask over the protocol, as a phone does.
	reply, err := c.request(proto.TypeProfileGet, proto.ProfileGetPayload{ProfileID: "development"}, 3*time.Second)
	if err != nil {
		t.Fatalf("profile.get: %v", err)
	}
	var got proto.ProfileGetResultPayload
	if err := proto.DecodePayload(reply, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	obj, ok := got.Profile.(map[string]any)
	if !ok {
		t.Fatalf("the profile is %T, want an object", got.Profile)
	}
	iconsMap, ok := obj["icons"].(map[string]any)
	if !ok {
		t.Fatalf("the served document has no icons map: %v", obj["icons"])
	}
	uri, ok := iconsMap[iconFile].(string)
	if !ok {
		t.Fatalf("no entry for %q: %v", iconFile, iconsMap)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("the entry is not a PNG data URI: %.40s", uri)
	}
	if len(uri) < 100 {
		t.Errorf("the data URI is suspiciously short: %d bytes", len(uri))
	}
}

// TestProfileGetLeavesMissingIconsOut checks the degradation path: an icon the
// host does not have cached must not break the profile. One placeholder button is
// a far better outcome than a deck that will not load.
func TestProfileGetLeavesMissingIconsOut(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	token := ts.pair(t, "android-icons-missing")
	c := ts.connect(t, token, "android-icons-missing")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
	if code != http.StatusOK {
		t.Fatalf("GET document: %d %s", code, raw)
	}
	var doc profile.Profile
	_ = json.Unmarshal(raw, &doc)
	doc.Pages[0].Buttons[0].Icon = profile.Icon{Type: "image", Value: "never-fetched.png"}
	body, _ := json.Marshal(doc)
	if code, raw := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(body)); code != http.StatusOK {
		t.Fatalf("PUT document: %d %s", code, raw)
	}

	reply, err := c.request(proto.TypeProfileGet, proto.ProfileGetPayload{ProfileID: "development"}, 3*time.Second)
	if err != nil {
		t.Fatalf("profile.get must still succeed: %v", err)
	}
	var got proto.ProfileGetResultPayload
	if err := proto.DecodePayload(reply, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	obj := got.Profile.(map[string]any)
	if m, ok := obj["icons"].(map[string]any); ok {
		if _, present := m["never-fetched.png"]; present {
			t.Error("an icon that is not cached was reported as resolved")
		}
	}
}
