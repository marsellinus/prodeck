package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/profile"
	"github.com/mobiledeck/mobiledeck/host/internal/profiles"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
)

// handleProfileList answers profile.list.
func (sess *Session) handleProfileList(env proto.Envelope) {
	s := sess.srv
	list := s.profiles.List()

	out := make([]proto.ProfileSummary, 0, len(list))
	for _, e := range list {
		out = append(out, proto.ProfileSummary{
			ID:       e.Doc.ID,
			Name:     e.Doc.Name,
			Icon:     e.Doc.Icon,
			Pages:    e.Doc.PageIDs(),
			Revision: e.Revision,
		})
	}

	sess.reply(env, proto.TypeProfileListResult, proto.ProfileListResultPayload{
		Profiles: out,
		Active:   sess.activeProfileLocked(),
	})
}

// handleProfileGet answers profile.get with the raw document.
func (sess *Session) handleProfileGet(env proto.Envelope) {
	s := sess.srv
	var req proto.ProfileGetPayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "profile.get payload is malformed: %v", err)
		return
	}

	entry, ok := s.profiles.Entry(req.ProfileID)
	if !ok {
		// A profile that failed validation is reported as invalid rather than
		// missing, so the operator sees why instead of chasing a phantom.
		if err, failed := s.profiles.Errors()[req.ProfileID]; failed {
			sess.fail(env, proto.CodeInvalidProfile, "profile %q failed validation: %v", req.ProfileID, err)
			return
		}
		sess.fail(env, proto.CodeNotFound, "no profile %q on this host; available: %v", req.ProfileID, s.profiles.IDs())
		return
	}

	// Resolve the image icons this document references into data URIs, so a
	// button with `"icon": {"type":"image"}` has something to draw. Only the
	// referenced files are inlined, and an icon that is not in the cache is
	// simply left out: the client draws its placeholder rather than the profile
	// failing to serve (docs/PROTOCOL.md §5).
	//
	// The document itself is copied first. Mutating entry.Doc would write the
	// resolved icons into the shared registry entry and, worse, into the next
	// save of that profile.
	served := *entry.Doc
	served.Icons = s.resolveIcons(&served)

	// Marshal from the document rather than sending the file bytes: the
	// defaults applied at load time must reach the client, so it never has to
	// know what a missing field means.
	raw, err := json.Marshal(&served)
	if err != nil {
		sess.fail(env, proto.CodeInternal, "could not encode profile %q", req.ProfileID)
		return
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		sess.fail(env, proto.CodeInternal, "could not normalise profile %q", req.ProfileID)
		return
	}

	sess.reply(env, proto.TypeProfileGetResult, proto.ProfileGetResultPayload{
		Profile:  doc,
		Revision: entry.Revision,
	})
}

// handleSetActiveProfile records the session's active profile and pushes the
// current button states so the client's tiles are correct immediately.
func (sess *Session) handleSetActiveProfile(env proto.Envelope) {
	s := sess.srv
	var req proto.ProfileSetActivePayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "profile.set_active payload is malformed: %v", err)
		return
	}
	if _, ok := s.profiles.Get(req.ProfileID); !ok {
		sess.fail(env, proto.CodeNotFound, "no profile %q on this host", req.ProfileID)
		return
	}

	sess.mu.Lock()
	sess.activeProfile = req.ProfileID
	sess.activePage = ""
	sess.mu.Unlock()

	sess.reply(env, proto.TypeProfileSetActive, map[string]any{
		"profile_id": req.ProfileID,
		"ok":         true,
	})
	sess.pushStates(req.ProfileID)
}

// handleProfileReload re-reads a profile from disk on demand.
func (sess *Session) handleProfileReload(env proto.Envelope) {
	s := sess.srv
	var req proto.ProfileReloadPayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "profile.reload payload is malformed: %v", err)
		return
	}
	entry, err := s.profiles.ReloadOne(req.ProfileID)
	if err != nil {
		sess.fail(env, proto.CodeInvalidProfile, "could not reload %q: %v", req.ProfileID, err)
		return
	}
	s.engine.ForgetProfile(req.ProfileID)

	sess.reply(env, proto.TypeProfileReload, map[string]any{
		"profile_id": entry.Doc.ID,
		"revision":   entry.Revision,
		"ok":         true,
	})
	sess.push(proto.TypeEventProfileChange, proto.EventProfileChangedPayload{
		ProfileID: entry.Doc.ID,
		Revision:  entry.Revision,
	})
}

// handleProfileExport returns a profile as a JSON string.
func (sess *Session) handleProfileExport(env proto.Envelope) {
	var req proto.ProfileGetPayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "profile.export payload is malformed: %v", err)
		return
	}
	raw, err := sess.srv.profiles.Export(req.ProfileID)
	if err != nil {
		sess.fail(env, proto.CodeNotFound, "could not export %q: %v", req.ProfileID, err)
		return
	}
	sess.reply(env, proto.TypeProfileExportRes, proto.ProfileExportResultPayload{
		ProfileID: req.ProfileID,
		JSON:      string(raw),
	})
}

