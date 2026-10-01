package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mobiledeck/mobiledeck/host/internal/profile"
)

// The profile-editing endpoints back the desktop layout editor. They are tested
// here rather than through the GUI because the guarantee that matters is the
// server's: a save either lands whole and valid, or it does not land at all.
// An editor that can write a broken profile is worse than no editor, since the
// deck then fails to load and the user cannot tell which edit did it.

// adminDo performs an admin request against the test server.
func adminDo(t *testing.T, ts *testServer, method, path, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.url+path, r)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+ts.auth.AdminToken())
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestAdminActionsListsTheRegistry checks that the editor's action picker is fed
// from the host's own registry rather than a hardcoded list that can drift.
func TestAdminActionsListsTheRegistry(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/actions", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, raw)
	}

	var out struct {
		Actions []struct {
			Type    string   `json:"type"`
			Scope   string   `json:"scope"`
			Params  []string `json:"params"`
			Example string   `json:"example"`
		} `json:"actions"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// Every registered action must be listed, with its scope.
	registered := ts.engine.Registry().Descriptions()
	if len(out.Actions) != len(registered) {
		t.Errorf("listed %d actions, the registry has %d", len(out.Actions), len(registered))
	}
	byType := map[string]string{}
	for _, a := range out.Actions {
		byType[a.Type] = a.Scope
	}
	for _, info := range registered {
		if byType[info.Type] != info.Scope {
			t.Errorf("%s: scope %q, want %q", info.Type, byType[info.Type], info.Scope)
		}
	}
	// The risky actions must carry their scope here too: the editor shows it next
	// to the action, and that is how a user learns a button can run a command.
	if byType["run_command"] != "scripts" {
		t.Errorf("run_command scope = %q, want scripts", byType["run_command"])
	}
	if byType["system.shutdown"] != "system.power" {
		t.Errorf("system.shutdown scope = %q, want system.power", byType["system.shutdown"])
	}
	if len(out.Scopes) == 0 {
		t.Error("the scope list is empty")
	}
}

// TestAdminProfileDocumentRoundTrip covers reading and replacing a document.
func TestAdminProfileDocumentRoundTrip(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
	if code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", code, raw)
	}
	var doc profile.Profile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	if doc.ID != "development" {
		t.Fatalf("id = %q", doc.ID)
	}
	before := len(doc.Pages[0].Buttons)

	// Add a button through the whole-document path.
	doc.Pages[0].Buttons = append(doc.Pages[0].Buttons, profile.Button{
		ID:    "added-by-test",
		Label: "Added",
		Icon:  profile.Icon{Type: "emoji", Value: "x"},
		Cell:  profile.Cell{Row: 3, Column: 3},
		State: profile.State{Type: "momentary"},
		OnPress: &profile.Action{
			Type: "noop", Params: json.RawMessage("{}"),
		},
	})
	body, _ := json.Marshal(doc)

	code, raw = adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(body))
	if code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", code, raw)
	}

	var after profile.Profile
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatalf("decoding the reply: %v", err)
	}
	if got := len(after.Pages[0].Buttons); got != before+1 {
		t.Fatalf("buttons = %d, want %d", got, before+1)
	}

	// It must be on disk, not only in memory: a restart has to keep it.
	entry, ok := ts.prof.Entry("development")
	if !ok {
		t.Fatal("the profile vanished from the registry")
	}
	if _, ok := entry.Doc.Button(entry.Doc.Pages[0].ID, "added-by-test"); !ok {
		t.Error("the new button is not in the reloaded registry")
	}
}

// TestAdminProfileDocumentRejectsInvalid is the property that makes the editor
// safe: a broken edit is refused and the previous good profile keeps working.
func TestAdminProfileDocumentRejectsInvalid(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, raw)
	}
	var doc profile.Profile
	_ = json.Unmarshal(raw, &doc)
	goodButtons := len(doc.Pages[0].Buttons)

	cases := []struct {
		name    string
		mutate  func(*profile.Profile)
		wantSub string
	}{
		{
			name: "overlapping cell",
			mutate: func(p *profile.Profile) {
				p.Pages[0].Buttons = append(p.Pages[0].Buttons, profile.Button{
					ID: "overlap", Label: "X", Icon: profile.Icon{Type: "emoji", Value: "x"},
					Cell:    profile.Cell{Row: 0, Column: 0},
					State:   profile.State{Type: "momentary"},
					OnPress: &profile.Action{Type: "noop", Params: json.RawMessage("{}")},
				})
			},
			wantSub: "overlaps",
		},
		{
			name: "unknown action",
			mutate: func(p *profile.Profile) {
				p.Pages[0].Buttons = append(p.Pages[0].Buttons, profile.Button{
					ID: "ghost", Label: "X", Icon: profile.Icon{Type: "emoji", Value: "x"},
					Cell:    profile.Cell{Row: 2, Column: 2},
					State:   profile.State{Type: "momentary"},
					OnPress: &profile.Action{Type: "obs.start_streaming", Params: json.RawMessage("{}")},
				})
			},
			wantSub: "not provided by this host",
		},
		{
			name: "cell outside the grid",
			mutate: func(p *profile.Profile) {
				p.Pages[0].Buttons[0].Cell = profile.Cell{Row: 40, Column: 0}
			},
			wantSub: "outside the",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Re-read each time: a refused save must not have changed anything.
			code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles/development/document", "")
			if code != http.StatusOK {
				t.Fatalf("GET: %d %s", code, raw)
			}
			var p profile.Profile
			_ = json.Unmarshal(raw, &p)
			tc.mutate(&p)
			body, _ := json.Marshal(p)

			code, raw = adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", string(body))
			if code != http.StatusBadRequest {
				t.Fatalf("an invalid document was accepted: %d %s", code, raw)
			}
			var e struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(raw, &e)
			if e.Error != "invalid_profile" {
				t.Errorf("error = %q, want invalid_profile (body: %s)", e.Error, raw)
			}
			if !strings.Contains(e.Message, tc.wantSub) {
				t.Errorf("message %q does not mention %q", e.Message, tc.wantSub)
			}

			// And the live profile must be untouched.
			entry, _ := ts.prof.Entry("development")
			if got := len(entry.Doc.Pages[0].Buttons); got != goodButtons {
				t.Errorf("the refused save changed the live profile: %d buttons, want %d", got, goodButtons)
			}
		})
	}
}

// TestAdminProfilePageUpsertAndDelete covers page-level editing.
func TestAdminProfilePageUpsertAndDelete(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	page := `{"id":"extra","name":"Extra","buttons":[
	  {"id":"b","label":"B","icon":{"type":"emoji","value":"b"},
	   "cell":{"row":0,"column":0},"state":{"type":"momentary"},
	   "on_press":{"type":"noop","params":{}}}]}`

	code, raw := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/pages/extra", page)
	if code != http.StatusOK {
		t.Fatalf("PUT page: %d %s", code, raw)
	}
	if !strings.Contains(string(raw), `"extra"`) {
		t.Fatalf("the new page is missing from the reply: %s", raw)
	}

	// The id in the body must match the path, or a typo silently creates a
	// second page.
	code, _ = adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/pages/extra",
		strings.Replace(page, `"id":"extra"`, `"id":"other"`, 1))
	if code != http.StatusBadRequest {
		t.Fatalf("a mismatched page id was accepted: %d", code)
	}

	// Deleting a non-root page works.
	code, raw = adminDo(t, ts, http.MethodDelete, "/api/v1/admin/profiles/development/pages/extra", "")
	if code != http.StatusOK {
		t.Fatalf("DELETE page: %d %s", code, raw)
	}
	if strings.Contains(string(raw), `"extra"`) {
		t.Error("the page survived deletion")
	}

	// The root page must not be deletable: a profile with no root has nothing to
	// render and the phone would show an empty deck with no way back.
	code, raw = adminDo(t, ts, http.MethodDelete, "/api/v1/admin/profiles/development/pages/home", "")
	if code != http.StatusConflict {
		t.Fatalf("deleting the root page returned %d, want 409: %s", code, raw)
	}
}

// TestAdminProfileButtonUpsertAndDelete covers button-level editing, which is
// what the editor's grid uses.
func TestAdminProfileButtonUpsertAndDelete(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	button := `{"id":"gui-btn","label":"From the panel",
	  "icon":{"type":"emoji","value":"g"},
	  "cell":{"row":3,"column":2},"state":{"type":"momentary"},
	  "on_press":{"type":"deck.notify","params":{"message":"hi","level":"info"}}}`

	code, raw := adminDo(t, ts, http.MethodPut,
		"/api/v1/admin/profiles/development/pages/home/buttons/gui-btn", button)
	if code != http.StatusOK {
		t.Fatalf("PUT button: %d %s", code, raw)
	}
	if !strings.Contains(string(raw), "gui-btn") {
		t.Fatalf("the button is missing from the reply: %s", raw)
	}

	// Replacing it must not duplicate it.
	replaced := strings.Replace(button, "From the panel", "Renamed", 1)
	code, raw = adminDo(t, ts, http.MethodPut,
		"/api/v1/admin/profiles/development/pages/home/buttons/gui-btn", replaced)
	if code != http.StatusOK {
		t.Fatalf("PUT button (replace): %d %s", code, raw)
	}
	if n := strings.Count(string(raw), `"gui-btn"`); n != 1 {
		t.Errorf("the button appears %d times after a replace, want 1", n)
	}
	if !strings.Contains(string(raw), "Renamed") {
		t.Error("the replacement did not take effect")
	}

	// A button on a page that does not exist is a 404, not a silent create.
	code, _ = adminDo(t, ts, http.MethodPut,
		"/api/v1/admin/profiles/development/pages/ghost/buttons/x", button)
	if code != http.StatusNotFound {
		t.Errorf("a button on a missing page returned %d, want 404", code)
	}

	// Delete.
	code, raw = adminDo(t, ts, http.MethodDelete,
		"/api/v1/admin/profiles/development/pages/home/buttons/gui-btn", "")
	if code != http.StatusOK {
		t.Fatalf("DELETE button: %d %s", code, raw)
	}
	if strings.Contains(string(raw), "gui-btn") {
		t.Error("the button survived deletion")
	}
}

// TestAdminProfileEditRequiresTheToken checks that the editor endpoints are not
// a way around the admin gate.
func TestAdminProfileEditRequiresTheToken(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	for _, path := range []string{
		"/api/v1/admin/profiles/development/document",
		"/api/v1/admin/actions",
		"/api/v1/admin/profiles/development/pages/home/buttons/x",
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.url+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a token returned %d, want 401", path, resp.StatusCode)
		}
	}

	// And a write without a token must not change anything.
	before := len(ts.prof.IDs())
	req, _ := http.NewRequest(http.MethodPut, ts.url+"/api/v1/admin/profiles/development/document",
		bytes.NewReader([]byte(`{"schema":1,"id":"development"}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT without a token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated write returned %d, want 401", resp.StatusCode)
	}
	if len(ts.prof.IDs()) != before {
		t.Error("an unauthenticated write changed the profile list")
	}
}

