//go:build windows

package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
)

// TestEndToEndRealKeystrokes is the acceptance test for the whole Windows input
// path. Everything below is real: a real WebSocket, the real protocol, the real
// engine, the real SendInput implementation in internal/platform, and a real
// Win32 window that records the characters it receives.
//
// It is skipped unless MOBILEDECK_E2E_INPUT is set, because it needs a desktop
// session and it steals keyboard focus from whatever the developer is doing.
// scripts/smokeinput must be built first; its path is taken from
// MOBILEDECK_E2E_WINDOW, defaulting to .devdata/smokeinput.exe.
//
// Run it with:
//
//	MOBILEDECK_E2E_INPUT=1 go test ./internal/server -run TestEndToEndRealKeystrokes -v
func TestEndToEndRealKeystrokes(t *testing.T) {
	if os.Getenv("MOBILEDECK_E2E_INPUT") == "" {
		t.Skip("set MOBILEDECK_E2E_INPUT=1 to run the real-keystroke test; it needs a desktop session and takes focus")
	}

	windowExe := os.Getenv("MOBILEDECK_E2E_WINDOW")
	if windowExe == "" {
		windowExe = filepath.Join("..", "..", "..", ".devdata", "smokeinput.exe")
	}
	if _, err := os.Stat(windowExe); err != nil {
		t.Fatalf("the recording window is not built at %s: %v\nbuild it with: cd scripts/smokeinput && go build -o ../../.devdata/smokeinput.exe .", windowExe, err)
	}

	killLeftoverRecorders(t)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "chars.txt")
	readyPath := filepath.Join(dir, "ready")

	// Start the window and wait for it to take focus.
	win := exec.Command(windowExe, outPath, readyPath)
	if err := win.Start(); err != nil {
		t.Fatalf("starting the recording window: %v", err)
	}
	defer func() {
		_ = win.Process.Kill()
		_, _ = win.Process.Wait()
		// Let the window really disappear before the next test starts its own,
		// otherwise the two race for the foreground.
		time.Sleep(300 * time.Millisecond)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the recording window never reported that it was ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A short settle after the ready file, so the focus change has landed.
	time.Sleep(200 * time.Millisecond)

	// The host runs with the real Windows platform: only the launcher, shell,
	// media, power and metrics adapters are stubbed, because this test is about
	// input and nothing else should touch the machine.
	real := platform.New()
	plat := &platform.Platform{
		OSName:   real.OSName,
		Input:    real.Input, // the real SendInput implementation
		Launcher: &platform.Unsupported{},
		Shell:    &platform.Unsupported{},
		Media:    &platform.Unsupported{},
		Power:    &platform.Unsupported{},
		Metrics:  &staticMetrics{},
	}

	ts := newTestServerPlatform(t, plat)

	// A profile whose button types a known string through the real input layer.
	const want = "mobiledeck-e2e"
	if err := writeProfile(t, ts.srv.cfg.ProfilesDir, "e2e", e2eProfileJSON); err != nil {
		t.Fatalf("writing the e2e profile: %v", err)
	}
	if err := ts.prof.Reload(); err != nil {
		t.Fatalf("reloading profiles: %v", err)
	}

	token := ts.pair(t, "e2e-device")
	c := ts.connect(t, token, "e2e-device")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	reply, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "e2e", PageID: "home", ButtonID: "type",
		Press: proto.PressInfo{Kind: "short"},
	}, 15*time.Second)
	if err != nil {
		t.Fatalf("button.press: %v", err)
	}
	var result proto.ActionResultPayload
	if err := json.Unmarshal(reply.Payload, &result); err != nil {
		t.Fatalf("decoding action.result: %v", err)
	}
	if !result.OK {
		t.Fatalf("the typing action failed: %+v", result.Error)
	}

	// The window writes each character as it arrives, so a short settle is all
	// that is needed for the last one to land.
	got := waitForText(t, outPath, want, 5*time.Second)
	if got != want {
		if log, err := os.ReadFile(outPath + ".log"); err == nil {
			lines := strings.Split(strings.TrimSpace(string(log)), "\n")
			if len(lines) > 12 {
				lines = lines[:12]
			}
			t.Logf("the window saw these messages:\n%s", strings.Join(lines, "\n"))
		}
		t.Fatalf("the window received %q, want %q\n"+
			"a mismatch means the SendInput layout or the virtual-key codes are wrong", got, want)
	}
	t.Logf("the real window received %q through the real SendInput path", got)
}

