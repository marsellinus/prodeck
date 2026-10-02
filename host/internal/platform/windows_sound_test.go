//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWindowsSoundRefusesUndecodableFormat is the format gate. MCI decodes WAV,
// MP3 and WMA through the DirectShow codecs but has no decoder for ogg, flac,
// m4a, aac or opus, so those must be refused by name rather than handed to the
// system and reported as a generic "problem occurred in initializing MCI".
func TestWindowsSoundRefusesUndecodableFormat(t *testing.T) {
	dir := t.TempDir()
	for _, ext := range []string{".ogg", ".flac", ".m4a", ".aac", ".opus"} {
		path := filepath.Join(dir, "boom"+ext)
		if err := os.WriteFile(path, []byte("not really audio"), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}

		err := windowsSound{}.PlayFile(context.Background(), path, 80, false)
		if err == nil {
			t.Fatalf("%s was accepted", ext)
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: error = %v, want it to wrap ErrUnsupported", ext, err)
		}
		if !strings.Contains(err.Error(), ext) {
			t.Errorf("%s: the error does not name the format: %v", ext, err)
		}
	}
}

// TestWindowsSoundRefusesMissingFile checks that a stale reference is reported
// before the DLL is called, rather than as an opaque MCI failure.
func TestWindowsSoundRefusesMissingFile(t *testing.T) {
	err := windowsSound{}.PlayFile(context.Background(), filepath.Join(t.TempDir(), "gone.wav"), 80, false)
	if err == nil {
		t.Fatal("a missing file was accepted")
	}
	if !strings.Contains(err.Error(), "cannot be played") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestWindowsSoundHonoursCancellation covers the blocking path. A blocking play
// runs for the length of the file, so cancelling it must stop and close the
// device rather than wait the sound out. This is the case the old PlaySound
// implementation handled with PlaySound(NULL, 0, 0); the MCI equivalent is
// `stop` followed by `close`.
func TestWindowsSoundHonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.wav")
	// Long enough that a completed play would be obvious in the elapsed time.
	if err := os.WriteFile(path, silenceWAV(4000), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- windowsSound{}.PlayFile(ctx, path, 80, true)
	}()

	// Let playback start, then cancel it mid-sound.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("cancellation took %v; the sound was waited out rather than stopped", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled blocking play never returned")
	}

	// The cancelled device must be closed, not left in MCI's table.
	deadline := time.Now().Add(2 * time.Second)
	for mciOpenDevices() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the cancelled device was left open")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWindowsSoundRejectsAnAlreadyCancelledContext pins the cheap path: a
// context that is already done must return before any device is opened.
func TestWindowsSoundRejectsAnAlreadyCancelledContext(t *testing.T) {
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
	if got := mciOpenDevices(); got != 0 {
		t.Errorf("%d devices open after a cancelled-before-start play, want 0", got)
	}
}

// TestWindowsSoundReportsAnAppliedVolume pins the honesty contract. MCI can set
// a level through setaudio, so the adapter must NOT disclaim it: a result that
// says "this host cannot set the volume" while the level was in fact applied is
// as wrong as claiming one that never was.
func TestWindowsSoundReportsAnAppliedVolume(t *testing.T) {
	if (windowsSound{}).VolumeIgnored() {
		t.Error("windowsSound reports that it ignores the volume, but MCI applies it")
	}
	if !(windowsSound{}).SoundAvailable() {
		t.Error("MCI ships with Windows, so sound must be advertised as available")
	}
}

