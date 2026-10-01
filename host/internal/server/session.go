package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
	"github.com/mobiledeck/mobiledeck/host/internal/telemetry"
)

// Session is one connected client.
//
// Each session owns two goroutines: a reader that dispatches requests and a
// writer that drains a buffered channel. Splitting them is what keeps a slow
// client from blocking the host: the reader never writes to the socket, so a
// stalled TCP window cannot stop the host from reading the next request or from
// serving other sessions.
type Session struct {
	id     string // session id, not the device id: one device may hold two sockets
	device auth.Device
	scopes auth.ScopeSet
	remote string

	srv *Server
	log *slog.Logger

	conn *websocket.Conn

	send   chan []byte
	closed chan struct{}
	once   sync.Once

	mu            sync.Mutex
	activeProfile string
	activePage    string
	telemetry     *telemetry.Subscription
	// malformed counts frames that failed to parse. A client that sends ten of
	// them is broken or hostile, and either way the session ends.
	malformed int
	// pending tracks executions this session started, so a disconnect can
	// cancel them rather than leaving an orphaned script running.
	pending map[string]bool
	// cancels maps an execution id to the function that stops it. The session
	// owns these contexts so an action that outlives the synchronous reply
	// window keeps running.
	cancels map[string]context.CancelFunc
}

// upgrader is configured with an explicit origin check.
//
// The default gorilla behaviour accepts any origin, which would let a web page
// in the user's browser open a socket to the host. The host is not a browser
// service, so no origin is acceptable: native clients do not send one, and a
// browser is exactly the caller we do not want.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
	Error: func(w http.ResponseWriter, r *http.Request, status int, reason error) {
		http.Error(w, `{"error":"upgrade_failed","message":"`+reason.Error()+`"}`, status)
	},
}

// handleWS upgrades a connection and runs a session.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		writeError(w, http.StatusBadRequest, "expected_websocket", "this endpoint requires a WebSocket upgrade")
		return
	}

	// Refuse plaintext sessions from off-host when TLS is enabled: the client
	// would otherwise silently downgrade and send its token in the clear.
	if s.cfg.TLS.Enabled && r.TLS == nil && !isLoopback(r) {
		writeError(w, http.StatusForbidden, "tls_required",
			"this host requires TLS; connect with wss:// and pin the certificate fingerprint")
		return
	}

	if s.SessionCount() >= s.cfg.Session.MaxClients {
		s.log.Warn("refusing connection: client limit reached", "limit", s.cfg.Session.MaxClients, "ip", remoteIP(r))
		writeError(w, http.StatusServiceUnavailable, "too_many_clients",
			fmt.Sprintf("the host already has %d connected devices", s.cfg.Session.MaxClients))
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		s.log.Debug("websocket upgrade failed", "ip", remoteIP(r), "error", err)
		return
	}

	sess := &Session{
		id:      "s-" + proto.NewID(),
		remote:  remoteIP(r),
		srv:     s,
		log:     s.log,
		conn:    conn,
		send:    make(chan []byte, 64),
		closed:  make(chan struct{}),
		pending: make(map[string]bool),
		cancels: make(map[string]context.CancelFunc),
	}
	sess.run(r.Context())
}

