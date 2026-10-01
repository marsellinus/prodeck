//go:build linux

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// MPRIS2 names. Every compliant player exports the same interface at a
// per-player object path, which is what lets one button control "whatever is
// playing" without knowing the player.
const (
	mprisPrefix   = "org.mpris.MediaPlayer2"
	mprisPath     = dbus.ObjectPath("/org/mpris/MediaPlayer2")
	mprisPlayerIf = "org.mpris.MediaPlayer2.Player"
	mprisPropsIf  = "org.freedesktop.DBus.Properties"
)

// busTimeout bounds one D-Bus round trip. A player that has stopped responding
// must not hold up the button press.
const busTimeout = 3 * time.Second

// linuxMedia drives the desktop's media session.
//
// Playback control is MPRIS over the session bus. Volume is not part of MPRIS
// (players expose their own volume, not the sink's), so it is done with the
// desktop's mixer CLI, trying the PipeWire/PulseAudio tools before the older
// ALSA one.
type linuxMedia struct {
	Unsupported

	mu   sync.Mutex
	conn *dbus.Conn
}

// bus returns a session-bus connection, opening it on first use. The caller
// holds mu.
func (m *linuxMedia) bus() (*dbus.Conn, error) {
	if m.conn != nil {
		return m.conn, nil
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: no session bus is reachable, so media players cannot be controlled (%v)", ErrUnsupported, err)
	}
	m.conn = conn
	return conn, nil
}

// player picks the MPRIS player to control: the first one that is actually
// playing, otherwise the first one that exists, so a button still works when
// the player is paused.
func (m *linuxMedia) player(ctx context.Context) (dbus.BusObject, error) {
	conn, err := m.bus()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, busTimeout)
	defer cancel()

	var names []string
	if call := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0); call.Err != nil {
		return nil, fmt.Errorf("platform: listing D-Bus names: %w", call.Err)
	} else if err := call.Store(&names); err != nil {
		return nil, fmt.Errorf("platform: listing D-Bus names: %w", err)
	}

	var first dbus.BusObject
	for _, name := range names {
		if !strings.HasPrefix(name, mprisPrefix+".") {
			continue
		}
		obj := conn.Object(name, mprisPath)
		if first == nil {
			first = obj
		}
		var status string
		call := obj.CallWithContext(ctx, mprisPropsIf+".Get", 0, mprisPlayerIf, "PlaybackStatus")
		if call.Err != nil {
			continue
		}
		if err := call.Store(&status); err != nil {
			continue
		}
		if status == "Playing" {
			return obj, nil
		}
	}
	if first == nil {
		return nil, fmt.Errorf("%w: no MPRIS media player is running", ErrUnsupported)
	}
	return first, nil
}

// call invokes one method on the chosen player.
func (m *linuxMedia) call(ctx context.Context, method string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, err := m.player(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, busTimeout)
	defer cancel()
	if call := obj.CallWithContext(ctx, mprisPlayerIf+"."+method, 0); call.Err != nil {
		return fmt.Errorf("platform: %s refused: %w", method, call.Err)
	}
	return nil
}

func (m *linuxMedia) PlayPause(ctx context.Context) error { return m.call(ctx, "PlayPause") }
func (m *linuxMedia) Play(ctx context.Context) error      { return m.call(ctx, "Play") }
func (m *linuxMedia) Pause(ctx context.Context) error     { return m.call(ctx, "Pause") }
func (m *linuxMedia) Stop(ctx context.Context) error      { return m.call(ctx, "Stop") }
func (m *linuxMedia) Next(ctx context.Context) error      { return m.call(ctx, "Next") }
func (m *linuxMedia) Previous(ctx context.Context) error  { return m.call(ctx, "Previous") }

