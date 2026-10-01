package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// launchApplication starts a program. The target may be a platform map, which
// is how one profile works on Windows and Linux without edits.
type launchApplication struct {
	Base
	plat *platform.Platform
}

type launchParams struct {
	Target json.RawMessage   `json:"target"`
	Args   []string          `json:"args,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Detach *bool             `json:"detach,omitempty"`
}

func (a *launchApplication) Validate(raw json.RawMessage) error {
	var p launchParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if _, err := platform.ResolveTargetFor(a.plat.OSName, p.Target); err != nil {
		return err
	}
	if len(p.Args) > 64 {
		return fmt.Errorf("args lists %d entries; the maximum is 64", len(p.Args))
	}
	if len(p.Env) > 64 {
		return fmt.Errorf("env lists %d entries; the maximum is 64", len(p.Env))
	}
	return nil
}

func (a *launchApplication) Run(ctx context.Context, req Request) (Result, error) {
	var p launchParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	target, err := platform.ResolveTargetFor(a.plat.OSName, p.Target)
	if err != nil {
		return Result{}, err
	}
	detach := true
	if p.Detach != nil {
		detach = *p.Detach
	}
	opts := platform.LaunchOptions{Args: p.Args, Dir: p.Cwd, Env: p.Env, Detach: detach}
	if err := a.plat.Launcher.Open(ctx, target, opts); err != nil {
		return Result{}, err
	}
	return Result{Detail: "launched " + target}, nil
}

// openURL opens a URL in the default browser.
type openURL struct {
	Base
	plat *platform.Platform
}

type urlParams struct {
	URL string `json:"url"`
}

func (a *openURL) Validate(raw json.RawMessage) error {
	var p urlParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	_, err := platform.SanitizeURL(p.URL)
	return err
}

func (a *openURL) Run(ctx context.Context, req Request) (Result, error) {
	var p urlParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	u, err := platform.SanitizeURL(p.URL)
	if err != nil {
		return Result{}, err
	}
	if err := a.plat.Launcher.OpenURL(ctx, u); err != nil {
		return Result{}, err
	}
	return Result{Detail: "opened " + u}, nil
}

// openFolder opens a directory in the file manager, confined to the allowed
// roots unless the operator enabled absolute paths.
type openFolder struct {
	Base
	plat   *platform.Platform
	engine *Engine
}

type folderParams struct {
	Path string `json:"path"`
}

func (a *openFolder) Validate(raw json.RawMessage) error {
	var p folderParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if p.Path == "" {
		return fmt.Errorf("path must not be empty")
	}
	return nil
}

func (a *openFolder) Run(ctx context.Context, req Request) (Result, error) {
	var p folderParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	path, err := a.engine.resolvePath(p.Path, req.ProfileID, false)
	if err != nil {
		return Result{}, err
	}
	if err := a.plat.Launcher.OpenFolder(ctx, path); err != nil {
		return Result{}, err
	}
	return Result{Detail: "opened " + path}, nil
}

// openTerminal opens a terminal emulator, optionally running a command.
type openTerminal struct {
	Base
	plat   *platform.Platform
	engine *Engine
}

type terminalParams struct {
	Cwd     string `json:"cwd,omitempty"`
	Command string `json:"command,omitempty"`
}

func (a *openTerminal) Validate(raw json.RawMessage) error {
	var p terminalParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if len(p.Command) > 4096 {
		return fmt.Errorf("command is %d bytes; the maximum is 4096", len(p.Command))
	}
	return nil
}

func (a *openTerminal) Run(ctx context.Context, req Request) (Result, error) {
	var p terminalParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	dir := p.Cwd
	if dir != "" {
		resolved, err := a.engine.resolvePath(dir, req.ProfileID, false)
		if err != nil {
			return Result{}, err
		}
		dir = resolved
	}
	if err := a.plat.Launcher.OpenTerminal(ctx, platform.TerminalOptions{Dir: dir, Command: p.Command}); err != nil {
		return Result{}, err
	}
	return Result{Detail: "opened a terminal"}, nil
}

func registerApps(r *Registry, plat *platform.Platform, e *Engine) error {
	actions := []Action{
		&launchApplication{Base: NewBase("launch_application", auth.ScopeApps), plat: plat},
		&openURL{Base: NewBase("open_url", auth.ScopeApps), plat: plat},
		&openFolder{Base: NewBase("open_folder", auth.ScopeApps), plat: plat, engine: e},
		&openTerminal{Base: NewBase("open_terminal", auth.ScopeApps), plat: plat, engine: e},
	}
	for _, a := range actions {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
