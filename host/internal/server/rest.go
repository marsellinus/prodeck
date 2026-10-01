package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
	"github.com/mobiledeck/mobiledeck/host/internal/tlsutil"
)

// InfoResponse is the body of GET /api/v1/info (docs/PROTOCOL.md §2.1).
//
// It is unauthenticated by design: a client must be able to identify and verify
// a host before it holds any credential, which is what makes fingerprint
// pinning possible during pairing.
type InfoResponse struct {
	HostID        string      `json:"host_id"`
	HostName      string      `json:"host_name"`
	OS            string      `json:"os"`
	AgentVer      string      `json:"agent_version"`
	Protocol      Protocol    `json:"protocol"`
	TLS           TLSInfo     `json:"tls"`
	Pairing       PairingInfo `json:"pairing"`
	Profiles      ProfilesSum `json:"profiles"`
	Features      []string    `json:"features"`
	ServerTime    int64       `json:"server_time"`
	Power         any         `json:"power,omitempty"`
	Metrics       []string    `json:"metrics,omitempty"`
	ActiveProfile string      `json:"active_profile,omitempty"`
}

// Protocol is the supported protocol range.
type Protocol struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// TLSInfo describes the transport security state.
type TLSInfo struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint_sha256,omitempty"`
	NotAfter    string `json:"not_after,omitempty"`
	Plaintext   bool   `json:"plaintext_allowed,omitempty"`
}

// PairingInfo describes the pairing window.
type PairingInfo struct {
	Open       bool   `json:"open"`
	Method     string `json:"method"`
	ExpiresInS int    `json:"expires_in_s,omitempty"`
	PINLength  int    `json:"pin_length,omitempty"`
}

// ProfilesSum summarises the loaded profiles.
type ProfilesSum struct {
	Count    int    `json:"count"`
	Revision string `json:"revision"`
}

// handleInfo serves the discovery/verification endpoint.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	info := InfoResponse{
		HostID:     s.cfg.HostID,
		HostName:   s.cfg.HostName,
		OS:         s.engine.Platform().OSName,
		AgentVer:   Version,
		Protocol:   Protocol{Min: proto.Version, Max: proto.Version},
		ServerTime: time.Now().UnixMilli(),
		Features:   s.features(),
		Metrics:    s.metrics.Available(),
	}

	if s.tls != nil && s.cfg.TLS.Enabled {
		info.TLS = TLSInfo{
			Enabled:     true,
			Fingerprint: s.tls.Fingerprint,
			NotAfter:    s.tls.NotAfter.UTC().Format(time.RFC3339),
		}
	} else {
		info.TLS = TLSInfo{
			Enabled:   false,
			Plaintext: true,
		}
	}

	if pin, ok := s.auth.ActivePIN(); ok {
		info.Pairing = PairingInfo{
			Open:       true,
			Method:     "pin",
			ExpiresInS: int(time.Until(pin.ExpiresAt).Round(time.Second).Seconds()),
			PINLength:  len(pin.Code),
		}
	} else {
		info.Pairing = PairingInfo{Open: false, Method: "pin"}
	}

	info.Profiles = ProfilesSum{
		Count:    len(s.profiles.IDs()),
		Revision: s.profiles.RegistryRevision(),
	}
	if def := s.profiles.Default(); def != nil {
		info.ActiveProfile = def.Doc.ID
	}
	info.Power = s.engine.Platform().Power.Capabilities()

	writeJSON(w, http.StatusOK, info)
}

// HealthResponse is the body of GET /api/v1/health.
type HealthResponse struct {
	Status    string `json:"status"`
	UptimeS   int64  `json:"uptime_s"`
	Sessions  int    `json:"sessions"`
	Profiles  int    `json:"profiles"`
	Telemetry int    `json:"telemetry_subscribers"`
	Running   int    `json:"actions_running"`
}

// handleHealth is the liveness probe used by the CLI and by scripts/.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		UptimeS:   int64(s.Uptime().Seconds()),
		Sessions:  s.SessionCount(),
		Profiles:  len(s.profiles.IDs()),
		Telemetry: s.metrics.SubscriberCount(),
		Running:   len(s.engine.Running()),
	})
}

// PairRequest is the body of POST /api/v1/pair.
type PairRequest struct {
	PIN    string          `json:"pin"`
	Device PairDeviceInput `json:"device"`
}

// PairDeviceInput describes the pairing client.
type PairDeviceInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	Model    string `json:"model,omitempty"`
}

// PairResponse is a successful pairing.
type PairResponse struct {
	DeviceID string   `json:"device_id"`
	Token    string   `json:"token"`
	Scopes   []string `json:"scopes"`
	Host     PairHost `json:"host"`
}

// PairHost identifies the host to the client after pairing.
type PairHost struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	OS   string `json:"os"`
}

// PairError is a failed pairing.
type PairError struct {
	Error             string `json:"error"`
	Message           string `json:"message"`
	AttemptsRemaining int    `json:"attempts_remaining,omitempty"`
}

