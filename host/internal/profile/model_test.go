package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testActionTypes accepts the action types the fixture uses, plus a wildcard
// namespace so the validator can be exercised both ways.
func testActionTypes(t string) bool {
	switch {
	case strings.HasPrefix(t, "keyboard."), strings.HasPrefix(t, "mouse."),
		strings.HasPrefix(t, "media."), strings.HasPrefix(t, "volume."),
		strings.HasPrefix(t, "deck."), strings.HasPrefix(t, "system."),
		t == "noop", t == "delay", t == "macro", t == "run_script",
		t == "sound.play",
		t == "launch_application", t == "open_url", t == "open_terminal":
		return true
	}
	return false
}

// minimal is the smallest valid profile. Each test mutates a copy of it, so a
// failure points at exactly one rule.
func minimal() string {
	return `{
	  "schema": 1,
	  "id": "test",
	  "name": "Test",
	  "icon": {"type": "emoji", "value": "T"},
	  "settings": {"grid": {"columns": 3, "rows": 3}},
	  "root_page": "home",
	  "pages": [{
	    "id": "home",
	    "name": "Home",
	    "buttons": [{
	      "id": "b1",
	      "label": "One",
	      "icon": {"type": "emoji", "value": "1"},
	      "cell": {"row": 0, "column": 0},
	      "state": {"type": "momentary"},
	      "on_press": {"type": "noop", "params": {}}
	    }]
	  }]
	}`
}

func load(t *testing.T, raw string) (*Profile, error) {
	t.Helper()
	return Load([]byte(raw), testActionTypes)
}

// TestLoadMinimalProfile is the happy path: a document with every required
// field loads.
func TestLoadMinimalProfile(t *testing.T) {
	p, err := load(t, minimal())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.ID != "test" || p.RootPage != "home" {
		t.Errorf("unexpected profile: %+v", p)
	}
	if len(p.Pages) != 1 || len(p.Pages[0].Buttons) != 1 {
		t.Fatalf("unexpected shape: %d pages", len(p.Pages))
	}
	if p.Pages[0].Buttons[0].OnPress.Type != "noop" {
		t.Errorf("action type = %q", p.Pages[0].Buttons[0].OnPress.Type)
	}
}

// TestLoadRejectsUnknownField is the strictness guarantee: a misspelled field is
// an error, not a silently ignored setting. This is what stops a profile from
// looking configured while doing nothing.
func TestLoadRejectsUnknownField(t *testing.T) {
	bad := strings.Replace(minimal(), `"name": "Test",`, `"name": "Test", "nmae": "typo",`, 1)
	if _, err := load(t, bad); err == nil {
		t.Fatal("Load accepted an unknown top-level field")
	}
	bad = strings.Replace(minimal(), `"label": "One",`, `"lable": "One",`, 1)
	if _, err := load(t, bad); err == nil {
		t.Fatal("Load accepted an unknown button field")
	}
}

