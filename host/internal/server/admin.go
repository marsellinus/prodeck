package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
)

// The admin API is bound to loopback by isLoopback, and additionally requires
// the per-process admin token in the Authorization header. Two independent
// barriers because this surface can revoke credentials and rewrite profiles:
// a local unprivileged process should not be able to take over a deck by
// accident, and a browser tab on the host should not be able to reach it at all
// (which is also why no CORS headers are ever emitted).

// handleAdmin routes the admin API.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		s.log.Warn("refused an admin request from a non-local address", "ip", remoteIP(r), "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "loopback_only",
			"the admin API only accepts connections from this machine")
		return
	}
	token := bearerToken(r)
	if token == "" || !s.auth.VerifyAdminToken(token) {
		s.log.Warn("refused an admin request with a bad token", "ip", remoteIP(r), "path", r.URL.Path)
		writeError(w, http.StatusUnauthorized, "unauthorized",
			"a valid admin token is required; read it from `mobiledeck status --json`")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")

	switch {
	case len(parts) == 1 && parts[0] == "status":
		s.adminStatus(w, r)
	case len(parts) == 1 && parts[0] == "devices":
		s.adminDevices(w, r)
	case len(parts) == 2 && parts[0] == "devices":
		s.adminDevice(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "devices" && parts[2] == "scopes":
		s.adminDeviceScopes(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "profiles":
		s.adminProfiles(w, r)
	case len(parts) == 2 && parts[0] == "profiles" && parts[1] == "reload":
		s.adminProfilesReload(w, r)
	case len(parts) == 1 && parts[0] == "pair":
		s.adminPair(w, r)
	case len(parts) == 1 && parts[0] == "shutdown":
		s.adminShutdown(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown admin endpoint "+r.URL.Path)
	}
}

// adminStatus reports the runtime state.
func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	pairing := map[string]any{"open": false}
	if pin, ok := s.auth.ActivePIN(); ok {
		pairing = map[string]any{
			"open":         true,
			"pin":          pin.Code,
			"expires_in_s": int(time.Until(pin.ExpiresAt).Round(time.Second).Seconds()),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host_id":         s.cfg.HostID,
		"host_name":       s.cfg.HostName,
		"os":              s.engine.Platform().OSName,
		"version":         Version,
		"addr":            s.Addr(),
		"addresses":       s.LocalAddresses(),
		"tls":             s.cfg.TLS.Enabled,
		"fingerprint":     s.fingerprint(),
		"uptime_s":        int64(s.Uptime().Seconds()),
		"sessions":        s.Sessions(),
		"profiles":        s.profiles.IDs(),
		"pairing":         pairing,
		"actions_running": s.engine.Running(),
	})
}

// adminDevices lists, and with a sub-path mutates, paired devices.
func (s *Server) adminDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET, or a sub-path such as /devices/<id>")
		return
	}
	devices := s.auth.Devices()
	out := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		out = append(out, deviceView(d, s.Sessions()))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// adminDevice handles one device: GET, DELETE, PATCH.
func (s *Server) adminDevice(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		d, ok := s.auth.Device(id)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no device "+id)
			return
		}
		writeJSON(w, http.StatusOK, deviceView(d, s.Sessions()))

	case http.MethodDelete:
		ok, err := s.auth.Revoke(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no device "+id)
			return
		}
		// Kick any live session for the revoked device, otherwise it would keep
		// working until its socket happened to drop.
		s.disconnectDevice(id, "this device was revoked on the host")
		s.log.Info("device revoked", "device", id)
		writeJSON(w, http.StatusOK, map[string]any{"revoked": id})

	case http.MethodPatch:
		var body struct {
			Name     *string `json:"name,omitempty"`
			Disabled *bool   `json:"disabled,omitempty"`
		}
		if err := decodeBody(w, r, &body, 16*1024); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if body.Name != nil {
			if err := s.auth.Rename(id, *body.Name); err != nil {
				writeError(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
		}
		if body.Disabled != nil {
			if err := s.auth.SetDisabled(id, *body.Disabled); err != nil {
				writeError(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
			if *body.Disabled {
				s.disconnectDevice(id, "this device was disabled on the host")
			}
		}
		d, _ := s.auth.Device(id)
		writeJSON(w, http.StatusOK, deviceView(d, s.Sessions()))

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET, PATCH or DELETE")
	}
}

// adminDeviceScopes replaces a device's scope set.
func (s *Server) adminDeviceScopes(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use PUT")
		return
	}
	var body struct {
		Scopes []string `json:"scopes"`
	}
	if err := decodeBody(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	scopes, err := auth.ParseScopes(body.Scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	if err := s.auth.SetScopes(id, scopes); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.log.Info("device scopes changed", "device", id, "scopes", auth.ScopeList(scopes))
	d, _ := s.auth.Device(id)
	writeJSON(w, http.StatusOK, deviceView(d, s.Sessions()))
}

// adminProfiles lists loaded profiles and their load errors.
func (s *Server) adminProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET, or POST /profiles/reload")
		return
	}
	out := make([]map[string]any, 0)
	for _, e := range s.profiles.List() {
		out = append(out, map[string]any{
			"id":        e.Doc.ID,
			"name":      e.Doc.Name,
			"path":      e.Path,
			"revision":  e.Revision,
			"pages":     e.Doc.PageIDs(),
			"loaded_at": e.LoadedAt.UTC().Format(time.RFC3339),
		})
	}
	errs := s.profiles.Errors()
	failures := make([]map[string]any, 0, len(errs))
	for id, err := range errs {
		failures = append(failures, map[string]any{"id": id, "error": err.Error()})
	}
	sort.Slice(failures, func(i, j int) bool {
		return fmt.Sprint(failures[i]["id"]) < fmt.Sprint(failures[j]["id"])
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles": out,
		"errors":   failures,
		"dir":      s.profiles.Dir(),
	})
}

// adminProfilesReload re-reads every profile from disk.
func (s *Server) adminProfilesReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if err := s.profiles.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles": s.profiles.IDs(),
		"revision": s.profiles.RegistryRevision(),
	})
}