// NowPlaying reads the current track.
func (m *linuxMedia) NowPlaying(ctx context.Context) (NowPlaying, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, err := m.player(ctx)
	if err != nil {
		return NowPlaying{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, busTimeout)
	defer cancel()

	call := obj.CallWithContext(ctx, mprisPropsIf+".Get", 0, mprisPlayerIf, "Metadata")
	if call.Err != nil {
		return NowPlaying{}, false, fmt.Errorf("platform: reading track metadata: %w", call.Err)
	}
	// The property comes back as a variant holding a{sv}.
	var raw dbus.Variant
	if err := call.Store(&raw); err != nil {
		return NowPlaying{}, false, fmt.Errorf("platform: reading track metadata: %w", err)
	}
	meta, ok := raw.Value().(map[string]dbus.Variant)
	if !ok {
		return NowPlaying{}, false, nil
	}

	np := NowPlaying{
		Title:  variantString(meta["xesam:title"]),
		Album:  variantString(meta["xesam:album"]),
		Artist: variantStringList(meta["xesam:artist"]),
	}

	var status string
	if call := obj.CallWithContext(ctx, mprisPropsIf+".Get", 0, mprisPlayerIf, "PlaybackStatus"); call.Err == nil {
		_ = call.Store(&status)
	}
	np.Playing = status == "Playing"
	if np.Title == "" && np.Artist == "" && !np.Playing {
		// A player with no track loaded is not "now playing" anything.
		return NowPlaying{}, false, nil
	}
	return np, true, nil
}

func variantString(v dbus.Variant) string {
	s, _ := v.Value().(string)
	return s
}

// variantStringList handles xesam:artist, which the spec declares as a list but
// which some players send as a bare string.
func variantStringList(v dbus.Variant) string {
	switch value := v.Value().(type) {
	case []string:
		return strings.Join(value, ", ")
	case string:
		return value
	}
	return ""
}

// mixer describes one volume-control tool and how to build its arguments.
type mixer struct {
	name string
	// args builds the argument list for one action.
	args func(action string, percent int) []string
}

// mixers are tried in order: the PipeWire/PulseAudio tools know about the
// desktop's actual default sink, which amixer does not.
var mixers = []mixer{
	{
		name: "pactl",
		args: func(action string, percent int) []string {
			switch action {
			case "up":
				return []string{"set-sink-volume", "@DEFAULT_SINK@", "+5%"}
			case "down":
				return []string{"set-sink-volume", "@DEFAULT_SINK@", "-5%"}
			case "mute":
				return []string{"set-sink-mute", "@DEFAULT_SINK@", "toggle"}
			default:
				return []string{"set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%d%%", percent)}
			}
		},
	},
	{
		name: "wpctl",
		args: func(action string, percent int) []string {
			switch action {
			case "up":
				return []string{"set-volume", "@DEFAULT_AUDIO_SINK@", "5%+"}
			case "down":
				return []string{"set-volume", "@DEFAULT_AUDIO_SINK@", "5%-"}
			case "mute":
				return []string{"set-mute", "@DEFAULT_AUDIO_SINK@", "toggle"}
			default:
				return []string{"set-volume", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%d%%", percent)}
			}
		},
	},
	{
		name: "amixer",
		args: func(action string, percent int) []string {
			switch action {
			case "up":
				return []string{"-D", "pulse", "sset", "Master", "5%+"}
			case "down":
				return []string{"-D", "pulse", "sset", "Master", "5%-"}
			case "mute":
				return []string{"-D", "pulse", "sset", "Master", "toggle"}
			default:
				return []string{"-D", "pulse", "sset", "Master", fmt.Sprintf("%d%%", percent)}
			}
		},
	},
}

// volume runs one volume action through the first mixer that is installed and
// succeeds.
func (m *linuxMedia) volume(ctx context.Context, action string, percent int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var lastErr error
	for _, tool := range mixers {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		cmd := CommandContext(ctx, tool.name, tool.args(action, percent)...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = fmt.Errorf("%s %s: %w (%s)", tool.name, action, err, firstLine(string(out)))
	}
	if lastErr != nil {
		return fmt.Errorf("platform: volume control failed: %w", lastErr)
	}
	return fmt.Errorf("%w: none of pactl, wpctl or amixer is installed, so the volume cannot be changed", ErrUnsupported)
}

func (m *linuxMedia) VolumeUp(ctx context.Context) error   { return m.volume(ctx, "up", 0) }
func (m *linuxMedia) VolumeDown(ctx context.Context) error { return m.volume(ctx, "down", 0) }
func (m *linuxMedia) Mute(ctx context.Context) error       { return m.volume(ctx, "mute", 0) }

// SetVolume sets an absolute level.
func (m *linuxMedia) SetVolume(ctx context.Context, percent int) error {
	if percent < 0 || percent > 100 {
		return fmt.Errorf("platform: volume %d must be within 0..100", percent)
	}
	return m.volume(ctx, "set", percent)
}