// TestAdminPluginsReportsTheRealState checks that the endpoint does not pretend
// a Phase 3 feature exists.
func TestAdminPluginsReportsTheRealState(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/plugins", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var out struct {
		Implemented bool   `json:"implemented"`
		Phase       int    `json:"phase"`
		Note        string `json:"note"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.Implemented {
		t.Error("the plugin runtime is reported as implemented; it is Phase 3")
	}
	if out.Phase != 3 {
		t.Errorf("phase = %d, want 3", out.Phase)
	}
	if out.Note == "" {
		t.Error("no explanation was given")
	}
}

// TestAdminProfilesListReportsRejectedFiles covers the diagnostic path: a profile
// that fails validation must be reported, not hidden.
func TestAdminProfilesListReportsRejectedFiles(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})

	// Write a broken profile into the directory.
	bad := `{"schema":1,"id":"broken","name":"Broken","icon":{"type":"emoji","value":"b"},
	  "settings":{"grid":{"columns":3,"rows":3}},
	  "pages":[{"id":"home","name":"H","buttons":[
	    {"id":"a","label":"A","icon":{"type":"emoji","value":"a"},
	     "cell":{"row":0,"column":0},"state":{"type":"momentary"},
	     "on_press":{"type":"this.does.not.exist","params":{}}}]}]}`
	if err := writeProfile(t, ts.srv.cfg.ProfilesDir, "broken", bad); err != nil {
		t.Fatalf("writing the broken profile: %v", err)
	}
	if err := ts.prof.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	code, raw := adminDo(t, ts, http.MethodGet, "/api/v1/admin/profiles", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var out struct {
		Profiles []map[string]any `json:"profiles"`
		Errors   []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	var found bool
	for _, e := range out.Errors {
		if e.ID == "broken" && strings.Contains(e.Error, "not provided by this host") {
			found = true
		}
	}
	if !found {
		t.Errorf("the rejected profile was not reported: %s", raw)
	}
	// And it must not be served.
	for _, p := range out.Profiles {
		if p["id"] == "broken" {
			t.Error("a rejected profile is listed as loaded")
		}
	}
}

// TestAdminProfileCreateAndIdMismatch covers the editor's New profile path, and
// the guard that stops a document being written under the wrong id.
func TestAdminProfileCreateAndIdMismatch(t *testing.T) {
	ts := newTestServer(t, &recordingInput{})
	body := `{"schema":1,"id":"ghost","name":"Ghost","icon":{"type":"emoji","value":"g"},
	  "settings":{"grid":{"columns":2,"rows":2}},
	  "pages":[{"id":"home","name":"H","buttons":[
	    {"id":"a","label":"A","icon":{"type":"emoji","value":"a"},
	     "cell":{"row":0,"column":0},"state":{"type":"momentary"},
	     "on_press":{"type":"noop","params":{}}}]}]}`

	// Creating a new profile through the document endpoint is intended: it is
	// what the panel's "New profile" button does.
	code, raw := adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/ghost/document", body)
	if code != http.StatusOK {
		t.Fatalf("creating a profile failed: %d %s", code, raw)
	}
	if _, ok := ts.prof.Entry("ghost"); !ok {
		t.Error("the profile was not created")
	}

	// The document's id must match the path, or a typo would silently write to
	// the wrong profile.
	code, raw = adminDo(t, ts, http.MethodPut, "/api/v1/admin/profiles/development/document", body)
	if code != http.StatusBadRequest {
		t.Fatalf("a mismatched id returned %d, want 400: %s", code, raw)
	}
}

var _ = httptest.NewRequest
var _ = fmt.Sprint
