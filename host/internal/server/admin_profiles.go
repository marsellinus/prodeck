package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/profile"
)

// The profile-editing admin endpoints.
//
// They exist so the desktop GUI can build a layout without hand-editing JSON,
// and they are deliberately narrow: each one edits one thing, validates the
// whole document before writing it, and refuses anything the client could not
// render. A layout editor that can save an invalid profile is worse than no
// editor, because the deck then fails to load and the user has no idea which
// edit broke it.
//
// Every write goes through profiles.Registry.Save, which validates and reloads,
// so a saved profile is live on connected phones immediately.

// adminProfileDocument handles GET (the raw document) and PUT (replace it) for
// one profile.
func (s *Server) adminProfileDocument(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		raw, err := s.profiles.Export(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)

	case http.MethodPut:
		var doc profile.Profile
		if err := decodeBody(w, r, &doc, 4<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if doc.ID != id {
			writeError(w, http.StatusBadRequest, "invalid_argument",
				fmt.Sprintf("the document's id is %q but the target is %q", doc.ID, id))
			return
		}
		if err := s.profiles.Save(id, &doc); err != nil {
			// A validation failure is the common case here, and the message
			// names the JSON pointer, so it is returned verbatim for the editor
			// to show next to the offending field.
			writeError(w, http.StatusBadRequest, "invalid_profile", err.Error())
			return
		}
		s.log.Info("profile saved", "profile", id, "by", "gui")
		s.broadcastProfileChanged(id, s.revisionOf(id))
		s.adminProfileDocument(w, &http.Request{Method: http.MethodGet}, id)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or PUT")
	}
}