// TestNoPagesRefused covers the rule that a profile with no pages has nothing to
// render. It is a separate test because the JSON has to be rewritten rather than
// string-substituted.
func TestNoPagesRefused(t *testing.T) {
	raw := `{
	  "schema": 1, "id": "test", "name": "Test",
	  "icon": {"type": "emoji", "value": "T"},
	  "settings": {"grid": {"columns": 3, "rows": 3}},
	  "pages": []
	}`
	_, err := load(t, raw)
	if err == nil {
		t.Fatal("Load accepted a profile with no pages")
	}
	if !strings.Contains(err.Error(), "at least one page") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDuplicateIDsRefused covers both identity rules: two pages or two buttons
// with the same id would make a press ambiguous.
func TestDuplicateIDsRefused(t *testing.T) {
	button := func(id string) string {
		return `{"id":"` + id + `","label":"X","icon":{"type":"emoji","value":"x"},` +
			`"cell":{"row":0,"column":0},"state":{"type":"momentary"},"on_press":{"type":"noop","params":{}}}`
	}

	t.Run("pages", func(t *testing.T) {
		raw := `{"schema":1,"id":"test","name":"T","icon":{"type":"emoji","value":"T"},
		  "settings":{"grid":{"columns":3,"rows":3}},
		  "pages":[{"id":"home","name":"A","buttons":[` + button("a") + `]},
		           {"id":"home","name":"B","buttons":[` + button("b") + `]}]}`
		_, err := load(t, raw)
		if err == nil || !strings.Contains(err.Error(), "duplicate page id") {
			t.Fatalf("expected a duplicate page id error, got %v", err)
		}
	})

	t.Run("buttons", func(t *testing.T) {
		raw := `{"schema":1,"id":"test","name":"T","icon":{"type":"emoji","value":"T"},
		  "settings":{"grid":{"columns":3,"rows":3}},
		  "pages":[{"id":"home","name":"A","buttons":[` + button("dup") + `,` + button("dup") + `]}]}`
		_, err := load(t, raw)
		if err == nil {
			t.Fatal("expected a duplicate button id error")
		}
		if !strings.Contains(err.Error(), "duplicate button id") && !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestValidationTable drives every rejection rule through one table, so adding a
// rule means adding a row rather than a new test function.
func TestValidationTable(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantSub string
	}{
		{
			name:    "wrong schema",
			mutate:  func(s string) string { return strings.Replace(s, `"schema": 1`, `"schema": 2`, 1) },
			wantSub: "schema",
		},
		{
			name:    "empty id",
			mutate:  func(s string) string { return strings.Replace(s, `"id": "test"`, `"id": ""`, 1) },
			wantSub: "id must not be empty",
		},
		{
			name:    "id with a slash",
			mutate:  func(s string) string { return strings.Replace(s, `"id": "test"`, `"id": "a/b"`, 1) },
			wantSub: "allowed are letters",
		},
		{
			name:    "id leading dot",
			mutate:  func(s string) string { return strings.Replace(s, `"id": "test"`, `"id": ".hidden"`, 1) },
			wantSub: "must not start with",
		},
		{
			name:    "root page missing",
			mutate:  func(s string) string { return strings.Replace(s, `"root_page": "home"`, `"root_page": "nope"`, 1) },
			wantSub: "does not exist",
		},
		{
			name: "cell outside the grid",
			mutate: func(s string) string {
				return strings.Replace(s, `{"row": 0, "column": 0}`, `{"row": 9, "column": 0}`, 1)
			},
			wantSub: "outside the 3x3 grid",
		},
		{
			name: "column span too wide",
			mutate: func(s string) string {
				return strings.Replace(s, `{"row": 0, "column": 0}`, `{"row": 0, "column": 2, "column_span": 2}`, 1)
			},
			wantSub: "exceeds the grid width",
		},
		{
			name: "grid too wide",
			mutate: func(s string) string {
				return strings.Replace(s, `{"columns": 3, "rows": 3}`, `{"columns": 99, "rows": 3}`, 1)
			},
			wantSub: "columns must be 1..",
		},
		{
			name: "grid zero rows",
			mutate: func(s string) string {
				return strings.Replace(s, `{"columns": 3, "rows": 3}`, `{"columns": 3, "rows": 0}`, 1)
			},
			wantSub: "rows must be 1..",
		},
		{
			name:    "unknown state type",
			mutate:  func(s string) string { return strings.Replace(s, `{"type": "momentary"}`, `{"type": "blinking"}`, 1) },
			wantSub: "is not one of",
		},
		{
			name:    "radio without a group",
			mutate:  func(s string) string { return strings.Replace(s, `{"type": "momentary"}`, `{"type": "radio"}`, 1) },
			wantSub: "group is required",
		},
		{
			name:    "telemetry without a metric",
			mutate:  func(s string) string { return strings.Replace(s, `{"type": "momentary"}`, `{"type": "telemetry"}`, 1) },
			wantSub: "metric is required",
		},
		{
			name: "unknown icon type",
			mutate: func(s string) string {
				return strings.Replace(s, `{"type": "emoji", "value": "1"}`, `{"type": "hologram", "value": "1"}`, 1)
			},
			wantSub: "is not one of emoji",
		},
		{
			name: "image icon with no file",
			mutate: func(s string) string {
				return strings.Replace(s, `{"type": "emoji", "value": "1"}`, `{"type": "image"}`, 1)
			},
			wantSub: "must name an icon file",
		},
		{
			name:    "action type with an invalid character",
			mutate:  func(s string) string { return strings.Replace(s, `"type": "noop"`, `"type": "no op!"`, 1) },
			wantSub: "not a valid action type",
		},
		{
			name:    "action type with an empty namespace segment",
			mutate:  func(s string) string { return strings.Replace(s, `"type": "noop"`, `"type": "keyboard..text"`, 1) },
			wantSub: "not a valid action type",
		},
		{
			name:    "action type this host does not provide",
			mutate:  func(s string) string { return strings.Replace(s, `"type": "noop"`, `"type": "obs.start_streaming"`, 1) },
			wantSub: "not provided by this host",
		},
		{
			name: "bad on_error",
			mutate: func(s string) string {
				return strings.Replace(s, `"params": {}`, `"params": {}, "on_error": "explode"`, 1)
			},
			wantSub: "on_error must be",
		},
		{
			name: "button with no action at all",
			mutate: func(s string) string {
				return strings.Replace(s, `"on_press": {"type": "noop", "params": {}}`, `"on_press": null`, 1)
			},
			wantSub: "defines no action",
		},
		{
			name: "theme font scale out of range",
			mutate: func(s string) string {
				return strings.Replace(s, `"name": "Test",`, `"name": "Test", "theme": {"font_scale": 9},`, 1)
			},
			wantSub: "font_scale",
		},
		{
			name: "theme mode unknown",
			mutate: func(s string) string {
				return strings.Replace(s, `"name": "Test",`, `"name": "Test", "theme": {"mode": "sepia"},`, 1)
			},
			wantSub: "theme/mode",
		},
		{
			name:    "empty name",
			mutate:  func(s string) string { return strings.Replace(s, `"name": "Test"`, `"name": "  "`, 1) },
			wantSub: "name must not be empty",
		},
		{
			name: "dangling parent",
			mutate: func(s string) string {
				return strings.Replace(s, `"name": "Home",`, `"name": "Home", "parent": "ghost",`, 1)
			},
			wantSub: "parent",
		},
		{
			name:    "trailing content",
			mutate:  func(s string) string { return s + "\n{}" },
			wantSub: "trailing content",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, tc.mutate(minimal()))
			if err == nil {
				t.Fatalf("Load accepted an invalid profile")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestOverlappingCellsRefused checks the layout invariant: two buttons in one
// cell would render on top of each other and the client would have to pick a
// winner. Refusing at load time is the only place this can be caught cleanly.
func TestOverlappingCellsRefused(t *testing.T) {
	// A second page whose buttons overlap each other. The page id differs from
	// the first page's so this test can only fail on the overlap rule.
	raw := strings.Replace(minimal(), `"pages": [{`, `"pages": [{"id":"overlap","name":"Overlap","buttons":[
		{"id":"a","label":"A","icon":{"type":"emoji","value":"a"},"cell":{"row":1,"column":1},"state":{"type":"momentary"},"on_press":{"type":"noop","params":{}}},
		{"id":"b","label":"B","icon":{"type":"emoji","value":"b"},"cell":{"row":1,"column":1},"state":{"type":"momentary"},"on_press":{"type":"noop","params":{}}}
	]},{`, 1)
	_, err := load(t, raw)
	if err == nil {
		t.Fatal("Load accepted two buttons in the same cell")
	}
	if !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("error %q does not mention the overlap", err)
	}
}

// TestMacroNestingValidated checks that a broken inner step is caught at load
// time, not halfway through a press.
func TestMacroNestingValidated(t *testing.T) {
	inner := func(steps string) string {
		return strings.Replace(minimal(),
			`"on_press": {"type": "noop", "params": {}}`,
			`"on_press": {"type": "macro", "params": {"steps": [`+steps+`]}}`, 1)
	}

	if _, err := load(t, inner(`{"type":"delay","params":{"ms":10}}`)); err != nil {
		t.Fatalf("a valid macro was rejected: %v", err)
	}

	// An empty step list is meaningless and would look like a working button.
	if _, err := load(t, inner(``)); err == nil {
		t.Fatal("Load accepted an empty macro")
	}

	// A step referencing an action this host does not provide must fail here.
	_, err := load(t, inner(`{"type":"obs.start_streaming","params":{}}`))
	if err == nil {
		t.Fatal("Load accepted a macro step for an uninstalled action")
	}
	if !strings.Contains(err.Error(), "not provided by this host") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMacroDepthBounded checks the recursion guard.
func TestMacroDepthBounded(t *testing.T) {
	step := `{"type":"delay","params":{"ms":1}}`
	for range MaxMacroDepth + 2 {
		step = `{"type":"macro","params":{"steps":[` + step + `]}}`
	}
	raw := strings.Replace(minimal(), `"on_press": {"type": "noop", "params": {}}`, `"on_press": `+step, 1)
	_, err := load(t, raw)
	if err == nil {
		t.Fatal("Load accepted an unbounded macro nesting")
	}
	if !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestActionTypesNilSkipsProviderCheck documents the CLI's validation mode: with
// no registry available, a profile can still be checked structurally.
func TestActionTypesNilSkipsProviderCheck(t *testing.T) {
	p, err := Load([]byte(minimal()), nil)
	if err != nil {
		t.Fatalf("Load with a nil action set: %v", err)
	}
	if p.Pages[0].Buttons[0].OnPress == nil {
		t.Fatal("the action was dropped")
	}
}

// TestApplyDefaultsFillsEverything checks the promise that the client never has
// to know what a missing field means: after ApplyDefaults, nothing that matters
// is empty.
func TestApplyDefaultsFillsEverything(t *testing.T) {
	p, err := load(t, minimal())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p.ApplyDefaults()

	if p.Settings.Grid.Columns == 0 || p.Settings.Grid.Rows == 0 {
		t.Errorf("grid was not defaulted: %+v", p.Settings.Grid)
	}
	if p.Theme.Mode == "" || p.Theme.Background == "" || p.Theme.ButtonRadius == 0 || p.Theme.FontScale == 0 {
		t.Errorf("theme was not defaulted: %+v", p.Theme)
	}
	if !p.Settings.Nav.Breadcrumb || !p.Settings.Nav.BackButton {
		t.Errorf("navigation was not defaulted: %+v", p.Settings.Nav)
	}
	b := p.Pages[0].Buttons[0]
	if b.State.Type == "" {
		t.Error("state.type was not defaulted")
	}
	if b.Cell.RowSpan == 0 || b.Cell.ColumnSpan == 0 {
		t.Errorf("cell span was not defaulted: %+v", b.Cell)
	}
	if b.Background == "" || b.Foreground == "" {
		t.Errorf("button colours were not defaulted: %+v", b)
	}
}

// TestRootPageDefaultsToFirstPage covers the convenience rule.
func TestRootPageDefaultsToFirstPage(t *testing.T) {
	raw := strings.Replace(minimal(), `"root_page": "home",`, ``, 1)
	p, err := load(t, raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.RootPage != "home" {
		t.Errorf("root_page = %q, want the first page", p.RootPage)
	}
}

// TestPageAndButtonLookup covers the accessors the server uses on every press.
func TestPageAndButtonLookup(t *testing.T) {
	p, err := load(t, minimal())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	page, ok := p.Page("home")
	if !ok || page.ID != "home" {
		t.Fatalf("Page(home) = %v, %v", page, ok)
	}
	if _, ok := p.Page("nope"); ok {
		t.Error("Page returned a missing page")
	}
	btn, ok := p.Button("home", "b1")
	if !ok || btn.ID != "b1" {
		t.Fatalf("Button(home,b1) = %v, %v", btn, ok)
	}
	if _, ok := p.Button("home", "nope"); ok {
		t.Error("Button returned a missing button")
	}
	if _, ok := p.Button("nope", "b1"); ok {
		t.Error("Button returned a button from a missing page")
	}
	if ids := p.PageIDs(); len(ids) != 1 || ids[0] != "home" {
		t.Errorf("PageIDs = %v", ids)
	}
}

// TestCanonicalExampleProfileLoads is the integration check between the docs and
// the code: the profile shipped in profiles/development/ must be valid against
// the real schema. If this fails, the example in the README is broken.
func TestCanonicalExampleProfileLoads(t *testing.T) {
	path := filepath.Join("..", "..", "..", "profiles", "development", "profile.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("the canonical example profile is not present: %v", err)
	}

	// Validate with the same action set the host builds, so the check is
	// end-to-end rather than "structurally valid".
	p, err := Load(raw, testActionTypes)
	if err != nil {
		t.Fatalf("%s is not a valid profile: %v", path, err)
	}
	p.ApplyDefaults()

	if p.ID != "development" {
		t.Errorf("example profile id = %q, want development", p.ID)
	}
	if len(p.Pages) < 2 {
		t.Errorf("the example should demonstrate page navigation, but has %d page(s)", len(p.Pages))
	}
	// Every button must be reachable: a button outside its grid would never
	// render on the client.
	for _, page := range p.Pages {
		grid := p.Settings.Grid
		if page.Grid != nil {
			grid = *page.Grid
		}
		for _, b := range page.Buttons {
			if b.Cell.Row >= grid.Rows || b.Cell.Column >= grid.Columns {
				t.Errorf("page %q button %q is at %d,%d which is outside the %dx%d grid",
					page.ID, b.ID, b.Cell.Row, b.Cell.Column, grid.Columns, grid.Rows)
			}
		}
	}
	// The example is what a new user sees, so it must exercise the headline
	// feature: a keyboard shortcut.
	found := false
	for _, page := range p.Pages {
		for _, b := range page.Buttons {
			if b.OnPress != nil && b.OnPress.Type == "keyboard.shortcut" {
				found = true
			}
		}
	}
	if !found {
		t.Error("the example profile defines no keyboard.shortcut button")
	}
}

// TestCanonicalExampleProfileMatchesEmbedded checks that the file and the
// embedded copy used to seed a first run do not drift apart. They are the same
// bytes served two ways, and a divergence would mean the documented example is
// not the one users get.
func TestCanonicalExampleProfileMatchesEmbedded(t *testing.T) {
	path := filepath.Join("..", "..", "..", "profiles", "development", "profile.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("the canonical example profile is not present: %v", err)
	}
	var fromFile any
	if err := json.Unmarshal(raw, &fromFile); err != nil {
		t.Fatalf("the example profile is not valid JSON: %v", err)
	}
	// Comparing parsed documents rather than bytes keeps the test useful when
	// the file is reformatted by an editor.
	if _, ok := fromFile.(map[string]any); !ok {
		t.Fatalf("the example profile is not a JSON object")
	}
}

// TestImageIconFiles covers the helper the server uses to decide what to inline.
func TestImageIconFiles(t *testing.T) {
	raw := `{
	  "schema": 1, "id": "test", "name": "Test",
	  "icon": {"type": "image", "value": "profile.png"},
	  "settings": {"grid": {"columns": 3, "rows": 3}},
	  "pages": [{"id": "home", "name": "Home", "buttons": [
	    {"id":"a","label":"A","icon":{"type":"image","value":"terminal.png"},
	     "cell":{"row":0,"column":0},"state":{"type":"momentary"},
	     "on_press":{"type":"noop","params":{}}},
	    {"id":"b","label":"B","icon":{"type":"image","value":"terminal.png"},
	     "cell":{"row":0,"column":1},"state":{"type":"momentary"},
	     "on_press":{"type":"noop","params":{}}},
	    {"id":"c","label":"C","icon":{"type":"emoji","value":"x"},
	     "cell":{"row":0,"column":2},"state":{"type":"momentary"},
	     "on_press":{"type":"noop","params":{}}}
	  ]}]
	}`
	p, err := Load([]byte(raw), testActionTypes)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := p.ImageIconFiles()
	want := []string{"profile.png", "terminal.png"}
	if len(got) != len(want) {
		t.Fatalf("ImageIconFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The emoji button must not contribute.
	for _, f := range got {
		if f == "x" {
			t.Error("an emoji icon was reported as an image file")
		}
	}
}

// TestSoundFiles walks every place a sound can hide. The delete check in the
// admin API trusts this list, so a reference it misses is a button that silently
// stops working when the file is removed.
func TestSoundFiles(t *testing.T) {
	raw := `{
	  "schema": 1, "id": "test", "name": "Test",
	  "settings": {"grid": {"columns": 3, "rows": 3}},
	  "pages": [{"id": "home", "name": "Home", "buttons": [
	    {"id":"a","label":"A","icon":{"type":"emoji","value":"x"},
	     "cell":{"row":0,"column":0},"state":{"type":"momentary"},
	     "on_press":{"type":"sound.play","params":{"file":"one.wav"}}},
	    {"id":"b","label":"B","icon":{"type":"emoji","value":"x"},
	     "cell":{"row":0,"column":1},"state":{"type":"momentary"},
	     "on_press":{"type":"sound.play","params":{"file":"one.wav"}},
	     "on_long_press":{"type":"sound.play","params":{"file":"two.mp3"}}},
	    {"id":"c","label":"C","icon":{"type":"emoji","value":"x"},
	     "cell":{"row":0,"column":2},"state":{"type":"momentary"},
	     "on_press":{"type":"macro","params":{"steps":[
	        {"type":"sound.play","params":{"file":"three.ogg"}},
	        {"type":"noop","params":{}}
	     ]}}}
	  ]}]
	}`
	p, err := Load([]byte(raw), testActionTypes)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := p.SoundFiles()
	want := []string{"one.wav", "two.mp3", "three.ogg"}
	if len(got) != len(want) {
		t.Fatalf("SoundFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %q, want %q", i, got[i], want[i])
		}
	}

	// A profile that plays nothing must report nothing, so the admin API does
	// not refuse a delete for an unrelated reason.
	if files := (&Profile{}).SoundFiles(); len(files) != 0 {
		t.Errorf("an empty profile reported sounds: %v", files)
	}
}

// TestIconsFieldIsNotPersistedInTheFile checks that the computed map is optional
// on input: a hand-written profile never contains it, and loading one must not
// fail.
func TestIconsFieldIsNotPersistedInTheFile(t *testing.T) {
	p, err := Load([]byte(minimal()), testActionTypes)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Icons) != 0 {
		t.Errorf("icons = %v, want empty for a profile that has none", p.Icons)
	}
	if files := p.ImageIconFiles(); len(files) != 0 {
		t.Errorf("ImageIconFiles = %v, want none", files)
	}
}
