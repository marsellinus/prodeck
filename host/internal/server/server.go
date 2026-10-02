// Package server is the host's network edge: the WebSocket endpoint, the small
// REST surface used for discovery and pairing, the mDNS advertisement, and the
// loopback-only admin API.
//
// It owns no domain logic. It validates envelopes, checks scopes through the
// auth manager, calls the engine, and streams events. That boundary is what
// keeps a protocol change from touching the action implementations and vice
// versa (docs/ARCHITECTURE.md §2).
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/icons"
	"github.com/mobiledeck/mobiledeck/host/internal/logging"
	"github.com/mobiledeck/mobiledeck/host/internal/profiles"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
	"github.com/mobiledeck/mobiledeck/host/internal/telemetry"
	"github.com/mobiledeck/mobiledeck/host/internal/tlsutil"
)

// Version is the agent version reported to clients and by `mobiledeck version`.
const Version = "0.1.0"

// Server is the running host.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	auth     *auth.Manager
	profiles *profiles.Registry
	engine   *engine.Engine
	metrics  *telemetry.Collector
	icons    *icons.Client
	tls      *tlsutil.Material

	http   *http.Server
	mdns   *advertiser
	listen net.Listener

	mu       sync.RWMutex
	sessions map[string]*Session
	started  time.Time
}

// Options bundles the collaborators the server needs.
type Options struct {
	Config    config.Config
	Log       *slog.Logger
	Auth      *auth.Manager
	Profiles  *profiles.Registry
	Engine    *engine.Engine
	Telemetry *telemetry.Collector
	// Icons resolves the image icons a profile references into data URIs. It may
	// be nil, in which case an image icon is simply left unresolved and the
	// client draws its placeholder.
	Icons *icons.Client
	TLS   *tlsutil.Material
}

