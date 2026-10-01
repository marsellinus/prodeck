//go:build windows

package platform

import (
	"strings"
	"testing"
)

func TestWindowsScriptCommand(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		interpreter string
		wantName    string
		wantArgs    []string
	}{
		{
			name:     "powershell by extension",
			path:     `C:\scripts\hello.ps1`,
			wantName: "powershell.exe",
			wantArgs: []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", `C:\scripts\hello.ps1`},
		},
		{
			name:     "batch by extension",
			path:     `C:\scripts\hello.cmd`,
			wantName: "cmd.exe",
			wantArgs: []string{"/C", `C:\scripts\hello.cmd`},
		},
		{
			name:     "python by extension",
			path:     `C:\scripts\hello.py`,
			wantName: "python",
			wantArgs: []string{`C:\scripts\hello.py`},
		},
		{
			// An executable is run directly: the OS decides, not us.
			name:     "exe runs directly",
			path:     `C:\scripts\hello.exe`,
			wantName: `C:\scripts\hello.exe`,
			wantArgs: nil,
		},
		{
			name:        "interpreter override wins over extension",
			path:        `C:\scripts\hello.ps1`,
			interpreter: "pwsh",
			wantName:    "pwsh",
			wantArgs:    []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", `C:\scripts\hello.ps1`},
		},
		{
			name:        "cmd interpreter override",
			path:        `C:\scripts\hello.txt`,
			interpreter: "cmd",
			wantName:    "cmd.exe",
			wantArgs:    []string{"/C", `C:\scripts\hello.txt`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args, err := windowsScriptCommand(tc.path, tc.interpreter, nil)
			if err != nil {
				t.Fatalf("windowsScriptCommand: %v", err)
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if strings.Join(args, "\x00") != strings.Join(tc.wantArgs, "\x00") {
				t.Errorf("args = %q, want %q", args, tc.wantArgs)
			}
		})
	}

	// Extra arguments are appended after the script path.
	name, args, err := windowsScriptCommand(`C:\a.ps1`, "", []string{"one", "two"})
	if err != nil {
		t.Fatalf("windowsScriptCommand: %v", err)
	}
	if name != "powershell.exe" {
		t.Errorf("name = %q", name)
	}
	if len(args) != 7 || args[5] != "one" || args[6] != "two" {
		t.Errorf("script args not appended: %q", args)
	}
}

func TestWindowsShellCommand(t *testing.T) {
	tests := []struct {
		shell    string
		wantName string
	}{
		{"", "powershell.exe"},
		{"auto", "powershell.exe"},
		{"powershell", "powershell.exe"},
		{"pwsh", "pwsh.exe"},
		{"cmd", "cmd.exe"},
		{"CMD.EXE", "cmd.exe"},
		{"sh", "sh"},
		{"bash", "bash"},
	}
	for _, tc := range tests {
		name, args, err := windowsShellCommand(tc.shell, "echo hi")
		if err != nil {
			t.Errorf("windowsShellCommand(%q): %v", tc.shell, err)
			continue
		}
		if name != tc.wantName {
			t.Errorf("windowsShellCommand(%q) name = %q, want %q", tc.shell, name, tc.wantName)
		}
		if len(args) == 0 || args[len(args)-1] != "echo hi" {
			t.Errorf("windowsShellCommand(%q) args = %q, command not last", tc.shell, args)
		}
	}

	for _, bad := range []string{"nonsense", "fish"} {
		if _, _, err := windowsShellCommand(bad, "echo"); err == nil {
			t.Errorf("windowsShellCommand(%q) should have been rejected", bad)
		}
	}
}

func TestWindowsShellInterpreters(t *testing.T) {
	got := windowsShell{}.SupportedInterpreters()
	if len(got) == 0 {
		t.Fatalf("SupportedInterpreters is empty")
	}
	// Whatever the list says must be accepted by windowsShellCommand, or the
	// CLI would advertise something that fails at press time.
	for _, name := range got {
		if name == "python" || name == "python3" {
			continue // script interpreters, not command shells
		}
		if _, _, err := windowsShellCommand(name, "echo"); err != nil {
			t.Errorf("advertised interpreter %q is rejected by windowsShellCommand: %v", name, err)
		}
	}
}