// handleProfileImport writes a profile document to disk.
func (sess *Session) handleProfileImport(env proto.Envelope) {
	if !sess.scopes.Has(auth.ScopeProfilesWrite) {
		sess.fail(env, proto.CodeForbidden, "importing a profile needs the %q scope", auth.ScopeProfilesWrite)
		return
	}
	var req proto.ProfileImportPayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "profile.import payload is malformed: %v", err)
		return
	}
	if len(req.JSON) > 4<<20 {
		sess.fail(env, proto.CodeInvalidArgument, "profile.import payload is %d bytes; the maximum is 4 MiB", len(req.JSON))
		return
	}

	entry, err := sess.srv.profiles.Import([]byte(req.JSON), req.Overwrite)
	if err != nil {
		if errors.Is(err, profiles.ErrNotFound) {
			sess.fail(env, proto.CodeNotFound, "%v", err)
			return
		}
		sess.fail(env, proto.CodeInvalidProfile, "%v", err)
		return
	}
	sess.reply(env, proto.TypeProfileImport, map[string]any{
		"profile_id": entry.Doc.ID,
		"revision":   entry.Revision,
		"ok":         true,
	})
	sess.srv.log.Info("profile imported", "device", sess.device.ID, "profile", entry.Doc.ID)
}

// handleButtonPress runs the action bound to a button.
func (sess *Session) handleButtonPress(env proto.Envelope) {
	s := sess.srv
	var req proto.ButtonPressPayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "button.press payload is malformed: %v", err)
		return
	}
	if req.ButtonID == "" {
		sess.fail(env, proto.CodeInvalidArgument, "button_id is required")
		return
	}

	prof, page, btn, err := s.resolveButton(req.ProfileID, req.PageID, req.ButtonID)
	if err != nil {
		code := proto.CodeNotFound
		if errors.Is(err, errInvalidProfile) {
			code = proto.CodeInvalidProfile
		}
		sess.fail(env, code, "%v", err)
		return
	}

	// Track the client's position so an action that navigates (deck.back) and a
	// reconnect that resumes have something to work from.
	sess.mu.Lock()
	sess.activeProfile = prof.ID
	sess.activePage = page.ID
	sess.mu.Unlock()

	kind := req.Press.Kind
	if kind == "" {
		kind = "short"
	}

	var action *profile.Action
	switch kind {
	case "long":
		action = btn.OnLongPress
	case "repeat":
		action = btn.OnHold
	default:
		action = btn.OnPress
	}
	if action == nil {
		// A long press on a button that only defines on_press falls back to the
		// short action: the user pressed the button, and doing nothing would
		// look like a broken deck.
		if kind != "short" {
			action = btn.OnPress
		}
	}
	if action == nil {
		sess.fail(env, proto.CodeUnsupported, "button %q has no %s action", btn.ID, kind)
		return
	}

	// Host-side state transitions happen before the action runs, so a toggle
	// that launches something slow still flips immediately.
	sess.applyState(prof, page, btn)

	er := sess.engineRequest(action.Type, action.Params, prof.ID, page.ID, btn.ID)
	sess.runAction(env, er)
}

// handleButtonRelease runs a button's on_release action.
func (sess *Session) handleButtonRelease(env proto.Envelope) {
	s := sess.srv
	var req proto.ButtonReleasePayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "button.release payload is malformed: %v", err)
		return
	}
	prof, page, btn, err := s.resolveButton(req.ProfileID, req.PageID, req.ButtonID)
	if err != nil {
		sess.fail(env, proto.CodeNotFound, "%v", err)
		return
	}
	if btn.OnRelease == nil {
		// Not an error: the client may send a release for any button it pressed.
		// Silently accepting keeps the client simple.
		sess.reply(env, proto.TypeActionResult, proto.ActionResultPayload{
			ExecutionID: engine.NewExecutionID(),
			ActionID:    prof.ID + "/" + page.ID + "/" + btn.ID,
			OK:          true,
		})
		return
	}
	er := sess.engineRequest(btn.OnRelease.Type, btn.OnRelease.Params, prof.ID, page.ID, btn.ID)
	sess.runAction(env, er)
}

// handleActionCancel stops a running execution.
func (sess *Session) handleActionCancel(env proto.Envelope) {
	var req proto.ActionCancelPayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "action.cancel payload is malformed: %v", err)
		return
	}
	if req.ExecutionID == "" {
		sess.fail(env, proto.CodeInvalidArgument, "execution_id is required")
		return
	}

	sess.mu.Lock()
	owned := sess.pending[req.ExecutionID]
	sess.mu.Unlock()

	// A device may only cancel its own executions: otherwise one phone could
	// abort another phone's work.
	if !owned {
		sess.fail(env, proto.CodeNotFound, "execution %q is not running on this session", req.ExecutionID)
		return
	}
	if !sess.srv.engine.Cancel(req.ExecutionID) {
		sess.fail(env, proto.CodeNotFound, "execution %q has already finished", req.ExecutionID)
		return
	}
	sess.reply(env, proto.TypeActionCancel, map[string]any{"execution_id": req.ExecutionID, "cancelled": true})
}