// New builds a server. It does not listen until Start is called.
func New(opts Options) (*Server, error) {
	if opts.Log == nil {
		return nil, errors.New("server: a logger is required")
	}
	if opts.Auth == nil {
		return nil, errors.New("server: an auth manager is required")
	}
	if opts.Profiles == nil {
		return nil, errors.New("server: a profile registry is required")
	}
	if opts.Engine == nil {
		return nil, errors.New("server: an engine is required")
	}
	if opts.Telemetry == nil {
		return nil, errors.New("server: a telemetry collector is required")
	}

	s := &Server{
		cfg:      opts.Config,
		log:      opts.Log,
		auth:     opts.Auth,
		profiles: opts.Profiles,
		engine:   opts.Engine,
		metrics:  opts.Telemetry,
		icons:    opts.Icons,
		tls:      opts.TLS,
		sessions: make(map[string]*Session),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/info", s.handleInfo)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/pair", s.handlePair)
	mux.HandleFunc("/api/v1/admin/", s.handleAdmin)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/", s.handleRoot)

	s.http = &http.Server{
		Handler:           s.withMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // WebSocket connections must not be cut by a write deadline
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(opts.Log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}
	return s, nil
}

// Listen binds the socket and starts advertising, without serving yet.
//
// Bind is separated from Serve so a caller can report the real address before
// the first request is accepted: with an ephemeral port the address is not known
// until the bind happens, and a start-up banner printed before it would show an
// empty port.
func (s *Server) Listen() error {
	addr := net.JoinHostPort(s.cfg.Bind, fmt.Sprintf("%d", s.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("server: listen on %s: %w (is another mobiledeck instance or another program already using that port?)", addr, err)
	}
	s.listen = ln
	s.started = time.Now()

	// The telemetry collector samples only while something is subscribed, but its
	// loop has to be running for that to happen. Starting it here rather than in
	// the CLI means an embedder cannot forget it and silently ship a host whose
	// telemetry never emits.
	s.metrics.Start()

	if s.cfg.TLS.Enabled {
		if s.tls == nil {
			_ = ln.Close()
			return errors.New("server: tls is enabled but no certificate material was provided")
		}
		cert := s.tls.Cert
		s.http.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			// The client pins our fingerprint, so it does not need a chain. We
			// still present the self-signed leaf as its own root.
			ClientAuth: tls.NoClientCert,
		}
		ln = tls.NewListener(ln, s.http.TLSConfig)
	}

	if s.cfg.Discovery.MDNS {
		adv, err := startAdvertiser(s.cfg, s.tls, s.log)
		if err != nil {
			// Discovery is a convenience, not a requirement: a client can
			// always enter the address by hand. Report and continue.
			s.log.Warn("mDNS advertisement unavailable; clients must enter the address manually", "error", err)
		} else {
			s.mdns = adv
		}
	}

	s.log.Info("listening",
		"addr", s.listen.Addr().String(),
		"tls", s.cfg.TLS.Enabled,
		"fingerprint", s.fingerprint(),
	)
	return nil
}

// Serve accepts connections until ctx is cancelled or the server stops. Listen
// must have been called first.
func (s *Server) Serve(ctx context.Context) error {
	if s.listen == nil {
		return errors.New("server: Serve was called before Listen")
	}
	ln := s.listen

	errCh := make(chan error, 1)
	go func() {
		err := s.http.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return s.Close()
	case err := <-errCh:
		return err
	}
}

// Start binds and serves. It is the convenience form used by tests and by any
// embedder that does not need to inspect the address first.
func (s *Server) Start(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve(ctx)
}

// Close shuts the server down, closing every session first so clients receive a
// clean close frame instead of a dropped socket.
func (s *Server) Close() error {
	s.log.Info("shutting down")

	s.mu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.RUnlock()
	for _, sess := range sessions {
		sess.close(proto.CloseTryAgainLater, "host is shutting down")
	}

	if s.mdns != nil {
		s.mdns.shutdown()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

// Addr returns the bound address.
func (s *Server) Addr() string {
	if s.listen == nil {
		return ""
	}
	return s.listen.Addr().String()
}

// Port returns the bound port.
func (s *Server) Port() int {
	if s.listen == nil {
		return 0
	}
	if tcp, ok := s.listen.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return s.cfg.Port
}

// Sessions returns the connected device ids.
func (s *Server) Sessions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		out = append(out, id)
	}
	return out
}

// SessionCount reports how many devices are connected.
func (s *Server) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// Uptime reports how long the server has been listening.
func (s *Server) Uptime() time.Duration {
	if s.started.IsZero() {
		return 0
	}
	return time.Since(s.started)
}

func (s *Server) fingerprint() string {
	if s.tls == nil {
		return ""
	}
	return s.tls.Fingerprint
}

// withMiddleware adds panic recovery, request logging and the security headers
// every response carries.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// A panic in a handler must not take the whole host down: a deck that
		// stays up and logs the bug is far more useful than a crash loop.
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in http handler", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			}
		}()

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")

		next.ServeHTTP(w, r)
		s.log.Debug("http",
			"method", r.Method, "path", r.URL.Path,
			"remote", remoteIP(r), "duration_ms", time.Since(start).Milliseconds())
	})
}

// remoteIP extracts the peer address. It deliberately does not trust
// X-Forwarded-For: the host is not designed to sit behind a proxy, and honouring
// a client-supplied header would let an attacker forge the address that the
// pairing rate limit is keyed on.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLoopback reports whether a request came from the local machine. The admin
// API is restricted to it, so an unprivileged local process can administer the
// host while a LAN peer cannot.
func isLoopback(r *http.Request) bool {
	ip := net.ParseIP(remoteIP(r))
	return ip != nil && ip.IsLoopback()
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error":   code,
		"message": msg,
	})
}

// decodeBody reads a JSON request body with a size limit and strict field
// checking, so a typo in a hand-written request is reported rather than ignored.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = proto.MaxMessageBytes
	}
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(body)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

// Logger returns the logger the server uses, for the CLI's `logs` command.
func (s *Server) Logger() *slog.Logger { return s.log }

// LogWriter adapts the standard library logger used by net/http.
var _ = logging.Writer

// hasFeature reports whether a feature string is advertised to clients.
func hasFeature(features []string, name string) bool {
	for _, f := range features {
		if strings.EqualFold(f, name) {
			return true
		}
	}
	return false
}