// run performs the handshake and then pumps frames until the session ends.
func (sess *Session) run(ctx context.Context) {
	s := sess.srv

	// The first frame must be a hello, within the handshake deadline. Without
	// this bound, an idle TCP connection would hold a session slot forever.
	_ = sess.conn.SetReadDeadline(time.Now().Add(s.cfg.HelloTimeout()))

	env, err := sess.readEnvelope()
	if err != nil {
		sess.log.Debug("session closed before handshake", "remote", sess.remote, "error", err)
		sess.close(proto.CloseMalformed, "expected a hello frame")
		return
	}
	if env.Type != proto.TypeHello {
		sess.log.Warn("first frame was not a hello", "type", env.Type, "remote", sess.remote)
		sess.close(proto.CloseMalformed, "the first frame must be hello")
		return
	}

	var hello proto.HelloPayload
	if err := proto.DecodePayload(env, &hello); err != nil {
		sess.close(proto.CloseMalformed, "hello payload is malformed")
		return
	}

	device, err := s.auth.Authenticate(hello.Token, hello.DeviceID, sess.remote)
	if err != nil {
		code := proto.CloseUnauthenticated
		reason := "authentication failed"
		switch {
		case errors.Is(err, auth.ErrDeviceDisabled):
			code = proto.CloseDeviceDisabled
			reason = "this device is disabled on the host"
		case errors.Is(err, auth.ErrRateLimited):
			code = proto.CloseRateLimited
			reason = "too many attempts from this address"
		}
		sess.log.Warn("session rejected", "device", hello.DeviceID, "remote", sess.remote, "reason", reason)
		sess.close(code, reason)
		return
	}

	sess.device = device
	sess.scopes = device.ScopeSet()
	sess.activeProfile = hello.Resume.ProfileID
	if sess.activeProfile == "" {
		if def := s.profiles.Default(); def != nil {
			sess.activeProfile = def.Doc.ID
		}
	}
	sess.activePage = hello.Resume.PageID

	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()

	sess.log.Info("device connected",
		"device", device.ID, "name", device.Name, "remote", sess.remote,
		"platform", device.Platform, "session", sess.id)

	go sess.writePump()

	if err := sess.sendWelcome(); err != nil {
		sess.close(proto.CloseTryAgainLater, "could not send the welcome frame")
		return
	}

	sess.readLoop()
}

// readLoop reads and dispatches frames until the connection ends.
func (sess *Session) readLoop() {
	s := sess.srv
	defer sess.teardown()

	// A client that stops sending ping frames is gone even if the TCP socket
	// still looks open, which is the normal case when a phone leaves Wi-Fi.
	idle := s.cfg.IdleTimeout()

	for {
		_ = sess.conn.SetReadDeadline(time.Now().Add(idle))
		env, err := sess.readEnvelope()
		if errors.Is(err, errMalformedFrame) {
			// One bad frame is answered with a protocol error and the session
			// continues; the counter inside readEnvelope bounds the abuse.
			sess.log.Debug("skipping a malformed frame", "device", sess.device.ID)
			sess.push(proto.TypeError, proto.Errorf(proto.CodeInvalidArgument, "that frame could not be decoded"))
			continue
		}
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				sess.log.Info("device disconnected", "device", sess.device.ID, "code", closeErr.Code, "reason", closeErr.Text)
			} else if isTimeout(err) {
				sess.log.Info("device timed out", "device", sess.device.ID, "idle_ms", idle.Milliseconds())
			} else {
				sess.log.Debug("read ended", "device", sess.device.ID, "error", err)
			}
			return
		}

		if err := s.auth.RateAllow(sess.device.ID); err != nil {
			sess.log.Warn("device exceeded its request rate", "device", sess.device.ID)
			sess.reply(env, proto.TypeError, proto.Errorf(proto.CodeRateLimited, "too many requests; slow down"))
			sess.close(proto.CloseRateLimited, "rate limit exceeded")
			return
		}

		if !sess.dispatch(env) {
			return
		}
	}
}

// readEnvelope reads one frame and decodes it, enforcing the size cap.
//
// A frame that fails to decode is reported as errMalformedFrame, which the
// caller treats as "skip and keep reading". Distinguishing that from a transport
// failure is what makes the malformed-frame policy meaningful: a decode error
// used to end the loop on the first occurrence, so the counter could never reach
// its threshold.
func (sess *Session) readEnvelope() (proto.Envelope, error) {
	sess.conn.SetReadLimit(proto.MaxMessageBytes)
	_, data, err := sess.conn.ReadMessage()
	if err != nil {
		return proto.Envelope{}, err
	}
	env, err := proto.Decode(data)
	if err != nil {
		sess.mu.Lock()
		sess.malformed++
		count := sess.malformed
		sess.mu.Unlock()

		if count >= maxMalformedFrames {
			sess.log.Warn("closing session: too many malformed frames", "device", sess.device.ID, "count", count)
			// Close here rather than letting the caller do it: close() is
			// once-guarded, and the teardown path would otherwise win the race
			// with a normal-closure code, hiding the real reason from the client.
			sess.close(proto.CloseMalformed, "too many malformed frames")
			return proto.Envelope{}, errSessionClosing
		}
		return proto.Envelope{}, errMalformedFrame
	}
	return env, nil
}