// adminProfilePage handles PUT (upsert) and DELETE of one page.
func (s *Server) adminProfilePage(w http.ResponseWriter, r *http.Request, id, pageID string) {
	doc, err := s.loadProfileDoc(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	switch r.Method {
	case http.MethodPut:
		var page profile.Page
		if err := decodeBody(w, r, &page, 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if page.ID != pageID {
			writeError(w, http.StatusBadRequest, "invalid_argument",
				fmt.Sprintf("the page's id is %q but the target is %q", page.ID, pageID))
			return
		}
		replaced := false
		for i := range doc.Pages {
			if doc.Pages[i].ID == pageID {
				doc.Pages[i] = page
				replaced = true
				break
			}
		}
		if !replaced {
			doc.Pages = append(doc.Pages, page)
		}

	case http.MethodDelete:
		// The root page and the last page cannot be removed: a profile with no
		// pages has nothing to render, and the client would show a blank deck
		// with no way back.
		if len(doc.Pages) <= 1 {
			writeError(w, http.StatusConflict, "conflict", "a profile must keep at least one page")
			return
		}
		if doc.RootPage == pageID {
			writeError(w, http.StatusConflict, "conflict",
				"the root page cannot be deleted; set another page as the root first")
			return
		}
		kept := doc.Pages[:0]
		for _, p := range doc.Pages {
			if p.ID != pageID {
				kept = append(kept, p)
			}
		}
		if len(kept) == len(doc.Pages) {
			writeError(w, http.StatusNotFound, "not_found", "no page "+pageID)
			return
		}
		doc.Pages = kept

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use PUT or DELETE")
		return
	}

	s.saveProfileDoc(w, id, doc)
}

// adminProfileButton handles PUT (upsert) and DELETE of one button on a page.
func (s *Server) adminProfileButton(w http.ResponseWriter, r *http.Request, id, pageID, buttonID string) {
	doc, err := s.loadProfileDoc(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	var page *profile.Page
	for i := range doc.Pages {
		if doc.Pages[i].ID == pageID {
			page = &doc.Pages[i]
			break
		}
	}
	if page == nil {
		writeError(w, http.StatusNotFound, "not_found", "no page "+pageID)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var button profile.Button
		if err := decodeBody(w, r, &button, 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
			return
		}
		if button.ID != buttonID {
			writeError(w, http.StatusBadRequest, "invalid_argument",
				fmt.Sprintf("the button's id is %q but the target is %q", button.ID, buttonID))
			return
		}
		replaced := false
		for i := range page.Buttons {
			if page.Buttons[i].ID == buttonID {
				page.Buttons[i] = button
				replaced = true
				break
			}
		}
		if !replaced {
			page.Buttons = append(page.Buttons, button)
		}

	case http.MethodDelete:
		kept := page.Buttons[:0]
		for _, b := range page.Buttons {
			if b.ID != buttonID {
				kept = append(kept, b)
			}
		}
		if len(kept) == len(page.Buttons) {
			writeError(w, http.StatusNotFound, "not_found", "no button "+buttonID)
			return
		}
		page.Buttons = kept

	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use PUT or DELETE")
		return
	}

	s.saveProfileDoc(w, id, doc)
}

// adminActions lists every action this host can run, with the parameters each
// one accepts.
//
// This is what makes the editor's action picker honest: it offers exactly what
// the host will execute, rather than a hardcoded list in the GUI that drifts
// from the registry.
func (s *Server) adminActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	type actionDoc struct {
		Type    string   `json:"type"`
		Scope   string   `json:"scope"`
		Params  []string `json:"params"`
		Example string   `json:"example"`
	}

	docs := map[string]actionDoc{
		"keyboard.shortcut": {Params: []string{"keys", "mode", "interval_ms"},
			Example: `{"keys":["CTRL","C"]}`},
		"keyboard.text": {Params: []string{"text", "interval_ms"},
			Example: `{"text":"hello","interval_ms":20}`},
		"keyboard.key":        {Params: []string{"key", "mode"}, Example: `{"key":"ENTER"}`},
		"mouse.click":         {Params: []string{"button", "count"}, Example: `{"button":"left","count":1}`},
		"mouse.move":          {Params: []string{"dx", "dy"}, Example: `{"dx":0,"dy":100}`},
		"mouse.move_absolute": {Params: []string{"x", "y"}, Example: `{"x":0.5,"y":0.5}`},
		"mouse.scroll":        {Params: []string{"dx", "dy"}, Example: `{"dy":3}`},
		"launch_application": {Params: []string{"target", "args", "cwd", "env", "detach"},
			Example: `{"target":{"windows":"notepad.exe","linux":"gedit"}}`},
		"open_url":      {Params: []string{"url"}, Example: `{"url":"https://example.com"}`},
		"open_folder":   {Params: []string{"path"}, Example: `{"path":"~"}`},
		"open_terminal": {Params: []string{"cwd", "command"}, Example: `{}`},
		"run_script": {Params: []string{"path", "args", "cwd", "env", "timeout_ms", "interpreter"},
			Example: `{"path":"scripts/hello.sh"}`},
		"run_command": {Params: []string{"command", "shell", "cwd", "env", "timeout_ms"},
			Example: `{"command":"echo hello"}`},
		"media.play": {}, "media.pause": {}, "media.play_pause": {},
		"media.stop": {}, "media.next": {}, "media.previous": {},
		"volume.up": {}, "volume.down": {}, "volume.mute": {},
		"volume.set":  {Params: []string{"level"}, Example: `{"level":40}`},
		"system.lock": {}, "system.sleep": {},
		"system.shutdown":     {Params: []string{"confirm"}, Example: `{"confirm":true}`},
		"system.restart":      {Params: []string{"confirm"}, Example: `{"confirm":true}`},
		"system.stats":        {Params: []string{"metric"}, Example: `{}`},
		"system.info":         {},
		"deck.open_page":      {Params: []string{"profile_id", "page_id"}, Example: `{"page_id":"media"}`},
		"deck.back":           {},
		"deck.change_profile": {Params: []string{"profile_id"}, Example: `{"profile_id":"development"}`},
		"deck.notify": {Params: []string{"message", "level"},
			Example: `{"message":"hello","level":"info"}`},
		"delay": {Params: []string{"ms"}, Example: `{"ms":200}`},
		"macro": {Params: []string{"steps", "stop_on_error", "step_timeout_ms"},
			Example: `{"steps":[{"type":"keyboard.key","params":{"key":"ENTER"}}]}`},
		"noop": {},
	}

	out := make([]actionDoc, 0, len(docs))
	for _, info := range s.engine.Registry().Descriptions() {
		d := docs[info.Type]
		d.Type = info.Type
		d.Scope = info.Scope
		if d.Params == nil {
			d.Params = []string{}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })

	writeJSON(w, http.StatusOK, map[string]any{
		"actions":          out,
		"scopes":           auth.AllScopes(),
		"high_risk_scopes": auth.HighRiskScopes(),
	})
}

// adminPlugins reports the plugin directory. The runtime is Phase 3; the
// endpoint exists so the GUI can show the folder and explain the state rather
// than hiding a feature that does not exist yet.
func (s *Server) adminPlugins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"implemented": false,
		"phase":       3,
		"note":        "the plugin runtime is Phase 3; see docs/PLUGIN_DEVELOPMENT.md for the frozen interface",
		"dir":         "",
		"plugins":     []any{},
	})
}

// --- helpers -------------------------------------------------------------

func (s *Server) loadProfileDoc(id string) (*profile.Profile, error) {
	entry, ok := s.profiles.Entry(id)
	if !ok {
		return nil, fmt.Errorf("no profile %q on this host", id)
	}
	// A deep copy, so a failed validation cannot leave the live registry holding
	// a half-edited document.
	raw, err := json.Marshal(entry.Doc)
	if err != nil {
		return nil, err
	}
	var doc profile.Profile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *Server) saveProfileDoc(w http.ResponseWriter, id string, doc *profile.Profile) {
	if err := s.profiles.Save(id, doc); err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			code = http.StatusConflict
		}
		writeError(w, code, "invalid_profile", err.Error())
		return
	}
	s.log.Info("profile edited", "profile", id, "by", "gui")
	s.broadcastProfileChanged(id, s.revisionOf(id))
	raw, err := s.profiles.Export(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) revisionOf(id string) string {
	if e, ok := s.profiles.Entry(id); ok {
		return e.Revision
	}
	return ""
}

// engine is referenced for its registry, which the action list is built from.
var _ = engine.ActionInfo{}
