//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winmm is resolved lazily: a host that never plays a sound should not load the
// multimedia library at all. PlaySound is not wrapped by x/sys, so it is called
// through the DLL directly, exactly as windows_power.go calls LockWorkStation.
var procPlaySound = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")

// PlaySound flags (mmsystem.h).
const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndFilename  = 0x00020000
)

// windowsSound plays a file through PlaySoundW, which is the one audio API the
// Windows shell exposes without COM or a media framework.
//
// The trade-off is deliberate and narrow: PlaySound decodes WAV through the
// system codec and nothing else, and it has no volume parameter at all. A file
// in another format is refused here, by name, rather than accepted and then
// silently ignored — a button that looks configured and produces no noise is the
// worst possible failure for a soundboard.
type windowsSound struct {
	Unsupported
}

// SoundAvailable reports that PlaySound is always present. It is part of
// winmm.dll, which every supported Windows version ships, so there is no
// runtime probe to make.
func (windowsSound) SoundAvailable() bool { return true }

// VolumeIgnored reports that a requested level has no effect: PlaySound has no
// volume parameter, and faking one by scaling samples would mean decoding the
// file ourselves. The action says so in its result rather than claiming a level
// it never applied.
func (windowsSound) VolumeIgnored() bool { return true }

func (windowsSound) PlayFile(ctx context.Context, path string, volume int, blocking bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("platform: %s cannot be played: %w", path, err)
	}
	// PlaySound decodes only WAV. Any other extension the sounds directory
	// accepts would reach the system and fail as a silent no-op, so it is named
	// and refused here instead.
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".wav" {
		return fmt.Errorf("%w: Windows PlaySound decodes only WAV, not %s; convert the file to .wav for a soundboard button",
			ErrUnsupported, ext)
	}

	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("platform: %s is not a valid path: %w", path, err)
	}
	flags := uint32(sndFilename | sndNoDefault)
	if !blocking {
		flags |= sndAsync
	}

	if !blocking {
		// SND_ASYNC returns as soon as playback has begun, which is what makes a
		// press feel instant. SND_NODEFAULT keeps a missing file from falling
		// back to the system beep.
		if r, _, callErr := procPlaySound.Call(uintptr(unsafe.Pointer(ptr)), 0, uintptr(flags)); r == 0 {
			return fmt.Errorf("platform: Windows could not play %s: %w", path, callErr)
		}
		return nil
	}

	// Blocking playback must still honour cancellation. PlaySound is a
	// synchronous C call that cannot be interrupted, so it runs on its own
	// goroutine and a cancelled context stops it with PlaySound(NULL, 0, 0),
	// which is the documented way to silence whatever is playing.
	done := make(chan error, 1)
	go func() {
		r, _, callErr := procPlaySound.Call(uintptr(unsafe.Pointer(ptr)), 0, uintptr(flags))
		if r == 0 {
			done <- fmt.Errorf("platform: Windows could not play %s: %w", path, callErr)
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		procPlaySound.Call(0, 0, 0)
		return ctx.Err()
	}
}
