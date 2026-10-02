package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mobiledeck/mobiledeck/host/internal/icons"
)

// The icon endpoints back the picker in the desktop panel.
//
// They are the one part of the host that talks to the internet, and only when a
// user asks for an icon: a search hits the icon set's catalogue, and a preview
// downloads and rasterises one icon into the local cache. Everything after that
// is offline, because the profile serves the cached PNG as a data URI
// (docs/PROTOCOL.md §5).
//
// No API key is involved. All five sets are open and public, which is what makes
// this work without the user registering anywhere.

// adminIconSets lists the icon sources the picker offers, with their licences.
func (s *Server) adminIconSets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	if s.icons == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"note":      "the icon cache is not configured on this host; image icons will render as placeholders",
			"sets":      []any{},
		})
		return
	}

	sets := make([]map[string]any, 0, len(icons.Sets))
	for _, set := range icons.Sets {
		sets = append(sets, map[string]any{
			"id":       set.ID,
			"name":     set.Name,
			"licence":  set.Licence,
			"homepage": set.Homepage,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"sets":      sets,
		"cache_dir": s.icons.CacheDir(),
		"note":      "searching needs the internet; once an icon is used it is cached and the profile works offline",
	})
}

// adminIconSearch lists the icons in a set, filtered by a substring.
//
// The filter runs here rather than in the browser because the catalogue is a few
// thousand names and shipping all of them to the panel on every keystroke would
// be pointless traffic. The full catalogue is cached on disk for a day, so a
// search after the first one is served locally.
func (s *Server) adminIconSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	if s.icons == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"the icon cache is not configured on this host")
		return
	}

	setID := r.URL.Query().Get("set")
	if setID == "" {
		setID = icons.Sets[0].ID
	}
	set, ok := icons.SetByID(setID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_argument",
			fmt.Sprintf("unknown icon set %q; try one of %s", setID, setIDs()))
		return
	}

	limit := 60
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	names, err := s.icons.Catalog(r.Context(), set)
	if err != nil {
		// Reported as a gateway problem rather than an internal one: the cause
		// is the network or the icon service, not this host.
		writeError(w, http.StatusBadGateway, "upstream_unavailable",
			"could not reach the icon set: "+err.Error())
		return
	}

	matches := make([]string, 0, limit)
	total := 0
	for _, n := range names {
		if query != "" && !strings.Contains(n, query) {
			continue
		}
		total++
		if len(matches) < limit {
			matches = append(matches, n)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"set":     set.ID,
		"name":    set.Name,
		"licence": set.Licence,
		"query":   query,
		"total":   total,
		"icons":   matches,
		"cached":  s.cachedNames(set),
	})
}

// adminIconPreview returns the icon as a data URI, fetching and rasterising it
// on first use.
//
// This is also what "use this icon" does: once it is in the cache, the profile
// references it by file name and the data URI is inlined whenever the profile is
// served.
func (s *Server) adminIconPreview(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	if s.icons == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"the icon cache is not configured on this host")
		return
	}

	setID := r.URL.Query().Get("set")
	if setID == "" {
		setID = icons.Sets[0].ID
	}
	set, ok := icons.SetByID(setID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_argument",
			fmt.Sprintf("unknown icon set %q", setID))
		return
	}

	res, err := s.icons.Get(r.Context(), set, name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file_name": res.FileName,
		"data_uri":  res.DataURI,
		"source":    res.Source,
		"bytes":     res.Bytes,
	})
}

// adminIconImport stores a PNG the user supplied.
//
// The body is a JSON object with a base64 payload rather than a multipart
// upload: the panel has the bytes in the browser already, and one JSON shape
// keeps the panel's bridge to a single function.
func (s *Server) adminIconImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if s.icons == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"the icon cache is not configured on this host")
		return
	}

	var body struct {
		// Name is the file name to show; the stored name is derived from it.
		Name string `json:"name"`
		// Data is a data URI or a bare base64 payload.
		Data string `json:"data"`
	}
	if err := decodeBody(w, r, &body, 8<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "name is required")
		return
	}

	payload := body.Data
	if i := strings.Index(payload, ","); strings.HasPrefix(payload, "data:") && i >= 0 {
		payload = payload[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "data is not valid base64: "+err.Error())
		return
	}

	path, err := s.icons.WriteImported(body.Name, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	uri := s.icons.Resolve(path)
	s.log.Info("icon imported", "file", path, "bytes", len(raw))
	writeJSON(w, http.StatusOK, map[string]any{
		"file_name": path,
		"data_uri":  uri,
		"source":    "custom",
	})
}

// cachedNames reports which of a set's icons are already on disk, so the picker
// can mark them and so a user can see what works offline.
func (s *Server) cachedNames(set icons.Set) []string {
	out := []string{}
	for _, n := range s.icons.CachedInSet(set.ID) {
		out = append(out, n)
	}
	return out
}

func setIDs() string {
	ids := make([]string, 0, len(icons.Sets))
	for _, s := range icons.Sets {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ", ")
}
