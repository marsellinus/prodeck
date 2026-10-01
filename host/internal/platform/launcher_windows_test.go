//go:build windows

package platform

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests open a real console window, so they are skipped unless the
// developer asks for them. They exist because the bug they cover was invisible
// to every other kind of test: the action returned success, the host stayed up,
// and the terminal still vanished.
//
//	MOBILEDECK_E2E_CONSOLE=1 go test ./internal/platform -run Terminal -v

func countCmd() int {
	out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq cmd.exe", "/NH").Output()
	return strings.Count(string(out), "cmd.exe")
}

func requireConsole(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping a test that opens a real window")
	}
	if !e2eConsoleEnabled() {
		t.Skip("set MOBILEDECK_E2E_CONSOLE=1 to run the console lifetime test")
	}
}

// TestTerminalSurvivesTheAction is the regression test for a terminal that
// appeared and was force-killed about a hundred milliseconds later.
//
// The engine cancels the action's context as soon as Run returns. The launcher
// used to pass that context to the child, and CommandContext installs a Cancel
// that runs taskkill on the process group, so the console died the moment the
// button press finished. From the phone that looks like the action crashing.
func TestTerminalSurvivesTheAction(t *testing.T) {
	requireConsole(t)

	before := countCmd()
	p := New()

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Launcher.OpenTerminal(ctx, TerminalOptions{}); err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	// Exactly what the engine does the moment the action returns.
	cancel()

	time.Sleep(3 * time.Second)
	after := countCmd()
	if after <= before {
		t.Fatalf("the terminal did not survive the action returning (cmd.exe before=%d, after=%d)", before, after)
	}
	t.Logf("a terminal is still running (cmd.exe %d -> %d)", before, after)
}

// TestTerminalWithCommandSurvives covers the /K <command> form, which is what a
// profile uses when the button should also run something.
func TestTerminalWithCommandSurvives(t *testing.T) {
	requireConsole(t)

	before := countCmd()
	p := New()

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Launcher.OpenTerminal(ctx, TerminalOptions{Command: "echo mobiledeck"}); err != nil {
		t.Fatalf("OpenTerminal with a command: %v", err)
	}
	cancel()

	time.Sleep(3 * time.Second)
	if after := countCmd(); after <= before {
		t.Fatalf("the terminal with a command did not survive (before=%d, after=%d)", before, after)
	}
}