// adminPair opens a pairing window and returns the PIN. This is how the CLI
// asks a running daemon for a PIN instead of generating one the daemon would
// not honour.
func (s *Server) adminPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	pin, err := s.auth.IssuePIN()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.log.Info("pairing opened", "expires_in_s", int(time.Until(pin.ExpiresAt).Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{
		"pin":          pin.Code,
		"expires_at":   pin.ExpiresAt.UTC().Format(time.RFC3339),
		"expires_in_s": int(time.Until(pin.ExpiresAt).Round(time.Second).Seconds()),
		"host_name":    s.cfg.HostName,
	})
}

// adminShutdown stops the host cleanly.
func (s *Server) adminShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
	s.log.Info("shutdown requested through the admin API")
	go func() {
		time.Sleep(200 * time.Millisecond) // let the response flush
		if err := s.Close(); err != nil {
			s.log.Warn("shutdown returned an error", "error", err)
		}
	}()
}

// disconnectDevice closes every session belonging to a device.
func (s *Server) disconnectDevice(deviceID, reason string) {
	s.mu.RLock()
	var targets []*Session
	for _, sess := range s.sessions {
		if sess.device.ID == deviceID {
			targets = append(targets, sess)
		}
	}
	s.mu.RUnlock()
	for _, sess := range targets {
		sess.close(closeCodeFor(reason), reason)
	}
}

func closeCodeFor(reason string) int {
	if strings.Contains(reason, "revoked") || strings.Contains(reason, "disabled") {
		return 4403
	}
	return 1013
}

// deviceView renders a device for the API, never including the token hash.
func deviceView(d auth.Device, live []string) map[string]any {
	view := map[string]any{
		"id":                d.ID,
		"name":              d.Name,
		"platform":          d.Platform,
		"model":             d.Model,
		"scopes":            scopeStrings(d.Scopes),
		"created_at":        d.CreatedAt.UTC().Format(time.RFC3339),
		"disabled":          d.Disabled,
		"token_fingerprint": d.TokenFingerprint,
		"connected":         containsString(live, d.ID),
	}
	if !d.LastSeen.IsZero() {
		view["last_seen"] = d.LastSeen.UTC().Format(time.RFC3339)
	}
	if d.LastIP != "" {
		view["last_ip"] = d.LastIP
	}
	return view
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// bearerToken extracts a bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

var _ = json.Marshal
var _ = io.Discard
