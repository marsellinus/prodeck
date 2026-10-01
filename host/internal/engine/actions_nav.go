package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
)

// Navigation actions are addressed to the client that sent the press, never
// broadcast: opening a page on one phone must not move another phone's deck.
//
// They carry no scope because they change nothing on the host. Requiring a
// permission to turn a page would be ceremony with no security value.

// openPage navigates the client to another page, optionally in another profile.
type openPage struct {
	Base
	engine *Engine
}

type openPageParams struct {
	ProfileID string `json:"profile_id,omitempty"`
	PageID    string `json:"page_id"`
}

func (a *openPage) Validate(raw json.RawMessage) error {
	var p openPageParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.PageID) == "" {
		return fmt.Errorf("page_id must not be empty")
	}
	return nil
}

func (a *openPage) Run(ctx context.Context, req Request) (Result, error) {
	var p openPageParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	profileID := p.ProfileID
	if profileID == "" {
		profileID = req.ProfileID
	}
	// The page is resolved against the loaded profile so a stale button gives a
	// clear error instead of a client that navigates to nowhere. The check is
	// skipped when no lookup is installed, which is the case for the CLI's
	// `actions` listing and for unit tests.
	if a.engine.opts.PageExists != nil && !a.engine.opts.PageExists(profileID, p.PageID) {
		return Result{}, fmt.Errorf("page %q does not exist in profile %q", p.PageID, profileID)
	}
	a.engine.emit(Event{
		DeviceID: req.DeviceID,
		Type:     EventOpenPage,
		Payload:  map[string]any{"profile_id": profileID, "page_id": p.PageID},
	})
	return Result{Detail: "opened page " + p.PageID}, nil
}

// deckBack pops one level of the client's page stack.
type deckBack struct {
	Base
	engine *Engine
}

func (a *deckBack) Validate(raw json.RawMessage) error {
	var p noParams
	return decodeParams(raw, &p)
}

func (a *deckBack) Run(ctx context.Context, req Request) (Result, error) {
	a.engine.emit(Event{DeviceID: req.DeviceID, Type: EventBack, Payload: map[string]any{}})
	return Result{Detail: "went back"}, nil
}

// changeProfile switches the client to another profile.
type changeProfile struct {
	Base
	engine *Engine
}

type changeProfileParams struct {
	ProfileID string `json:"profile_id"`
}

func (a *changeProfile) Validate(raw json.RawMessage) error {
	var p changeProfileParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.ProfileID) == "" {
		return fmt.Errorf("profile_id must not be empty")
	}
	return nil
}

func (a *changeProfile) Run(ctx context.Context, req Request) (Result, error) {
	var p changeProfileParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	if a.engine.opts.ProfileExists != nil && !a.engine.opts.ProfileExists(p.ProfileID) {
		return Result{}, fmt.Errorf("profile %q is not loaded", p.ProfileID)
	}
	a.engine.emit(Event{
		DeviceID: req.DeviceID,
		Type:     EventChangeProfile,
		Payload:  map[string]any{"profile_id": p.ProfileID},
	})
	return Result{Detail: "switched to " + p.ProfileID}, nil
}

// deckNotify shows a transient message on the client.
type deckNotify struct {
	Base
	engine *Engine
}

type notifyParams struct {
	Message string `json:"message"`
	Level   string `json:"level,omitempty"`
}

func (a *deckNotify) Validate(raw json.RawMessage) error {
	var p notifyParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Message) == "" {
		return fmt.Errorf("message must not be empty")
	}
	if len(p.Message) > 512 {
		return fmt.Errorf("message is %d bytes; the maximum is 512", len(p.Message))
	}
	switch p.Level {
	case "", "info", "success", "warn", "error":
	default:
		return fmt.Errorf("level %q must be info, success, warn or error", p.Level)
	}
	return nil
}

func (a *deckNotify) Run(ctx context.Context, req Request) (Result, error) {
	var p notifyParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	level := p.Level
	if level == "" {
		level = "info"
	}
	a.engine.emit(Event{
		DeviceID: req.DeviceID,
		Type:     EventNotify,
		Payload:  map[string]any{"message": p.Message, "level": level},
	})
	return Result{Detail: "notified the client"}, nil
}

func registerNav(r *Registry, e *Engine) error {
	actions := []Action{
		&openPage{Base: NewBase("deck.open_page", auth.ScopeNone), engine: e},
		&deckBack{Base: NewBase("deck.back", auth.ScopeNone), engine: e},
		&changeProfile{Base: NewBase("deck.change_profile", auth.ScopeNone), engine: e},
		&deckNotify{Base: NewBase("deck.notify", auth.ScopeNone), engine: e},
	}
	for _, a := range actions {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