// maxMalformedFrames is how many undecodable frames a session tolerates before
// it is closed. A client that produces this many is broken or hostile.
const maxMalformedFrames = 10

// dispatch routes one message. It returns false when the session must end.
func (sess *Session) dispatch(env proto.Envelope) bool {
	switch env.Type {
	case proto.TypePing:
		sess.reply(env, proto.TypePong, proto.PongPayload{Echo: payloadEcho(env)})
		return true

	case proto.TypePong:
		return true

	case proto.TypeHello:
		// A second hello is a protocol error: the session is already bound to a
		// device and re-binding it would be a way to smuggle a second identity.
		sess.reply(env, proto.TypeError, proto.Errorf(proto.CodeInvalidArgument, "hello was already sent on this connection"))
		return true

	case proto.TypeProfileList:
		sess.handleProfileList(env)

	case proto.TypeProfileGet:
		sess.handleProfileGet(env)

	case proto.TypeProfileSetActive:
		sess.handleSetActiveProfile(env)

	case proto.TypeProfileReload:
		sess.handleProfileReload(env)

	case proto.TypeProfileExport:
		sess.handleProfileExport(env)

	case proto.TypeProfileImport:
		sess.handleProfileImport(env)

	case proto.TypeButtonPress:
		sess.handleButtonPress(env)

	case proto.TypeButtonRelease:
		sess.handleButtonRelease(env)

	case proto.TypeActionCancel:
		sess.handleActionCancel(env)

	case proto.TypeTelemetrySubscribe:
		sess.handleTelemetrySubscribe(env)

	default:
		sess.reply(env, proto.TypeError, proto.Errorf(proto.CodeUnsupported, "message type %q is not supported by this host", env.Type))
	}
	return true
}

// writePump drains the send channel. It is the only goroutine that writes.
func (sess *Session) writePump() {
	s := sess.srv
	heartbeat := s.cfg.Heartbeat()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-sess.closed:
			return
		case msg := <-sess.send:
			_ = sess.conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout()))
			if err := sess.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				sess.log.Debug("write failed", "device", sess.device.ID, "error", err)
				sess.close(proto.CloseTryAgainLater, "write failed")
				return
			}
		case <-ticker.C:
			// The host pings too, so a client that only reads (a wall-mounted
			// tablet, say) still proves it is alive.
			_ = sess.conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout()))
			if err := sess.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				sess.close(proto.CloseTryAgainLater, "heartbeat failed")
				return
			}
		}
	}
}

// sendEnvelope queues an envelope for delivery.
//
// A full buffer means the client is not draining. The session is closed rather
// than blocking, because the alternative is the host's event fan-out stalling
// on one unresponsive phone (docs/ARCHITECTURE.md §3.3).
func (sess *Session) sendEnvelope(env proto.Envelope) {
	data, err := json.Marshal(env)
	if err != nil {
		sess.log.Error("could not encode an outbound frame", "type", env.Type, "error", err)
		return
	}
	select {
	case <-sess.closed:
		return
	default:
	}
	select {
	case sess.send <- data:
	default:
		sess.log.Warn("closing session: outbound buffer is full", "device", sess.device.ID, "type", env.Type)
		sess.close(proto.CloseTryAgainLater, "the client is not reading its socket fast enough")
	}
}

// reply sends a message correlated to a request.
func (sess *Session) reply(req proto.Envelope, typ string, payload any) {
	env, err := proto.Reply(req, typ, payload)
	if err != nil {
		sess.log.Error("could not build a reply", "type", typ, "error", err)
		return
	}
	sess.sendEnvelope(env)
}

// push sends an unsolicited event.
func (sess *Session) push(typ string, payload any) {
	env, err := proto.New(typ, payload)
	if err != nil {
		sess.log.Error("could not build an event", "type", typ, "error", err)
		return
	}
	sess.sendEnvelope(env)
}

