package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// keyboardShortcut sends a chord such as CTRL+SHIFT+P.
type keyboardShortcut struct {
	Base
	plat *platform.Platform
}

type shortcutParams struct {
	// Keys is a list of key names, or a platform map of lists, so one profile
	// can express "CTRL+SHIFT+ESC on Windows, CTRL+ALT+T on Linux".
	Keys json.RawMessage `json:"keys"`
	Mode string          `json:"mode,omitempty"`
	// IntervalMS spaces the keystrokes, which some applications need in order
	// to notice a fast paste.
	IntervalMS int `json:"interval_ms,omitempty"`
}

// resolveKeys decodes the two accepted forms of `keys`, resolving a platform map
// against the operating system the host is running as.
func resolveKeys(osName string, raw json.RawMessage) ([]string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("keys is required")
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("keys: %w", err)
		}
		return list, nil
	}
	// Platform map: pick this host's entry.
	var m map[string][]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("keys must be a list of key names or a map from platform to list: %w", err)
	}
	if list, ok := m[osName]; ok && len(list) > 0 {
		return list, nil
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("keys has no entry for %s (it defines %s)", osName, strings.Join(names, ", "))
}

func (a *keyboardShortcut) Validate(raw json.RawMessage) error {
	var p shortcutParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	keys, err := resolveKeys(a.plat.OSName, p.Keys)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("keys must list at least one key, for example [\"CTRL\",\"C\"]")
	}
	if len(keys) > 8 {
		return fmt.Errorf("keys lists %d keys; a shortcut is at most 8", len(keys))
	}
	for _, k := range keys {
		if _, err := platform.LookupKey(k); err != nil {
			return err
		}
	}
	if _, err := platform.ParseKeyMode(p.Mode); err != nil {
		return err
	}
	if p.IntervalMS < 0 || p.IntervalMS > 5000 {
		return fmt.Errorf("interval_ms must be 0..5000, got %d", p.IntervalMS)
	}
	return nil
}

func (a *keyboardShortcut) Run(ctx context.Context, req Request) (Result, error) {
	var p shortcutParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	names, err := resolveKeys(a.plat.OSName, p.Keys)
	if err != nil {
		return Result{}, err
	}
	mode, err := platform.ParseKeyMode(p.Mode)
	if err != nil {
		return Result{}, err
	}
	keys := make([]platform.Key, 0, len(names))
	for _, k := range names {
		keys = append(keys, platform.Key(k))
	}

	if p.IntervalMS > 0 && mode == platform.KeyPress && len(keys) > 1 {
		// Spaced delivery: each key is pressed and released in turn, which is
		// what an application that reads raw scancodes expects. It is a
		// different thing from a chord, so it is opt-in.
		for _, k := range keys {
			if err := a.plat.Input.Key(ctx, k, platform.KeyPress); err != nil {
				return Result{}, err
			}
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(time.Duration(p.IntervalMS) * time.Millisecond):
			}
		}
		return Result{Detail: fmt.Sprintf("sent %d keys with %dms spacing", len(keys), p.IntervalMS)}, nil
	}

	if err := a.plat.Input.Shortcut(ctx, keys, mode); err != nil {
		return Result{}, err
	}
	return Result{Detail: fmt.Sprintf("%s %v", mode, names)}, nil
}

// keyboardKey taps one key.
type keyboardKey struct {
	Base
	plat *platform.Platform
}

type keyParams struct {
	Key  string `json:"key"`
	Mode string `json:"mode,omitempty"`
}

func (a *keyboardKey) Validate(raw json.RawMessage) error {
	var p keyParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if _, err := platform.LookupKey(p.Key); err != nil {
		return err
	}
	_, err := platform.ParseKeyMode(p.Mode)
	return err
}

func (a *keyboardKey) Run(ctx context.Context, req Request) (Result, error) {
	var p keyParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	mode, err := platform.ParseKeyMode(p.Mode)
	if err != nil {
		return Result{}, err
	}
	if err := a.plat.Input.Key(ctx, platform.Key(p.Key), mode); err != nil {
		return Result{}, err
	}
	return Result{Detail: fmt.Sprintf("%s %s", mode, p.Key)}, nil
}

// keyboardText types a literal string.
type keyboardText struct {
	Base
	plat *platform.Platform
}

type textParams struct {
	Text       string `json:"text"`
	IntervalMS int    `json:"interval_ms,omitempty"`
}

// MaxTextBytes bounds one text injection. A phone keyboard cannot produce more
// than a few thousand characters, and an unbounded field would let a client
// hold an input worker indefinitely.
const MaxTextBytes = 4096

func (a *keyboardText) Validate(raw json.RawMessage) error {
	var p textParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if p.Text == "" {
		return fmt.Errorf("text must not be empty")
	}
	if len(p.Text) > MaxTextBytes {
		return fmt.Errorf("text is %d bytes; the maximum is %d", len(p.Text), MaxTextBytes)
	}
	if p.IntervalMS < 0 || p.IntervalMS > 1000 {
		return fmt.Errorf("interval_ms must be 0..1000, got %d", p.IntervalMS)
	}
	return nil
}

func (a *keyboardText) Run(ctx context.Context, req Request) (Result, error) {
	var p textParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	if err := a.plat.Input.Text(ctx, p.Text, time.Duration(p.IntervalMS)*time.Millisecond); err != nil {
		return Result{}, err
	}
	return Result{Output: map[string]any{"typed": len([]rune(p.Text))}}, nil
}

func registerKeyboard(r *Registry, plat *platform.Platform) error {
	actions := []Action{
		&keyboardShortcut{Base: NewBase("keyboard.shortcut", auth.ScopeKeyboard), plat: plat},
		&keyboardKey{Base: NewBase("keyboard.key", auth.ScopeKeyboard), plat: plat},
		&keyboardText{Base: NewBase("keyboard.text", auth.ScopeKeyboard), plat: plat},
	}
	for _, a := range actions {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
