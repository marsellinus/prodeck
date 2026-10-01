// Package profile implements the profile document model: the layout, pages,
// buttons and actions that a client renders (docs/PROTOCOL.md §5).
//
// The package is deliberately I/O-free apart from Load/Save helpers that take a
// path. Validation is strict and total: a profile either loads completely or is
// rejected with a JSON pointer naming the offending field. That rule is what
// makes hand-editing a profile safe.
package profile

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Schema is the profile document schema version.
const Schema = 1

// MaxGridColumns / MaxGridRows bound a grid so a malformed profile cannot make
// a client allocate an unbounded layout.
const (
	MaxGridColumns = 16
	MaxGridRows    = 16
	MaxPages       = 128
	MaxButtons     = 512
	MaxMacroDepth  = 8
)

// Profile is one complete deck layout.
type Profile struct {
	Schema     int               `json:"schema"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Icon       Icon              `json:"icon"`
	Theme      Theme             `json:"theme"`
	Settings   Settings          `json:"settings"`
	RootPage   string            `json:"root_page"`
	Pages      []Page            `json:"pages"`
	Meta       map[string]string `json:"meta,omitempty"`
	Dir        string            `json:"-"` // on-disk directory, filled by the loader
	Revision   string            `json:"-"` // content hash, filled by the loader
	SourcePath string            `json:"-"`
}

// Page is one screen of buttons.
type Page struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Icon    *Icon    `json:"icon,omitempty"`
	Grid    *Grid    `json:"grid,omitempty"` // nil inherits the profile grid
	Buttons []Button `json:"buttons"`
	Parent  string   `json:"parent,omitempty"` // for breadcrumbs; informational
}

// Grid is the button matrix size. A nil grid means "inherit".
type Grid struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// Button is one cell of a page.
type Button struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Sublabel     string          `json:"sublabel,omitempty"`
	Icon         Icon            `json:"icon"`
	Background   string          `json:"background,omitempty"`
	Foreground   string          `json:"foreground,omitempty"`
	Cell         Cell            `json:"cell"`
	State        State           `json:"state"`
	OnPress      *Action         `json:"on_press"`
	OnLongPress  *Action         `json:"on_long_press,omitempty"`
	OnRelease    *Action         `json:"on_release,omitempty"`
	OnHold       *Action         `json:"on_hold,omitempty"` // repeats while held
	HoldRepeatMS int             `json:"hold_repeat_ms,omitempty"`
	Permissions  []string        `json:"permissions,omitempty"` // scopes this button needs
	Hidden       bool            `json:"hidden,omitempty"`
	Extra        json.RawMessage `json:"-"` // reserved for future plugin fields
}

// Cell places a button in the grid. Defaults to 1x1 at 0,0 if unset.
type Cell struct {
	Row        int `json:"row"`
	Column     int `json:"column"`
	RowSpan    int `json:"row_span"`
	ColumnSpan int `json:"column_span"`
}

// Icon describes how to draw a button or profile icon. The Type field selects
// which of the other fields is meaningful, so an unknown type is a hard error
// rather than a silently blank tile.
type Icon struct {
	Type  string `json:"type"`            // emoji | material | lucide | image | text
	Value string `json:"value,omitempty"` // emoji char, icon name, or file name
	Color string `json:"color,omitempty"`
}

// State describes how a button's tile behaves over time (PROTOCOL.md §5.2).
type State struct {
	Type    string  `json:"type"`
	Group   string  `json:"group,omitempty"`   // radio group id
	Metric  string  `json:"metric,omitempty"`  // telemetry metric for state.type=telemetry
	Format  string  `json:"format,omitempty"`  // e.g. "{value:.0f}%"
	Default string  `json:"default,omitempty"` // text before the first update
	Min     float64 `json:"min,omitempty"`
	Max     float64 `json:"max,omitempty"`
	Color   string  `json:"color,omitempty"`
}

// StateTypes is the closed set of state kinds this schema version defines.
var StateTypes = map[string]bool{
	"momentary": true, "toggle": true, "radio": true, "status": true,
	"progress": true, "counter": true, "timer": true, "telemetry": true,
}

// Theme is the visual configuration for a profile.
type Theme struct {
	Background       string  `json:"background,omitempty"`
	Accent           string  `json:"accent,omitempty"`
	ButtonBackground string  `json:"button_background,omitempty"`
	ButtonForeground string  `json:"button_foreground,omitempty"`
	ButtonRadius     int     `json:"button_radius,omitempty"`
	FontScale        float64 `json:"font_scale,omitempty"`
	Spacing          int     `json:"spacing,omitempty"`
	Animation        string  `json:"animation,omitempty"`
	Mode             string  `json:"mode,omitempty"` // dark | light | oled | custom
}

// Settings are deck behaviour toggles.
type Settings struct {
	Grid       Grid        `json:"grid"`
	Haptic     bool        `json:"haptic"`
	Nav        Nav         `json:"nav"`
	AutoReturn *AutoReturn `json:"auto_return,omitempty"`
}

// Nav configures the navigation chrome.
type Nav struct {
	Breadcrumb bool `json:"breadcrumb"`
	BackButton bool `json:"back_button"`
	PageTabs   bool `json:"page_tabs"`
}

// AutoReturn pops back to the root page after an action runs, like Stream Deck's
// folder behaviour. Off by default because it surprises people.
type AutoReturn struct {
	Enabled bool `json:"enabled"`
	DelayMS int  `json:"delay_ms,omitempty"`
}

// Action is one executable step. Type is a dotted, namespaced identifier; Params
// is validated by the action implementation, not by this package.
type Action struct {
	Type                string          `json:"type"`
	Params              json.RawMessage `json:"params,omitempty"`
	OnError             string          `json:"on_error,omitempty"` // abort | continue
	Label               string          `json:"label,omitempty"`    // for editors and macro steps
	RequireConfirmation bool            `json:"require_confirmation,omitempty"`
}

// ActionTypeSet reports whether an action type may be used. The engine injects
// this so the profile package does not depend on the engine, and so a profile
// referencing an unknown or uninstalled plugin action is rejected at load time
// rather than at press time.
type ActionTypeSet func(string) bool

// --- Loading -------------------------------------------------------------

// Load parses and validates a profile document.
func Load(raw []byte, actionTypes ActionTypeSet) (*Profile, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // strict: a typo is an error, not a silent no-op
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profile: parse: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("profile: trailing content after the profile object")
	}
	if err := p.Validate(actionTypes); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the whole document and reports the first problem it finds,
// naming the JSON pointer of the offending field.
func (p *Profile) Validate(actionTypes ActionTypeSet) error {
	if p.Schema != Schema {
		return fmt.Errorf("profile: schema %d is not supported (want %d)", p.Schema, Schema)
	}
	if err := validID("id", p.ID); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("profile: /name must not be empty")
	}
	if err := p.Icon.validate("/icon"); err != nil {
		return err
	}
	if err := p.Theme.validate(); err != nil {
		return err
	}
	if err := p.Settings.Grid.validate("/settings/grid"); err != nil {
		return err
	}
	if len(p.Pages) == 0 {
		return fmt.Errorf("profile: /pages must contain at least one page")
	}
	if len(p.Pages) > MaxPages {
		return fmt.Errorf("profile: /pages has %d pages, max is %d", len(p.Pages), MaxPages)
	}

	seenPages := make(map[string]bool, len(p.Pages))
	total := 0
	for i := range p.Pages {
		page := &p.Pages[i]
		ptr := fmt.Sprintf("/pages/%d", i)
		if err := validID("page id", page.ID); err != nil {
			return fmt.Errorf("profile: %s/id: %w", ptr, err)
		}
		if seenPages[page.ID] {
			return fmt.Errorf("profile: %s/id: duplicate page id %q", ptr, page.ID)
		}
		seenPages[page.ID] = true
		if strings.TrimSpace(page.Name) == "" {
			return fmt.Errorf("profile: %s/name must not be empty", ptr)
		}
		if page.Grid != nil {
			if err := page.Grid.validate(ptr + "/grid"); err != nil {
				return err
			}
		}
		grid := p.gridFor(page)
		total += len(page.Buttons)
		if len(page.Buttons) > MaxButtons {
			return fmt.Errorf("profile: %s/buttons has %d buttons, max is %d", ptr, len(page.Buttons), MaxButtons)
		}
		if err := page.validateButtons(p, grid, ptr, actionTypes); err != nil {
			return err
		}
	}
	if total == 0 {
		return fmt.Errorf("profile: no buttons are defined in any page")
	}

	if p.RootPage == "" {
		p.RootPage = p.Pages[0].ID
	} else if !seenPages[p.RootPage] {
		return fmt.Errorf("profile: /root_page %q does not exist", p.RootPage)
	}
	// Parent links are informational but a dangling one breaks breadcrumbs.
	for i := range p.Pages {
		if par := p.Pages[i].Parent; par != "" && !seenPages[par] {
			return fmt.Errorf("profile: /pages/%d/parent %q does not exist", i, par)
		}
	}
	return nil
}

func (page *Page) validateButtons(p *Profile, grid Grid, pagePtr string, actionTypes ActionTypeSet) error {
	seen := make(map[string]bool, len(page.Buttons))
	occupied := make(map[[2]int]string, len(page.Buttons))
	for i := range page.Buttons {
		b := &page.Buttons[i]
		ptr := fmt.Sprintf("%s/buttons/%d", pagePtr, i)
		if err := validID("button id", b.ID); err != nil {
			return fmt.Errorf("profile: %s/id: %w", ptr, err)
		}
		if seen[b.ID] {
			return fmt.Errorf("profile: %s/id: duplicate button id %q on page %q", ptr, b.ID, page.ID)
		}
		seen[b.ID] = true
		if err := b.Icon.validate(ptr + "/icon"); err != nil {
			return err
		}
		if err := b.State.validate(ptr + "/state"); err != nil {
			return err
		}
		if err := b.Cell.validate(grid, ptr+"/cell"); err != nil {
			return err
		}
		for _, kind := range []struct {
			name string
			a    *Action
		}{{"on_press", b.OnPress}, {"on_long_press", b.OnLongPress}, {"on_release", b.OnRelease}, {"on_hold", b.OnHold}} {
			if kind.a == nil {
				continue
			}
			if err := kind.a.validate(ptr+"/"+kind.name, actionTypes, 0); err != nil {
				return err
			}
		}
		if b.OnHold != nil && b.HoldRepeatMS <= 0 {
			b.HoldRepeatMS = 150 // sane default rather than an error: harmless
		}
		if b.OnHold != nil && b.HoldRepeatMS < 30 {
			return fmt.Errorf("profile: %s/hold_repeat_ms: %d is below the 30 ms floor", ptr, b.HoldRepeatMS)
		}
		if b.OnPress == nil && b.OnLongPress == nil && b.OnHold == nil {
			return fmt.Errorf("profile: %s defines no action at all", ptr)
		}
		// Cell overlap is a layout bug that would render two tiles on top of
		// each other; refuse it rather than letting the client pick a winner.
		// The spans are already normalised by Cell.validate above.
		for r := b.Cell.Row; r < b.Cell.Row+b.Cell.RowSpan; r++ {
			for c := b.Cell.Column; c < b.Cell.Column+b.Cell.ColumnSpan; c++ {
				if other, ok := occupied[[2]int{r, c}]; ok {
					return fmt.Errorf("profile: %s/cell overlaps button %q at row %d column %d", ptr, other, r, c)
				}
				occupied[[2]int{r, c}] = b.ID
			}
		}
	}
	return nil
}

func (a *Action) validate(ptr string, actionTypes ActionTypeSet, depth int) error {
	if strings.TrimSpace(a.Type) == "" {
		return fmt.Errorf("profile: %s/type must not be empty", ptr)
	}
	if !validActionType(a.Type) {
		return fmt.Errorf("profile: %s/type %q is not a valid action type: it must be an identifier such as \"noop\" or a dotted namespace such as \"keyboard.shortcut\", using only letters, digits, '.', '-' and '_'", ptr, a.Type)
	}
	switch a.OnError {
	case "", "abort", "continue":
	default:
		return fmt.Errorf("profile: %s/on_error must be \"abort\" or \"continue\", got %q", ptr, a.OnError)
	}
	// Nested macros are validated recursively so a broken inner step is caught
	// at load time, not at press time.
	if a.Type == "macro" {
		if depth >= MaxMacroDepth {
			return fmt.Errorf("profile: %s: macro nesting exceeds %d levels", ptr, MaxMacroDepth)
		}
		var m struct {
			Steps []Action `json:"steps"`
		}
		if err := json.Unmarshal(orEmpty(a.Params), &m); err != nil {
			return fmt.Errorf("profile: %s/params: %w", ptr, err)
		}
		if len(m.Steps) == 0 {
			return fmt.Errorf("profile: %s/params/steps must not be empty", ptr)
		}
		for i := range m.Steps {
			if err := m.Steps[i].validate(fmt.Sprintf("%s/params/steps/%d", ptr, i), actionTypes, depth+1); err != nil {
				return err
			}
		}
	}
	if actionTypes != nil && !actionTypes(a.Type) {
		return fmt.Errorf("profile: %s/type %q is not provided by this host (missing action or uninstalled plugin)", ptr, a.Type)
	}
	return nil
}

func (ic Icon) validate(ptr string) error {
	switch ic.Type {
	case "emoji", "material", "lucide", "text", "image", "none", "":
	default:
		return fmt.Errorf("profile: %s/type %q is not one of emoji, material, lucide, text, image, none", ptr, ic.Type)
	}
	if ic.Type == "image" && ic.Value == "" {
		return fmt.Errorf("profile: %s/value must name an icon file when type is image", ptr)
	}
	if ic.Type != "" && ic.Type != "none" && ic.Type != "image" && ic.Value == "" {
		return fmt.Errorf("profile: %s/value must not be empty for type %q", ptr, ic.Type)
	}
	return nil
}

func (s State) validate(ptr string) error {
	if s.Type == "" {
		return nil // treated as momentary
	}
	if !StateTypes[s.Type] {
		keys := make([]string, 0, len(StateTypes))
		for k := range StateTypes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return fmt.Errorf("profile: %s/type %q is not one of %s", ptr, s.Type, strings.Join(keys, ", "))
	}
	if s.Type == "radio" && s.Group == "" {
		return fmt.Errorf("profile: %s/group is required for state type radio", ptr)
	}
	if s.Type == "telemetry" && s.Metric == "" {
		return fmt.Errorf("profile: %s/metric is required for state type telemetry", ptr)
	}
	return nil
}

func (g Grid) validate(ptr string) error {
	if g.Columns < 1 || g.Columns > MaxGridColumns {
		return fmt.Errorf("profile: %s/columns must be 1..%d, got %d", ptr, MaxGridColumns, g.Columns)
	}
	if g.Rows < 1 || g.Rows > MaxGridRows {
		return fmt.Errorf("profile: %s/rows must be 1..%d, got %d", ptr, MaxGridRows, g.Rows)
	}
	return nil
}

// validate checks the cell against its grid and normalises a missing span to 1.
//
// Normalising here rather than only in ApplyDefaults is what makes the overlap
// check in validateButtons work: a zero span would make that loop iterate over
// an empty range and silently skip the check.
func (c *Cell) validate(grid Grid, ptr string) error {
	if c.Row < 0 || c.Row >= grid.Rows {
		return fmt.Errorf("profile: %s/row %d is outside the %dx%d grid", ptr, c.Row, grid.Columns, grid.Rows)
	}
	if c.Column < 0 || c.Column >= grid.Columns {
		return fmt.Errorf("profile: %s/column %d is outside the %dx%d grid", ptr, c.Column, grid.Columns, grid.Rows)
	}
	rs, cs := c.RowSpan, c.ColumnSpan
	if rs == 0 {
		rs = 1
	}
	if cs == 0 {
		cs = 1
	}
	c.RowSpan, c.ColumnSpan = rs, cs
	if rs < 1 || c.Row+rs > grid.Rows {
		return fmt.Errorf("profile: %s/row_span %d exceeds the grid height %d", ptr, rs, grid.Rows)
	}
	if cs < 1 || c.Column+cs > grid.Columns {
		return fmt.Errorf("profile: %s/column_span %d exceeds the grid width %d", ptr, cs, grid.Columns)
	}
	return nil
}

func (t Theme) validate() error {
	switch t.Mode {
	case "", "dark", "light", "oled", "custom":
	default:
		return fmt.Errorf("profile: /theme/mode %q is not one of dark, light, oled, custom", t.Mode)
	}
	if t.FontScale != 0 && (t.FontScale < 0.5 || t.FontScale > 3) {
		return fmt.Errorf("profile: /theme/font_scale %.2f must be 0.5..3.0", t.FontScale)
	}
	if t.ButtonRadius < 0 || t.ButtonRadius > 64 {
		return fmt.Errorf("profile: /theme/button_radius %d must be 0..64", t.ButtonRadius)
	}
	if t.Spacing < 0 || t.Spacing > 64 {
		return fmt.Errorf("profile: /theme/spacing %d must be 0..64", t.Spacing)
	}
	return nil
}

// gridFor resolves the effective grid for a page.
func (p *Profile) gridFor(page *Page) Grid {
	if page.Grid != nil {
		return *page.Grid
	}
	return p.Settings.Grid
}

// Page returns a page by id.
func (p *Profile) Page(id string) (*Page, bool) {
	for i := range p.Pages {
		if p.Pages[i].ID == id {
			return &p.Pages[i], true
		}
	}
	return nil, false
}

// Button returns a button on a page by id.
func (p *Profile) Button(pageID, buttonID string) (*Button, bool) {
	page, ok := p.Page(pageID)
	if !ok {
		return nil, false
	}
	for i := range page.Buttons {
		if page.Buttons[i].ID == buttonID {
			return &page.Buttons[i], true
		}
	}
	return nil, false
}

// PageIDs returns page ids in document order, for tab bars.
func (p *Profile) PageIDs() []string {
	out := make([]string, 0, len(p.Pages))
	for i := range p.Pages {
		out = append(out, p.Pages[i].ID)
	}
	return out
}

// validID restricts identifiers to a conservative set: they appear in JSON, in
// URLs, in log lines, and in file paths, so anything exotic is refused early.
func validID(what, id string) error {
	if id == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	if len(id) > 64 {
		return fmt.Errorf("%s is %d bytes, max is 64", what, len(id))
	}
	for i, r := range id {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("%s %q contains %q at index %d; allowed are letters, digits, '.', '-', '_'", what, id, r, i)
		}
	}
	if id[0] == '.' || id[0] == '-' {
		return fmt.Errorf("%s %q must not start with %q", what, id, id[0])
	}
	return nil
}

// validActionType checks the action type shape.
//
// Two forms are accepted, and both are in the catalogue (docs/PROTOCOL.md §10):
// a single identifier (`noop`, `macro`, `run_script`, `launch_application`) for
// core actions, and a dotted namespace (`keyboard.shortcut`, `obs.start_streaming`)
// for grouped and plugin actions. The shape check only rejects characters that
// would be ambiguous in a log line or a JSON pointer; whether a type is
// *provided* is a separate question, answered by the provider predicate.
func validActionType(t string) bool {
	if t == "" || len(t) > 96 {
		return false
	}
	parts := strings.Split(t, ".")
	for _, p := range parts {
		if p == "" {
			return false
		}
		for i, r := range p {
			ok := r == '-' || r == '_' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				return false
			}
			if i == 0 && (r == '-' || r == '_' || (r >= '0' && r <= '9')) {
				return false
			}
		}
	}
	return true
}

func orEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// ApplyDefaults fills optional fields so clients never have to. It is called
// after Validate, and its output is what the client actually receives.
func (p *Profile) ApplyDefaults() {
	if p.Settings.Grid.Columns == 0 {
		p.Settings.Grid = Grid{Columns: 4, Rows: 5}
	}
	if p.Settings.Nav == (Nav{}) {
		p.Settings.Nav = Nav{Breadcrumb: true, BackButton: true, PageTabs: true}
	}
	if p.Theme.Mode == "" {
		p.Theme.Mode = "dark"
	}
	if p.Theme.ButtonRadius == 0 {
		p.Theme.ButtonRadius = 14
	}
	if p.Theme.FontScale == 0 {
		p.Theme.FontScale = 1
	}
	if p.Theme.Spacing == 0 {
		p.Theme.Spacing = 8
	}
	if p.Theme.Background == "" {
		p.Theme.Background = "#0e0e12"
	}
	if p.Theme.ButtonBackground == "" {
		p.Theme.ButtonBackground = "#1c1c22"
	}
	if p.Theme.ButtonForeground == "" {
		p.Theme.ButtonForeground = "#f2f2f5"
	}
	if p.Theme.Accent == "" {
		p.Theme.Accent = "#4c8bf5"
	}
	if p.Theme.Animation == "" {
		p.Theme.Animation = "fade"
	}
	for i := range p.Pages {
		page := &p.Pages[i]
		if page.Icon == nil {
			continue
		}
	}
	for i := range p.Pages {
		for j := range p.Pages[i].Buttons {
			b := &p.Pages[i].Buttons[j]
			if b.State.Type == "" {
				b.State.Type = "momentary"
			}
			if b.Cell.RowSpan == 0 {
				b.Cell.RowSpan = 1
			}
			if b.Cell.ColumnSpan == 0 {
				b.Cell.ColumnSpan = 1
			}
			if b.Background == "" {
				b.Background = p.Theme.ButtonBackground
			}
			if b.Foreground == "" {
				b.Foreground = p.Theme.ButtonForeground
			}
			if b.Icon.Type == "" {
				b.Icon.Type = "none"
			}
		}
	}
}