// TestEndToEndRealShortcut checks that a chord reaches a real window as a
// modified keystroke rather than as two unrelated keys.
func TestEndToEndRealShortcut(t *testing.T) {
	if os.Getenv("MOBILEDECK_E2E_INPUT") == "" {
		t.Skip("set MOBILEDECK_E2E_INPUT=1 to run the real-keystroke test")
	}

	windowExe := os.Getenv("MOBILEDECK_E2E_WINDOW")
	if windowExe == "" {
		windowExe = filepath.Join("..", "..", "..", ".devdata", "smokeinput.exe")
	}
	if _, err := os.Stat(windowExe); err != nil {
		t.Skipf("the recording window is not built at %s", windowExe)
	}

	killLeftoverRecorders(t)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "chars.txt")
	readyPath := filepath.Join(dir, "ready")

	win := exec.Command(windowExe, outPath, readyPath)
	if err := win.Start(); err != nil {
		t.Fatalf("starting the recording window: %v", err)
	}
	defer func() {
		_ = win.Process.Kill()
		_, _ = win.Process.Wait()
		time.Sleep(300 * time.Millisecond)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the recording window never became ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	real := platform.New()
	plat := &platform.Platform{
		OSName: real.OSName, Input: real.Input,
		Launcher: &platform.Unsupported{}, Shell: &platform.Unsupported{},
		Media: &platform.Unsupported{}, Power: &platform.Unsupported{},
		Metrics: &staticMetrics{},
	}
	ts := newTestServerPlatform(t, plat)

	// SHIFT+A must arrive as an uppercase A: that proves the modifier was held
	// down across the key rather than pressed and released around it.
	if err := writeProfile(t, ts.srv.cfg.ProfilesDir, "e2e", e2eShortcutProfileJSON); err != nil {
		t.Fatalf("writing the profile: %v", err)
	}
	if err := ts.prof.Reload(); err != nil {
		t.Fatalf("reloading profiles: %v", err)
	}

	token := ts.pair(t, "e2e-shortcut")
	c := ts.connect(t, token, "e2e-shortcut")
	if _, err := c.hello(); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	if _, err := c.request(proto.TypeButtonPress, proto.ButtonPressPayload{
		ProfileID: "e2e", PageID: "home", ButtonID: "shift-a",
		Press: proto.PressInfo{Kind: "short"},
	}, 15*time.Second); err != nil {
		t.Fatalf("button.press: %v", err)
	}

	got := waitForText(t, outPath, "A", 5*time.Second)
	if !strings.Contains(got, "A") {
		t.Fatalf("the window received %q, want it to contain \"A\"; a lowercase \"a\" would mean the SHIFT modifier was not held", got)
	}
	t.Logf("the real window received %q for a SHIFT+A chord", got)
}

// killLeftoverRecorders terminates any recording window left behind by an
// earlier run and waits for it to exit.
//
// Two recorders alive at once both try to take the foreground, and the one that
// loses the race types into the other, which shows up as a spurious failure. The
// process is killed by image name rather than by handle because a previous run's
// handle is gone.
func killLeftoverRecorders(t *testing.T) {
	t.Helper()
	const image = "smokeinput.exe"

	for range 40 {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+image, "/NH").Output()
		if err != nil || !strings.Contains(string(out), image) {
			return
		}
		_ = exec.Command("taskkill", "/IM", image, "/F").Run()
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("a leftover recording window did not exit; the test may be unreliable")
}

// waitForText polls the recording file until it contains want, or the deadline
// passes.
func waitForText(t *testing.T, path, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			last = string(raw)
			if last == want {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// e2eProfileJSON types a known string through the real input layer.
const e2eProfileJSON = `{
  "schema": 1,
  "id": "e2e",
  "name": "E2E",
  "icon": { "type": "emoji", "value": "E" },
  "settings": { "grid": { "columns": 2, "rows": 2 } },
  "pages": [{
    "id": "home",
    "name": "Home",
    "buttons": [{
      "id": "type",
      "label": "Type",
      "icon": { "type": "emoji", "value": "T" },
      "cell": { "row": 0, "column": 0 },
      "state": { "type": "momentary" },
      "on_press": { "type": "keyboard.text", "params": { "text": "mobiledeck-e2e", "interval_ms": 10 } }
    }]
  }]
}`

// e2eShortcutProfileJSON sends a modified keystroke.
const e2eShortcutProfileJSON = `{
  "schema": 1,
  "id": "e2e",
  "name": "E2E",
  "icon": { "type": "emoji", "value": "E" },
  "settings": { "grid": { "columns": 2, "rows": 2 } },
  "pages": [{
    "id": "home",
    "name": "Home",
    "buttons": [{
      "id": "shift-a",
      "label": "Shift A",
      "icon": { "type": "emoji", "value": "A" },
      "cell": { "row": 0, "column": 0 },
      "state": { "type": "momentary" },
      "on_press": { "type": "keyboard.shortcut", "params": { "keys": ["SHIFT", "A"] } }
    }]
  }]
}`