// fail sends an error reply carrying a protocol error code.
func (sess *Session) fail(req proto.Envelope, code, format string, args ...any) {
	sess.reply(req, proto.TypeError, proto.Errorf(code, format, args...))
}

// sendWelcome completes the handshake.
func (sess *Session) sendWelcome() error {
	s := sess.srv
	payload := proto.WelcomePayload{
		SessionID: sess.id,
		Host: proto.HostInfo{
			ID:      s.cfg.HostID,
			Name:    s.cfg.HostName,
			OS:      s.engine.Platform().OSName,
			Version: Version,
		},
		Device: proto.DeviceInfo{
			ID:     sess.device.ID,
			Name:   sess.device.Name,
			Scopes: scopeStrings(sess.device.Scopes),
		},
		ServerTime:          time.Now().UnixMilli(),
		HeartbeatIntervalMS: s.cfg.Session.HeartbeatMS,
		IdleTimeoutMS:       s.cfg.Session.IdleTimeoutMS,
		Features:            s.features(),
		ProfilesRevision:    s.profiles.RegistryRevision(),
	}
	env, err := proto.New(proto.TypeWelcome, payload)
	if err != nil {
		return err
	}
	sess.sendEnvelope(env)
	return nil
}

// errSessionClosing signals that a close frame has already been sent, so the
// caller must stop reading without trying to close again.
var errSessionClosing = errors.New("server: the session is closing")

// errMalformedFrame signals that one frame could not be decoded. The connection
// is still usable, so the caller skips the frame and keeps reading.
var errMalformedFrame = errors.New("server: malformed frame")

// close ends the session with a WebSocket close code. It is idempotent.
func (sess *Session) close(code int, reason string) {
	sess.once.Do(func() {
		close(sess.closed)
		// A close frame should reach the client, so give it a moment.
		_ = sess.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		msg := websocket.FormatCloseMessage(code, truncateReason(reason))
		_ = sess.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
		_ = sess.conn.Close()
	})
}

// teardown removes the session from the server and cancels its executions.
func (sess *Session) teardown() {
	s := sess.srv

	sess.mu.Lock()
	sub := sess.telemetry
	cancels := make([]context.CancelFunc, 0, len(sess.cancels))
	for _, cancel := range sess.cancels {
		cancels = append(cancels, cancel)
	}
	pendingCount := len(sess.pending)
	sess.cancels = make(map[string]context.CancelFunc)
	sess.pending = make(map[string]bool)
	sess.mu.Unlock()

	if sub != nil {
		s.metrics.Unsubscribe(sub.ID)
	}
	// Actions started by this session are cancelled: a script that outlives the
	// phone that started it is a leak the user cannot see or stop. Cancelling
	// the session's own contexts is what stops them; the engine's registry is
	// consulted too, for actions started by another path.
	for _, cancel := range cancels {
		cancel()
	}

	s.mu.Lock()
	delete(s.sessions, sess.id)
	s.mu.Unlock()

	sess.close(websocket.CloseNormalClosure, "closed")
	sess.log.Info("session ended", "device", sess.device.ID, "session", sess.id, "cancelled_actions", pendingCount)
}

// --- helpers -------------------------------------------------------------

