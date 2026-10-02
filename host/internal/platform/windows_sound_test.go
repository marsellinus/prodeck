//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWindowsSoundRefusesNonWAV is the format gate. PlaySoundW decodes only WAV,
// so any other extension the sounds directory accepts must be refused by name
// rather than handed to the system and silently ignored.
func TestWindowsSoundRefusesNonWAV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boom.mp3")
	if err := os.WriteFile(path, []byte("not really an mp3"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	err := windowsSound{}.PlayFile(context.Background(), path, 80, false)
	if err == nil {
		t.Fatal("a non-WAV file was accepted")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("error = %v, want it to wrap ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), ".mp3") {
		t.Errorf("the error does not name the format: %v", err)
	}
}

// TestWindowsSoundRefusesMissingFile checks that a stale reference is reported
// before the DLL is called, rather than as an opaque PlaySound failure.
func TestWindowsSoundRefusesMissingFile(t *testing.T) {
	err := windowsSound{}.PlayFile(context.Background(), filepath.Join(t.TempDir(), "gone.wav"), 80, false)
	if err == nil {
		t.Fatal("a missing file was accepted")
	}
	if !strings.Contains(err.Error(), "cannot be played") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestWindowsSoundHonoursCancellation covers the blocking path: a cancelled
// context must stop the call rather than wait for the file to finish.
func TestWindowsSoundHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	path := filepath.Join(dir, "boom.wav")
	if err := os.WriteFile(path, wavHeader(), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if err := (windowsSound{}).PlayFile(ctx, path, 80, true); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// TestWindowsSoundReportsAnIgnoredVolume pins the honesty contract: the adapter
// must declare that it cannot set a level, which is what makes the action's
// detail admit it instead of claiming one was applied.
func TestWindowsSoundReportsAnIgnoredVolume(t *testing.T) {
	if !(windowsSound{}).VolumeIgnored() {
		t.Error("windowsSound does not report that it ignores the volume")
	}
	if !(windowsSound{}).SoundAvailable() {
		t.Error("PlaySound ships with Windows, so sound must be advertised as available")
	}
}

// wavHeader is a minimal RIFF/WAVE header, enough for a file to exist and be a
// WAV by extension. The tests never decode it.
func wavHeader() []byte {
	raw := make([]byte, 44)
	copy(raw[0:4], "RIFF")
	copy(raw[8:12], "WAVE")
	return raw
}
