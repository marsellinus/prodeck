//go:build linux

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// linuxShell runs scripts and commands.
type linuxShell struct {
	Unsupported
}

// SupportedInterpreters lists what RunScript can be told to use through
// ScriptOptions.Interpreter.
func (linuxShell) SupportedInterpreters() []string {
	return []string{"sh", "bash", "zsh", "dash", "python3", "python", "perl", "ruby", "node"}
}

func (linuxShell) RunScript(ctx context.Context, opts ScriptOptions) (ExecResult, error) {
	path := ExpandPath("", opts.Path)
	if path == "" {
		return ExecResult{}, fmt.Errorf("platform: script path must not be empty")
	}
	st, err := os.Stat(path)
	if err != nil {
		return ExecResult{}, fmt.Errorf("platform: script %s does not exist", path)
	}
	if st.IsDir() {
		return ExecResult{}, fmt.Errorf("platform: %s is a directory, not a script", path)
	}
	name, args, err := linuxScriptCommand(path, opts.Interpreter, opts.Args, st.Mode())
	if err != nil {
		return ExecResult{}, err
	}
	return runCaptured(ctx, name, args, opts.Dir, opts.Env, opts.Timeout)
}

func (linuxShell) RunCommand(ctx context.Context, opts CommandOptions) (ExecResult, error) {
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("platform: command must not be empty")
	}
	name, args, err := linuxShellCommand(opts.Shell, command)
	if err != nil {
		return ExecResult{}, err
	}
	return runCaptured(ctx, name, args, opts.Dir, opts.Env, opts.Timeout)
}

// linuxScriptCommand turns a script path into an executable plus arguments.
//
// An executable file is run directly so its shebang decides the interpreter,
// which is the only thing that gets `#!/usr/bin/env python3` right. A file
// without the execute bit would fail with EACCES, so the extension is used
// instead of telling the operator to chmod something the deck could just run.
func linuxScriptCommand(path, interpreter string, args []string, mode os.FileMode) (string, []string, error) {
	if interpreter != "" {
		return interpreter, append([]string{path}, args...), nil
	}
	if mode.Perm()&0o111 != 0 {
		return path, args, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	var name string
	switch ext {
	case ".sh":
		name = "sh"
	case ".bash":
		name = "bash"
	case ".zsh":
		name = "zsh"
	case ".py":
		name = "python3"
	case ".pl":
		name = "perl"
	case ".rb":
		name = "ruby"
	case ".js":
		name = "node"
	default:
		return "", nil, fmt.Errorf("platform: %s is not executable and its extension %q names no interpreter; chmod +x it or set an interpreter for the script", path, ext)
	}
	if _, err := exec.LookPath(name); err != nil {
		return "", nil, fmt.Errorf("platform: %s needs %s, which is not installed", path, name)
	}
	return name, append([]string{path}, args...), nil
}

// linuxShellCommand picks the interpreter for a command string.
func linuxShellCommand(shell, command string) (string, []string, error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "", "auto":
		// The user's login shell is preferred when it is one we know, because
		// a command written for the deck is usually written against the shell
		// they actually use. Anything else falls back to POSIX sh.
		if base := filepath.Base(strings.TrimSpace(os.Getenv("SHELL"))); base == "bash" || base == "zsh" {
			return base, []string{"-c", command}, nil
		}
		return "sh", []string{"-c", command}, nil
	case "sh", "bash", "zsh", "dash":
		return strings.ToLower(strings.TrimSpace(shell)), []string{"-c", command}, nil
	case "powershell", "pwsh", "cmd":
		return "", nil, fmt.Errorf("platform: shell %q is a Windows shell and is not available here", shell)
	default:
		return "", nil, fmt.Errorf("platform: shell %q must be auto, sh, bash, zsh or dash", shell)
	}
}
