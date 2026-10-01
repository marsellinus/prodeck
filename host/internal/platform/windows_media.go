//go:build windows

package platform

import (
	"context"
	"fmt"
)

// windowsMedia drives the OS media session through the media virtual keys.
//
// Windows has no MPRIS equivalent: the documented way for a program to control
// "whatever is playing" is the VK_MEDIA_* / VK_VOLUME_* virtual keys, which the
// shell routes to the foreground media app. That is also what a keyboard's
// media buttons send, so behaviour matches hardware.
type windowsMedia struct {
	Unsupported
	input *windowsInput
}

func (m windowsMedia) tap(ctx context.Context, key Key) error {
	if m.input == nil {
		return fmt.Errorf("platform: media input layer is not initialised")
	}
	return m.input.Key(ctx, key, KeyPress)
}

func (m windowsMedia) PlayPause(ctx context.Context) error { return m.tap(ctx, "MEDIAPLAYPAUSE") }
func (m windowsMedia) Play(ctx context.Context) error      { return m.tap(ctx, "MEDIAPLAYPAUSE") }
func (m windowsMedia) Pause(ctx context.Context) error     { return m.tap(ctx, "MEDIAPLAYPAUSE") }
func (m windowsMedia) Stop(ctx context.Context) error      { return m.tap(ctx, "MEDIASTOP") }
func (m windowsMedia) Next(ctx context.Context) error      { return m.tap(ctx, "MEDIANEXT") }
func (m windowsMedia) Previous(ctx context.Context) error  { return m.tap(ctx, "MEDIAPREV") }
func (m windowsMedia) VolumeUp(ctx context.Context) error  { return m.tap(ctx, "VOLUMEUP") }
func (m windowsMedia) VolumeDown(ctx context.Context) error {
	return m.tap(ctx, "VOLUMEDOWN")
}
func (m windowsMedia) Mute(ctx context.Context) error { return m.tap(ctx, "VOLUMEMUTE") }

// SetVolume is not implemented: Windows exposes no supported API for setting an
// absolute output level (the audio endpoint volume interfaces are COM-only and
// would let a profile pin the volume without the user seeing the change), and
// pretending to succeed by sending N volume-up keys would be a lie about the
// resulting level.
func (m windowsMedia) SetVolume(context.Context, int) error {
	return fmt.Errorf("%w: Windows can step the volume up and down, but not set an absolute level", ErrUnsupported)
}

// NowPlaying is not implemented: the media keys are fire-and-forget, and
// reading the current track would require the SMTC session manager, which has
// no Go binding and changes shape between Windows versions.
func (m windowsMedia) NowPlaying(context.Context) (NowPlaying, bool, error) {
	return NowPlaying{}, false, fmt.Errorf("%w: Windows media keys do not report what is playing", ErrUnsupported)
}
