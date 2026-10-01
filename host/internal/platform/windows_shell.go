//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// windowsShell runs scripts and commands.
//
// All children are started through CommandContext, so a script that spawns
// grandchildren is torn down as a group when the request is cancelled or times
// out (exec_windows.go).
type windowsShell struct {
	Unsupported
}

// SupportedInterpreters lists what RunScript can be told to use through
// ScriptOptions.Interpreter, and what RunCommand accepts as a Shell.
func (windowsShell) SupportedInterpreters() []string {
	return []string{"powershell", "pwsh", "cmd", "sh", "bash", "python", "python3"}
}

func (windowsShell) RunScript(ctx context.Context, opts ScriptOptions) (ExecResult, error) {
	path := ExpandPath("", opts.Path)
	if path == "" {
		return ExecResult{}, fmt.Errorf("platform: script path must not be empty")
	}
	if st, err := os.Stat(path); err != nil {
		return ExecResult{}, fmt.Errorf("platform: script %s does not exist", path)
	} else if st.IsDir() {
		return ExecResult{}, fmt.Errorf("platform: %s is a directory, not a script", path)
	}
	name, args, err := windowsScriptCommand(path, opts.Interpreter, opts.Args)
	if err != nil {
		return ExecResult{}, err
	}
	return runCaptured(ctx, name, args, opts.Dir, opts.Env, opts.Timeout)
}

func (windowsShell) RunCommand(ctx context.Context, opts CommandOptions) (ExecResult, error) {
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("platform: command must not be empty")
	}
	name, args, err := windowsShellCommand(opts.Shell, command)
	if err != nil {
		return ExecResult{}, err
	}
	return runCaptured(ctx, name, args, opts.Dir, opts.Env, opts.Timeout)
}

// windowsScriptCommand turns a script path into an executable plus arguments.
//
// The extension decides the interpreter, because Windows has no shebang: a
// ".ps1" handed to CreateProcess fails with "not a valid Win32 application",
// which is a useless thing to show the operator.
func windowsScriptCommand(path, interpreter string, args []string) (string, []string, error) {
	if interpreter != "" {
		switch strings.ToLower(strings.TrimSpace(interpreter)) {
		case "powershell", "pwsh":
			return interpreter, append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}, args...), nil
		case "cmd", "cmd.exe":
			return "cmd.exe", append([]string{"/C", path}, args...), nil
		default:
			return interpreter, append([]string{path}, args...), nil
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ps1", ".psm1":
		return "powershell.exe", append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}, args...), nil
	case ".bat", ".cmd":
		return "cmd.exe", append([]string{"/C", path}, args...), nil
	case ".py":
		return "python", append([]string{path}, args...), nil
	default:
		// .exe, .com, .jar via its association, or anything the shell knows
		// better than we do: let the OS decide.
		return path, args, nil
	}
}

// windowsShellCommand picks the interpreter for a command string.
//
// "auto" uses Windows PowerShell, which is present on every supported version;
// PowerShell 7 (pwsh) is used only when asked for by name, so a profile written
// against 5.1 keeps 5.1's behaviour.
func windowsShellCommand(shell, command string) (string, []string, error) {
	lowered := strings.ToLower(strings.TrimSpace(shell))
	switch lowered {
	case "", "auto", "powershell":
		return "powershell.exe", []string{"-NoProfile", "-Command", command}, nil
	case "pwsh":
		return "pwsh.exe", []string{"-NoProfile", "-Command", command}, nil
	case "cmd", "cmd.exe":
		return "cmd.exe", []string{"/C", command}, nil
	case "sh", "bash":
		return lowered, []string{"-c", command}, nil
	default:
		return "", nil, fmt.Errorf("platform: shell %q must be auto, powershell, pwsh, cmd, sh or bash", shell)
	}
}
