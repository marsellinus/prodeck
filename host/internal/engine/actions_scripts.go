package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// runScript executes a file. The path is confined to the profile directory and
// the host scripts directory unless the operator opted out, which is the
// mitigation for path traversal (docs/SECURITY.md T7).
type runScript struct {
	Base
	plat   *platform.Platform
	engine *Engine
}

type scriptParams struct {
	Path        string            `json:"path"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	TimeoutMS   int               `json:"timeout_ms,omitempty"`
	Interpreter string            `json:"interpreter,omitempty"`
}

func (a *runScript) Validate(raw json.RawMessage) error {
	var p scriptParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Path) == "" {
		return fmt.Errorf("path must not be empty")
	}
	if len(p.Args) > 64 {
		return fmt.Errorf("args lists %d entries; the maximum is 64", len(p.Args))
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > 3600000 {
		return fmt.Errorf("timeout_ms must be 0..3600000, got %d", p.TimeoutMS)
	}
	return nil
}

func (a *runScript) Run(ctx context.Context, req Request) (Result, error) {
	var p scriptParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	path, err := a.engine.resolvePath(p.Path, req.ProfileID, true)
	if err != nil {
		return Result{}, err
	}
	dir := p.Cwd
	if dir != "" {
		if dir, err = a.engine.resolvePath(dir, req.ProfileID, false); err != nil {
			return Result{}, err
		}
	} else {
		dir = filepath.Dir(path)
	}

	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = a.engine.opts.DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	res, err := a.plat.Shell.RunScript(runCtx, platform.ScriptOptions{
		Path:        path,
		Args:        p.Args,
		Dir:         dir,
		Env:         p.Env,
		Timeout:     timeout,
		Interpreter: p.Interpreter,
	})
	return scriptResult(res, err, started, runCtx)
}

// runCommand executes a shell command. It is the most powerful action in the
// catalogue and therefore carries the scripts scope, which is never granted by
// default.
type runCommand struct {
	Base
	plat   *platform.Platform
	engine *Engine
}

type commandParams struct {
	Command   string            `json:"command"`
	Shell     string            `json:"shell,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

func (a *runCommand) Validate(raw json.RawMessage) error {
	var p commandParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Command) == "" {
		return fmt.Errorf("command must not be empty")
	}
	if len(p.Command) > 8192 {
		return fmt.Errorf("command is %d bytes; the maximum is 8192", len(p.Command))
	}
	switch p.Shell {
	case "", "auto", "sh", "bash", "cmd", "powershell", "pwsh":
	default:
		return fmt.Errorf("shell %q must be auto, sh, bash, cmd, powershell or pwsh", p.Shell)
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > 3600000 {
		return fmt.Errorf("timeout_ms must be 0..3600000, got %d", p.TimeoutMS)
	}
	return nil
}

func (a *runCommand) Run(ctx context.Context, req Request) (Result, error) {
	var p commandParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	dir := ""
	if p.Cwd != "" {
		var err error
		if dir, err = a.engine.resolvePath(p.Cwd, req.ProfileID, false); err != nil {
			return Result{}, err
		}
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = a.engine.opts.DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	res, err := a.plat.Shell.RunCommand(runCtx, platform.CommandOptions{
		Command: p.Command,
		Shell:   p.Shell,
		Dir:     dir,
		Env:     p.Env,
		Timeout: timeout,
	})
	return scriptResult(res, err, started, runCtx)
}

// scriptResult converts a platform execution into an action result, mapping the
// interesting failure modes onto errors the server can classify.
func scriptResult(res platform.ExecResult, err error, started time.Time, ctx context.Context) (Result, error) {
	out := map[string]any{
		"exit_code":   res.ExitCode,
		"duration_ms": time.Since(started).Milliseconds(),
	}
	if res.Stdout != "" {
		out["stdout"] = res.Stdout
	}
	if res.Stderr != "" {
		out["stderr"] = res.Stderr
	}
	if res.Truncated {
		out["truncated"] = true
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || res.TimedOut {
			return Result{Output: out}, fmt.Errorf("the command exceeded its time limit and was stopped: %w", context.DeadlineExceeded)
		}
		if errors.Is(err, context.Canceled) || res.Cancelled {
			return Result{Output: out}, fmt.Errorf("the command was cancelled: %w", context.Canceled)
		}
		return Result{Output: out}, err
	}
	if res.ExitCode != 0 {
		// A non-zero exit is a failure the user needs to see, with the captured
		// stderr attached rather than swallowed.
		msg := fmt.Sprintf("exited with code %d", res.ExitCode)
		if res.Stderr != "" {
			msg += ": " + firstLine(res.Stderr)
		}
		return Result{Output: out}, errors.New(msg)
	}
	return Result{Output: out}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return strings.TrimSpace(s)
}

// resolvePath turns a profile-relative path into an absolute, confined path.
//
// Resolution order matters for authoring: a relative path is first tried
// against the profile directory (so a profile ships with its own scripts) and
// then against the host scripts directory (so a shared script is written once).
func (e *Engine) resolvePath(path, profileID string, mustBeFile bool) (string, error) {
	if e.opts.AllowAbsolutePaths {
		abs := platform.ExpandPath("", path)
		if mustBeFile {
			if info, err := os.Stat(abs); err != nil {
				return "", fmt.Errorf("%s does not exist", abs)
			} else if info.IsDir() {
				return "", fmt.Errorf("%s is a directory, not a script", abs)
			}
		}
		return abs, nil
	}

	roots := make([]string, 0, len(e.opts.ScriptRoots)+1)
	if dir := e.profileDir(profileID); dir != "" {
		roots = append(roots, dir)
	}
	roots = append(roots, e.opts.ScriptRoots...)
	if len(roots) == 0 {
		return "", fmt.Errorf("no script directory is configured for this host")
	}

	var lastErr error
	for _, root := range roots {
		candidate := platform.ExpandPath(root, path)
		confined, err := platform.ConfinePath(candidate, []string{root})
		if err != nil {
			lastErr = err
			continue
		}
		if mustBeFile {
			info, err := os.Stat(confined)
			if err != nil {
				lastErr = err
				continue
			}
			if info.IsDir() {
				lastErr = fmt.Errorf("%s is a directory, not a script", confined)
				continue
			}
		}
		return confined, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate path for %q", path)
	}
	return "", lastErr
}

// profileDir returns the on-disk directory of a loaded profile, or "" when the
// profile is unknown. The lookup is injected so the engine does not depend on
// the profile loader.
func (e *Engine) profileDir(profileID string) string {
	if e.opts.ProfileDir == nil || profileID == "" {
		return ""
	}
	return e.opts.ProfileDir(profileID)
}

func registerScripts(r *Registry, plat *platform.Platform, e *Engine) error {
	actions := []Action{
		&runScript{Base: NewBase("run_script", auth.ScopeScripts), plat: plat, engine: e},
		&runCommand{Base: NewBase("run_command", auth.ScopeScripts), plat: plat, engine: e},
	}
	for _, a := range actions {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