// handleTelemetrySubscribe starts or replaces the session's metric stream.
func (sess *Session) handleTelemetrySubscribe(env proto.Envelope) {
	s := sess.srv
	if !sess.scopes.Has(auth.ScopeSystemRead) {
		sess.fail(env, proto.CodeForbidden, "telemetry needs the %q scope", auth.ScopeSystemRead)
		return
	}
	var req proto.TelemetrySubscribePayload
	if err := proto.DecodePayload(env, &req); err != nil {
		sess.fail(env, proto.CodeInvalidArgument, "telemetry.subscribe payload is malformed: %v", err)
		return
	}

	// An empty metric list means "everything", and an empty interval means the
	// host's default. Both are clamped by the collector rather than rejected,
	// so a client asking for a faster rate gets the fastest the host will give.
	interval := time.Duration(req.IntervalMS) * time.Millisecond
	if req.IntervalMS == 0 {
		interval = time.Second
	}

	sess.mu.Lock()
	old := sess.telemetry
	sess.mu.Unlock()
	if old != nil {
		s.metrics.Unsubscribe(old.ID)
	}

	sub := s.metrics.Subscribe(sess.id, req.Metrics, interval)

	sess.mu.Lock()
	sess.telemetry = sub
	sess.mu.Unlock()

	sess.reply(env, proto.TypeTelemetrySubscribe, map[string]any{
		"ok":          true,
		"interval_ms": sub.Interval.Milliseconds(),
		"metrics":     sub.Metrics,
	})

	// Forward samples to the client until the subscription ends.
	go func() {
		for {
			select {
			case <-sess.closed:
				return
			case sample, ok := <-sub.C():
				if !ok {
					return
				}
				sess.push(proto.TypeEventTelemetry, proto.EventTelemetryPayload{
					TS:     sample.TS,
					Values: sample.Values,
				})
			}
		}
	}()
}

// --- helpers -------------------------------------------------------------

var errInvalidProfile = errors.New("profile is invalid")

// resolveButton finds a profile, page and button, reporting which part was
// missing so the client's error message is actionable.
func (s *Server) resolveButton(profileID, pageID, buttonID string) (*profile.Profile, *profile.Page, *profile.Button, error) {
	if profileID == "" {
		if def := s.profiles.Default(); def != nil {
			profileID = def.Doc.ID
		} else {
			return nil, nil, nil, errors.New("no profiles are loaded on this host")
		}
	}
	entry, ok := s.profiles.Entry(profileID)
	if !ok {
		if err, failed := s.profiles.Errors()[profileID]; failed {
			return nil, nil, nil, fmt.Errorf("%w: %s: %v", errInvalidProfile, profileID, err)
		}
		return nil, nil, nil, fmt.Errorf("no profile %q on this host", profileID)
	}
	prof := entry.Doc

	page, ok := prof.Page(pageID)
	if !ok {
		if pageID == "" {
			page, ok = prof.Page(prof.RootPage)
			if !ok {
				return nil, nil, nil, fmt.Errorf("profile %q has no root page %q", profileID, prof.RootPage)
			}
		} else {
			return nil, nil, nil, fmt.Errorf("profile %q has no page %q", profileID, pageID)
		}
	}

	btn, ok := prof.Button(page.ID, buttonID)
	if !ok {
		return nil, nil, nil, fmt.Errorf("page %q has no button %q", page.ID, buttonID)
	}
	return prof, page, btn, nil
}

// applyState performs the host-side state transition for a stateful button.
// It is a method on the session because the transition belongs to the press
// that caused it, not to the server.
func (sess *Session) applyState(prof *profile.Profile, page *profile.Page, btn *profile.Button) {
	sess.srv.engine.ApplyButtonState(prof, page, btn)
}

// pushStates sends every recorded state for a profile to this session.
func (sess *Session) pushStates(profileID string) {
	for _, st := range sess.srv.engine.ButtonStates(profileID) {
		sess.push(proto.TypeEventButtonState, proto.EventButtonStatePayload{
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
		})
	}
}

func (sess *Session) activeProfileLocked() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.activeProfile
}

// resolveIcons returns the data URIs for the image icons a profile references.
//
// It never fetches: it reads what is already in the icon cache, so serving a
// profile cannot block on the network. Icons are downloaded when the user picks
// them, which is the only moment a network round trip is acceptable.
//
// A missing icon is not an error. The profile is still perfectly usable with a
// placeholder on that one button, and refusing to serve the whole deck because
// one icon is absent would be a far worse failure.
func (s *Server) resolveIcons(doc *profile.Profile) map[string]string {
	if s.icons == nil {
		return nil
	}
	files := doc.ImageIconFiles()
	if len(files) == 0 {
		return nil
	}
	out := make(map[string]string, len(files))
	for _, name := range files {
		if uri := s.icons.Resolve(name); uri != "" {
			out[name] = uri
		} else {
			s.log.Debug("profile references an icon that is not cached", "profile", doc.ID, "icon", name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