// TestMCIClosesFinishedDevices checks that a non-blocking play does not leak its
// device: the sweep must close it once the sound has ended. A leaked device
// holds an MCI handle and an alias for the life of the process, so pressing the
// same pad repeatedly would eventually exhaust them.
//
// The file is a real, decodable WAV, because the sweep keys off MCI's own idea
// of whether playback is still running.
func TestMCIClosesFinishedDevices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "short.wav")
	if err := os.WriteFile(path, silenceWAV(120), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if err := (windowsSound{}).PlayFile(context.Background(), path, 50, false); err != nil {
		t.Fatalf("non-blocking play failed: %v", err)
	}
	if got := mciOpenDevices(); got != 1 {
		t.Fatalf("after a non-blocking play there are %d open devices, want 1", got)
	}

	// 120 ms of audio plus a sweep tick or two.
	deadline := time.Now().Add(5 * time.Second)
	for mciOpenDevices() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the finished device was never closed; the sweep is leaking devices")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMCIPlaysConcurrently checks the property a soundboard needs: a second
// press must not cut the first sound off. Two devices are opened and played at
// once, and both must report that they are still playing.
func TestMCIPlaysConcurrently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tone.wav")
	if err := os.WriteFile(path, silenceWAV(1500), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	first, err := mci.open(path, 100)
	if err != nil {
		t.Fatalf("opening the first device: %v", err)
	}
	if err := mci.play(first); err != nil {
		t.Fatalf("playing the first device: %v", err)
	}
	second, err := mci.open(path, 20)
	if err != nil {
		mci.close(first)
		t.Fatalf("opening the second device: %v", err)
	}
	if err := mci.play(second); err != nil {
		mci.close(first)
		mci.close(second)
		t.Fatalf("playing the second device: %v", err)
	}
	defer func() {
		mci.close(first)
		mci.close(second)
	}()

	if first.alias == second.alias {
		t.Fatal("two devices share an alias, so the second would cut the first off")
	}
	for _, dev := range []*mciDevice{first, second} {
		if r := mci.do(`status ` + dev.alias + ` mode`); r.code != 0 || r.text != "playing" {
			t.Errorf("device %s is %q (code %d) while both should be playing", dev.alias, r.text, r.code)
		}
	}
}

// TestMCIErrorMessageIsReal checks that a numeric MCI code is turned into the
// message Windows has for it. A bare code in a button result tells a user
// nothing about what went wrong.
func TestMCIErrorMessageIsReal(t *testing.T) {
	msg := mciErrorText(263) // MCIERR_DEVICE_NOT_OPEN
	if msg == "" || strings.Contains(msg, "263") {
		t.Errorf("mciErrorText(263) = %q, want the message Windows has for it", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "device") {
		t.Errorf("mciErrorText(263) = %q, want it to name the problem", msg)
	}
}

// TestMCIPlaysMP3EndToEnd drives the real adapter with a real MP3 and reports
// the MCI return codes and error strings for each step, then measures a
// blocking play against the file's own length.
//
// This is the test that proves the feature: an MP3 cannot be decoded by
// PlaySound at all, so a blocking call whose duration matches the file can only
// have come from MCI actually playing it. The codes are logged rather than
// asserted because they are the evidence a human reads when this breaks.
func TestMCIPlaysMP3EndToEnd(t *testing.T) {
	path := os.Getenv("MOBILEDECK_TEST_MP3")
	if path == "" {
		t.Skip("set MOBILEDECK_TEST_MP3 to a real MP3 to run this")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("MOBILEDECK_TEST_MP3=%s is not readable: %v", path, err)
	}

	dev, err := mci.open(path, 70)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	openCode := mci.do(`status ` + dev.alias + ` mode`)
	length := mci.do(`status ` + dev.alias + ` length`)
	volume := mci.do(`status ` + dev.alias + ` volume`)
	t.Logf("open: alias=%s mode=%q code=%d", dev.alias, openCode.text, openCode.code)
	t.Logf("status length = %q ms, status volume = %q (of 1000)", length.text, volume.text)
	if openCode.code != 0 || length.code != 0 {
		mci.close(dev)
		t.Fatalf("the device did not answer status queries: mode=%d length=%d", openCode.code, length.code)
	}
	if openCode.text != "stopped" {
		t.Errorf("a freshly opened device is %q, want stopped", openCode.text)
	}
	// 70% must have been applied near 700 of MCI's 1000. MCI stores the level
	// in eight bits, so the value it reads back is quantised: asking for 700
	// reports 707. The check is a tolerance rather than equality because the
	// quantisation is MCI's, not something this code can avoid.
	got, convErr := strconv.Atoi(strings.TrimSpace(volume.text))
	if convErr != nil || got < 690 || got > 710 {
		t.Errorf("status volume = %q, want about 700 for a 70%% request", volume.text)
	}

	fileMS, err := strconv.Atoi(strings.TrimSpace(length.text))
	if err != nil || fileMS <= 0 {
		mci.close(dev)
		t.Fatalf("status length = %q, want milliseconds: %v", length.text, err)
	}

	// The inspection device is closed before the blocking call so the final
	// count reflects only what PlayFile did with its own device.
	mci.close(dev)

	start := time.Now()
	if err := (windowsSound{}).PlayFile(context.Background(), path, 70, true); err != nil {
		t.Fatalf("blocking play failed: %v", err)
	}
	elapsed := time.Since(start)

	// A real 2 s file cannot finish in 200 ms; the tolerance covers codec
	// latency at the start and the sweep tick at the end.
	if elapsed < time.Duration(fileMS)*time.Millisecond/2 {
		t.Errorf("a %d ms file returned after %v; it cannot have been played", fileMS, elapsed)
	}
	t.Logf("blocking play of a %d ms file took %v", fileMS, elapsed.Round(time.Millisecond))

	// The blocking path closes its own device, so nothing may be left open.
	if got := mciOpenDevices(); got != 0 {
		t.Errorf("%d devices left open after a blocking play, want 0", got)
	}
}

// TestMCIReportsAnUnplayableFile checks the error a user sees when MCI cannot
// decode a file that got past the extension gate. The point is that the message
// is mciGetErrorStringW's own words rather than a bare number.
func TestMCIReportsAnUnplayableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.mp3")
	// An .mp3 by name with no MPEG frames in it: the extension gate passes it
	// and MCI has to be the one to refuse.
	if err := os.WriteFile(path, []byte("this is not an MPEG stream at all"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	err := windowsSound{}.PlayFile(context.Background(), path, 50, false)
	if err == nil {
		t.Fatal("a file with no decodable audio was accepted")
	}
	t.Logf("the user sees: %v", err)
	// The message must be Windows' text, not "MCI error 277".
	if !strings.Contains(err.Error(), "MCI") && !strings.Contains(err.Error(), "mci") {
		t.Errorf("error = %v, want it to come from mciGetErrorStringW", err)
	}
	if strings.Contains(err.Error(), "error 277") {
		t.Errorf("error = %v, want the decoded message rather than a bare code", err)
	}
}

// mciOpenDevices reports how many devices are currently registered.
func mciOpenDevices() int {
	mci.mu.Lock()
	defer mci.mu.Unlock()
	return len(mci.devices)
}

// wavHeader is a minimal RIFF/WAVE header, enough for a file to exist and be a
// WAV by extension. The tests never decode it.
func wavHeader() []byte {
	raw := make([]byte, 44)
	copy(raw[0:4], "RIFF")
	copy(raw[8:12], "WAVE")
	return raw
}

// silenceWAV builds a real 16-bit mono WAV of the requested length, so MCI can
// decode it and report how long it is. A header alone is not enough for the
// sweep tests: MCI needs an actual stream to have an opinion about whether
// playback is finished.
func silenceWAV(ms int) []byte {
	const sampleRate = 8000
	samples := sampleRate * ms / 1000
	data := make([]byte, samples*2) // 16-bit mono, already all zero

	raw := make([]byte, 44+len(data))
	copy(raw[0:4], "RIFF")
	putU32(raw[4:8], uint32(36+len(data)))
	copy(raw[8:12], "WAVE")
	copy(raw[12:16], "fmt ")
	putU32(raw[16:20], 16)           // fmt chunk size
	putU16(raw[20:22], 1)            // PCM
	putU16(raw[22:24], 1)            // mono
	putU32(raw[24:28], sampleRate)   // sample rate
	putU32(raw[28:32], sampleRate*2) // byte rate
	putU16(raw[32:34], 2)            // block align
	putU16(raw[34:36], 16)           // bits per sample
	copy(raw[36:40], "data")
	putU32(raw[40:44], uint32(len(data)))
	copy(raw[44:], data)
	return raw
}

func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func putU16(b []byte, v uint16) {
	b[0], b[1] = byte(v), byte(v>>8)
}
