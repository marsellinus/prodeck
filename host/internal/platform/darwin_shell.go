//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// darwinShell runs scripts and commands.
type darwinShell struct {
	Unsupported
}

// SupportedInterpreters lists what RunScript can be told to use through
// ScriptOptions.Interpreter.
func (darwinShell) SupportedInterpreters() []string {
	return []string{"sh", "bash", "zsh", "python3", "perl", "ruby", "node"}
}

func (darwinShell) RunScript(ctx context.Context, opts ScriptOptions) (ExecResult, error) {
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
	name, args, err := darwinScriptCommand(path, opts.Interpreter, opts.Args, st.Mode())
	if err != nil {
		return ExecResult{}, err
	}
	return runCaptured(ctx, name, args, opts.Dir, opts.Env, opts.Timeout)
}

func (darwinShell) RunCommand(ctx context.Context, opts CommandOptions) (ExecResult, error) {
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("platform: command must not be empty")
	}
	name, args, err := darwinShellCommand(opts.Shell, command)
	if err != nil {
		return ExecResult{}, err
	}
	return runCaptured(ctx, name, args, opts.Dir, opts.Env, opts.Timeout)
}

// darwinScriptCommand turns a script path into an executable plus arguments.
//
// An executable file is run directly so its shebang decides the interpreter; a
// `.command` file is a double-clickable shell script by convention, so it is
// handed to sh even when the execute bit is missing.
func darwinScriptCommand(path, interpreter string, args []string, mode os.FileMode) (string, []string, error) {
	if interpreter != "" {
		return interpreter, append([]string{path}, args...), nil
	}
	if mode.Perm()&0o111 != 0 {
		return path, args, nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	var name string
	switch ext {
	case ".command", ".sh":
		name = "/bin/sh"
	case ".bash":
		name = "/bin/bash"
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
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err != nil {
			return "", nil, fmt.Errorf("platform: %s is missing", name)
		}
	} else if _, err := exec.LookPath(name); err != nil {
		return "", nil, fmt.Errorf("platform: %s needs %s, which is not installed", path, name)
	}
	return name, append([]string{path}, args...), nil
}

// darwinShellCommand picks the interpreter for a command string.
func darwinShellCommand(shell, command string) (string, []string, error) {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "", "auto":
		// macOS ships /bin/sh (which is bash in POSIX mode) and /bin/bash; sh
		// is used by default because a deck command is usually a one-liner and
		// sh's startup is faster than bash's.
		return "/bin/sh", []string{"-c", command}, nil
	case "sh":
		return "/bin/sh", []string{"-c", command}, nil
	case "bash":
		return "/bin/bash", []string{"-c", command}, nil
	case "zsh":
		return "/bin/zsh", []string{"-c", command}, nil
	case "powershell", "pwsh", "cmd":
		return "", nil, fmt.Errorf("platform: shell %q is a Windows shell and is not available here", shell)
	default:
		return "", nil, fmt.Errorf("platform: shell %q must be auto, sh, bash or zsh", shell)
	}
}