// handlePair exchanges a PIN for a device token.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	ip := remoteIP(r)

	// The plaintext guard: pairing hands out a credential, so it must not
	// happen over an unencrypted LAN socket unless the operator explicitly
	// opted in (docs/SECURITY.md §5).
	if !s.cfg.TLS.Enabled && !isLoopback(r) && !s.cfg.InsecureAllowPlaintext {
		s.log.Warn("refused a pairing attempt over plaintext from a non-local address",
			"ip", ip, "hint", "restart with --insecure-allow-plaintext to allow this")
		writeError(w, http.StatusForbidden, "plaintext_refused",
			"this host requires TLS for pairing; restart it with --insecure-allow-plaintext if you accept that the PIN and token travel unencrypted on the LAN")
		return
	}

	var req PairRequest
	if err := decodeBody(w, r, &req, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	if strings.TrimSpace(req.PIN) == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "pin is required; run `mobiledeck pair` on the host to get one")
		return
	}
	if strings.TrimSpace(req.Device.ID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "device.id is required and must be stable across reconnects")
		return
	}
	if len(req.Device.ID) > 64 || len(req.Device.Name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_argument", "device.id and device.name are limited to 64 bytes")
		return
	}

	result, err := s.auth.Pair(req.PIN, auth.DeviceInfo{
		ID:       req.Device.ID,
		Name:     req.Device.Name,
		Platform: req.Device.Platform,
		Model:    req.Device.Model,
	}, ip, nil)

	if err != nil {
		status, code, msg := pairError(err)
		s.log.Warn("pairing failed", "ip", ip, "device", req.Device.ID, "reason", code)
		writeJSON(w, status, PairError{Error: code, Message: msg})
		return
	}

	s.log.Info("device paired", "device", result.Device.ID, "name", result.Device.Name, "ip", ip, "scopes", auth.ScopeList(result.Scopes))
	writeJSON(w, http.StatusCreated, PairResponse{
		DeviceID: result.Device.ID,
		Token:    result.Token,
		Scopes:   scopeStrings(result.Scopes),
		Host: PairHost{
			ID:   s.cfg.HostID,
			Name: s.cfg.HostName,
			OS:   s.engine.Platform().OSName,
		},
	})
}

// pairError maps an auth error onto an HTTP status and a stable error code.
func pairError(err error) (int, string, string) {
	switch {
	case errors.Is(err, auth.ErrNoPIN):
		return http.StatusConflict, "pairing_closed", "pairing is not open on the host; run `mobiledeck pair` to get a PIN"
	case errors.Is(err, auth.ErrPINExpired):
		return http.StatusUnauthorized, "pin_expired", "the PIN has expired; request a new one with `mobiledeck pair`"
	case errors.Is(err, auth.ErrPINInvalid):
		return http.StatusUnauthorized, "invalid_pin", "that PIN is not correct"
	case errors.Is(err, auth.ErrTooManyTries):
		return http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts; the PIN has been invalidated and pairing is locked for a while"
	case errors.Is(err, auth.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited", "too many pairing attempts from this address"
	case errors.Is(err, auth.ErrDeviceDisabled):
		return http.StatusForbidden, "device_disabled", "this device was disabled on the host; re-enable it with `mobiledeck devices enable <id>`"
	default:
		return http.StatusInternalServerError, "internal", "pairing failed on the host; check `mobiledeck logs`"
	}
}

func scopeStrings(scopes []auth.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		out = append(out, string(sc))
	}
	return out
}

// features is the capability list advertised in `welcome` and in /info.
func (s *Server) features() []string {
	features := []string{
		"profile.hotreload",
		"action.cancel",
		"button.state",
		"nav.breadcrumb",
	}
	if s.metrics.SubscriberCount() >= 0 {
		features = append(features, "telemetry")
	}
	if s.cfg.TLS.Enabled {
		features = append(features, "tls.pinning")
	}
	return features
}

// handleRoot serves a small plain-text banner on the root path. It exists so
// that pointing a browser at the host answers the obvious question ("is it
// running?") without requiring the CLI.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "MobileDeck host %s (%s)\n", Version, s.cfg.HostName)
	fmt.Fprintf(w, "protocol: %d\n", proto.Version)
	fmt.Fprintf(w, "os: %s\n", s.engine.Platform().OSName)
	fmt.Fprintf(w, "uptime: %s\n", s.Uptime().Round(time.Second))
	fmt.Fprintf(w, "profiles: %d\n", len(s.profiles.IDs()))
	fmt.Fprintf(w, "devices connected: %d\n", s.SessionCount())
	if s.cfg.TLS.Enabled && s.tls != nil {
		fmt.Fprintf(w, "tls fingerprint: %s\n", s.tls.Fingerprint)
	} else {
		fmt.Fprintln(w, "tls: disabled")
	}
	fmt.Fprintf(w, "\nendpoints: /api/v1/info /api/v1/health /api/v1/pair /ws\n")
}

// LocalAddresses returns the URLs a client on the LAN can use, for the CLI's
// startup banner and for `mobiledeck status`.
func (s *Server) LocalAddresses() []string {
	ips, err := tlsutil.LocalIPs()
	if err != nil {
		return nil
	}
	scheme := "http"
	if s.cfg.TLS.Enabled {
		scheme = "https"
	}
	port := s.Port()
	if port == 0 {
		port = s.cfg.Port
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip.IsLoopback() {
			continue
		}
		out = append(out, fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))))
	}
	return out
}

// Fingerprint returns the TLS certificate fingerprint, or "" when TLS is off.
func (s *Server) Fingerprint() string { return s.fingerprint() }

// EmitEvent delivers an engine event to the interested sessions. The engine
// calls this; it is the seam that keeps the engine free of session knowledge.
func (s *Server) EmitEvent(ev engine.Event) { s.emitEvent(ev) }
