package engine

import (
	"fmt"
	"sync"

	"github.com/mobiledeck/mobiledeck/host/internal/profile"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
)

// ButtonState is a client-visible override of a button's tile, pushed as
// `event.button.state` (docs/PROTOCOL.md §6.4).
type ButtonState struct {
	ProfileID string   `json:"profile_id"`
	PageID    string   `json:"page_id"`
	ButtonID  string   `json:"button_id"`
	Type      string   `json:"type"`
	Label     string   `json:"label,omitempty"`
	Color     string   `json:"color,omitempty"`
	Progress  *float64 `json:"progress,omitempty"`
	Value     *float64 `json:"value,omitempty"`
	Active    *bool    `json:"active,omitempty"`
	UpdatedMS int64    `json:"updated_ms"`
}

// stateKey identifies one button.
func stateKey(profileID, pageID, buttonID string) string {
	return profileID + "\x00" + pageID + "\x00" + buttonID
}

// SetButtonState records a new state for a button and emits it if it changed.
//
// Coalescing happens here: identical states are dropped, which is what keeps a
// counter button from flooding the socket when nothing is happening.
func (e *Engine) SetButtonState(st ButtonState) {
	key := stateKey(st.ProfileID, st.PageID, st.ButtonID)

	// The caller's pointers are copied, never retained. A caller that reuses one
	// variable across calls (a counter, a sampling loop) would otherwise mutate
	// the state already recorded, and the change detection below would compare a
	// value against itself and drop a real update.
	st = st.clone()

	e.mu.Lock()
	prev, exists := e.states[key]
	if exists && sameState(prev, st) {
		e.mu.Unlock()
		return
	}
	e.states[key] = st
	e.mu.Unlock()

	e.emit(Event{Type: protoEventButtonState, Payload: proto.EventButtonStatePayload{
		ProfileID: st.ProfileID,
		PageID:    st.PageID,
		ButtonID:  st.ButtonID,
		State: proto.StateVal{
			Type:     st.Type,
			Label:    st.Label,
			Color:    st.Color,
			Progress: st.Progress,
			Value:    st.Value,
			Active:   st.Active,
		},
	}})
}

// clone deep-copies the pointer fields so the engine owns its recorded state.
func (st ButtonState) clone() ButtonState {
	if st.Progress != nil {
		v := *st.Progress
		st.Progress = &v
	}
	if st.Value != nil {
		v := *st.Value
		st.Value = &v
	}
	if st.Active != nil {
		v := *st.Active
		st.Active = &v
	}
	return st
}

// sameState compares everything except the timestamp, so a state that only
// differs by when it was computed is not re-sent.
func sameState(a, b ButtonState) bool {
	if a.Type != b.Type || a.Label != b.Label || a.Color != b.Color {
		return false
	}
	if !eqPtr(a.Progress, b.Progress) || !eqPtr(a.Value, b.Value) || !eqBoolPtr(a.Active, b.Active) {
		return false
	}
	return true
}

func eqPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func eqBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ButtonStates returns every recorded state for a profile, so a client that
// reconnects or switches profile immediately sees current values instead of
// waiting for the next change.
func (e *Engine) ButtonStates(profileID string) []ButtonState {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ButtonState, 0, len(e.states))
	for _, st := range e.states {
		if profileID == "" || st.ProfileID == profileID {
			out = append(out, st.clone())
		}
	}
	return out
}

// ForgetProfile drops recorded states for a profile, called when the profile is
// reloaded: the old states refer to buttons that may no longer exist.
func (e *Engine) ForgetProfile(profileID string) {
	e.mu.Lock()
	for key, st := range e.states {
		if st.ProfileID == profileID {
			delete(e.states, key)
		}
	}
	e.mu.Unlock()
}

// toggleLatch remembers the current value of toggle/radio buttons so a
// subsequent press flips the right way even if the client reconnected and lost
// its local view.
type toggleLatch struct {
	mu     sync.Mutex
	values map[string]bool
}

func newToggleLatch() *toggleLatch {
	return &toggleLatch{values: make(map[string]bool)}
}

func (t *toggleLatch) flip(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.values[key] = !t.values[key]
	return t.values[key]
}

func (t *toggleLatch) get(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.values[key]
}

func (t *toggleLatch) set(key string, v bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.values[key] = v
}

// radioKey is the latch key for a radio group: the whole group shares one
// boolean, expressed as "which button id is on".
func radioKey(profileID, pageID, group string) string {
	return profileID + "\x00" + pageID + "\x00" + "radio:" + group
}

// ApplyButtonState performs the host-side half of the stateful-button
// behaviour: for toggle and radio the host is the authority, so the state
// survives a client restart.
func (e *Engine) ApplyButtonState(prof *profile.Profile, page *profile.Page, btn *profile.Button) {
	switch btn.State.Type {
	case "toggle":
		on := e.latches.flip(stateKey(prof.ID, page.ID, btn.ID))
		e.SetButtonState(ButtonState{
			ProfileID: prof.ID, PageID: page.ID, ButtonID: btn.ID,
			Type: "toggle", Active: &on, UpdatedMS: nowMS(),
		})
	case "radio":
		key := radioKey(prof.ID, page.ID, btn.State.Group)
		e.latches.set(key, true)
		for i := range page.Buttons {
			other := &page.Buttons[i]
			if other.State.Type != "radio" || other.State.Group != btn.State.Group {
				continue
			}
			active := other.ID == btn.ID
			e.SetButtonState(ButtonState{
				ProfileID: prof.ID, PageID: page.ID, ButtonID: other.ID,
				Type: "radio", Active: &active, UpdatedMS: nowMS(),
			})
		}
	}
}

// ValidateButtonStates checks the parts of a stateful button the profile
// validator cannot: that a telemetry binding names a metric this host actually
// produces. Without this, a profile reading gpu.usage on a machine with no GPU
// sampler would show "--" forever with no explanation.
func ValidateButtonStates(prof *profile.Profile, availableMetrics []string) error {
	if len(availableMetrics) == 0 {
		// No metrics at all is a host limitation, not a profile error.
		return nil
	}
	known := make(map[string]bool, len(availableMetrics))
	for _, m := range availableMetrics {
		known[m] = true
	}
	for i := range prof.Pages {
		page := &prof.Pages[i]
		for j := range page.Buttons {
			b := &page.Buttons[j]
			if b.State.Type == "telemetry" && !known[b.State.Metric] {
				return fmt.Errorf("profile: /pages/%d/buttons/%d/state/metric %q is not produced by this host; available metrics are %v",
					i, j, b.State.Metric, availableMetrics)
			}
		}
	}
	return nil
}
