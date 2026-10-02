//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// darwinSound plays a file with afplay, which is part of macOS itself and
// decodes everything Core Audio does: WAV, AIFF, MP3, AAC/m4a, ALAC and FLAC.
//
// Ogg and Opus are the gap. macOS has no system decoder for them, so a file in
// one of those formats is refused with the format named rather than handed to
// afplay to fail obscurely.
type darwinSound struct {
	Unsupported
}

// darwinUnsupportedExts are the containers Core Audio does not decode.
var darwinUnsupportedExts = map[string]bool{
	".ogg":  true,
	".opus": true,
}

// Available reports whether afplay is present. It ships with macOS, so this is
// effectively always true, but it is checked rather than assumed because the
// action and `doctor` both rely on the answer.
func (darwinSound) SoundAvailable() bool {
	_, err := exec.LookPath("afplay")
	return err == nil
}

func (darwinSound) PlayFile(ctx context.Context, path string, volume int, blocking bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("platform: %s cannot be played: %w", path, err)
	}
	if _, err := exec.LookPath("afplay"); err != nil {
		return fmt.Errorf("%w: afplay is not present, so this host cannot play a sound", ErrUnsupported)
	}
	if ext := strings.ToLower(filepath.Ext(path)); darwinUnsupportedExts[ext] {
		return fmt.Errorf("%w: macOS has no decoder for %s; use wav, mp3, m4a, aac, flac or aiff", ErrUnsupported, ext)
	}

	args := make([]string, 0, 3)
	if volume >= 0 {
		// afplay's -v is a linear gain factor, where 1.0 is the file as
		// recorded. volume is 0..100, so 50 becomes 0.5.
		args = append(args, "-v", strconv.FormatFloat(float64(volume)/100, 'f', 2, 64))
	}
	args = append(args, path)

	if !blocking {
		// Detached and reaped: a press must return immediately, and a sound
		// that finishes must not leave a zombie.
		return StartDetached(DetachedCommand("afplay", args...))
	}

	cmd := CommandContext(ctx, "afplay", args...)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("platform: afplay could not play %s: %w", path, err)
	}
	return nil
}