func payloadEcho(env proto.Envelope) string {
	var p struct {
		Echo string `json:"echo"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	return p.Echo
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// truncateReason keeps a close reason inside the 123-byte limit of the
// WebSocket close frame, which is easy to exceed with a formatted message.
func truncateReason(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "")
}

// markPending records an execution started by this session.
func (sess *Session) markPending(id string) {
	sess.mu.Lock()
	sess.pending[id] = true
	sess.mu.Unlock()
}

// trackExecution records how to stop an execution. The cancel function is kept
// until the execution finishes, so an action that outlives the synchronous reply
// window can still be stopped by the client, and so a disconnect stops it too.
func (sess *Session) trackExecution(id string, cancel context.CancelFunc) {
	sess.mu.Lock()
	sess.cancels[id] = cancel
	sess.mu.Unlock()
}

// clearPending forgets a finished execution.
func (sess *Session) clearPending(id string) {
	sess.mu.Lock()
	delete(sess.pending, id)
	delete(sess.cancels, id)
	sess.mu.Unlock()
}

// cancelExecution stops an execution this session started. It reports whether
// the execution existed.
func (sess *Session) cancelExecution(id string) bool {
	sess.mu.Lock()
	cancel, ok := sess.cancels[id]
	sess.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// engineRequest builds an engine request from a session and an envelope.
func (sess *Session) engineRequest(typ string, params json.RawMessage, profileID, pageID, buttonID string) engine.Request {
	return engine.Request{
		ExecutionID: engine.NewExecutionID(),
		DeviceID:    sess.device.ID,
		ProfileID:   profileID,
		PageID:      pageID,
		ButtonID:    buttonID,
		ActionType:  typ,
		Params:      params,
		Scopes:      sess.scopes,
	}
}

// runAction executes an action and replies, switching to the
// accepted/finished pair when the action outlives the synchronous window.
//
// The execution context is owned by the session, not by this function: an
// action that outlives the sync window must keep running, so its context may
// only be cancelled when the action finishes, when the client cancels it, or
// when the session ends. Cancelling it here (for example with a deferred
// cancel) would kill every action slower than the window, which is exactly the
// class of action the two-phase reply exists to support.
func (sess *Session) runAction(req proto.Envelope, er engine.Request) {
	sess.markPending(er.ExecutionID)

	// The synchronous window: short enough that the client's press feels
	// instant, long enough that most actions never produce a second message.
	const syncWindow = 1500 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	sess.trackExecution(er.ExecutionID, cancel)

	done := make(chan engine.Result, 1)
	errCh := make(chan error, 1)

	go func() {
		defer cancel()
		res, err := sess.srv.engine.Execute(ctx, er)
		if err != nil {
			errCh <- err
			return
		}
		done <- res
	}()

	finish := func(env proto.Envelope) {
		sess.clearPending(er.ExecutionID)
		sess.sendEnvelope(env)
	}

	select {
	case res := <-done:
		env, err := proto.Reply(req, proto.TypeActionResult, proto.ActionResultPayload{
			ExecutionID: er.ExecutionID,
			ActionID:    er.ActionKey(),
			ActionType:  er.ActionType,
			OK:          true,
			Output:      res.Output,
		})
		if err != nil {
			sess.log.Error("could not build an action result", "error", err)
			return
		}
		finish(env)

	case err := <-errCh:
		env, buildErr := proto.Reply(req, proto.TypeActionResult, proto.ActionResultPayload{
			ExecutionID: er.ExecutionID,
			ActionID:    er.ActionKey(),
			ActionType:  er.ActionType,
			OK:          false,
			Error: &proto.ActionError{
				Code:    engine.ErrorCode(err),
				Message: err.Error(),
			},
		})
		if buildErr != nil {
			sess.log.Error("could not build an action result", "error", buildErr)
			return
		}
		finish(env)

	case <-time.After(syncWindow):
		// Still running. Tell the client it was accepted and let the completion
		// arrive as an event, so a long script never blocks the client's request
		// table and never blocks this session's reader.
		env, err := proto.Reply(req, proto.TypeActionResult, proto.ActionResultPayload{
			ExecutionID: er.ExecutionID,
			ActionID:    er.ActionKey(),
			ActionType:  er.ActionType,
			OK:          true,
			Accepted:    true,
		})
		if err != nil {
			sess.log.Error("could not build an action result", "error", err)
			return
		}
		sess.sendEnvelope(env)

		go func() {
			defer sess.clearPending(er.ExecutionID)
			select {
			case res := <-done:
				sess.push(proto.TypeEventActionEnd, proto.ActionResultPayload{
					ExecutionID: er.ExecutionID,
					ActionID:    er.ActionKey(),
					ActionType:  er.ActionType,
					OK:          true,
					Output:      res.Output,
				})
			case err := <-errCh:
				sess.push(proto.TypeEventActionEnd, proto.ActionResultPayload{
					ExecutionID: er.ExecutionID,
					ActionID:    er.ActionKey(),
					ActionType:  er.ActionType,
					OK:          false,
					Error: &proto.ActionError{
						Code:    engine.ErrorCode(err),
						Message: err.Error(),
					},
				})
			}
		}()
	}
}
