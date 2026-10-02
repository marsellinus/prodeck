//go:build linux

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

// linuxSound plays a file through the first audio tool that is installed.
//
// There is no single "play this file" API on Linux: the desktop's sound server
// is PulseAudio or PipeWire depending on the distribution, and each ships its
// own CLI. The tools are tried in an order that prefers the one that knows the
// user's actual default sink, and aplay is last among the simple ones because it
// talks to ALSA directly and can only decode WAV.
//
// A tool that is not installed is skipped rather than reported: a host with
// PipeWire and no pulseaudio is the normal case, not an error.
type linuxSound struct {
	Unsupported
}

// soundTool describes one player and how to invoke it.
type soundTool struct {
	name string
	// args builds the command line. volume < 0 means the caller did not ask for
	// a level, so the tool's own default is used.
	args func(path string, volume int) []string
	// handles reports whether the tool can decode this extension.
	handles func(ext string) bool
}

// allFormats is the set of extensions the sounds directory accepts.
func allFormats(string) bool { return true }

// wavOnly is for aplay, which talks to ALSA and decodes WAV and nothing else.
func wavOnly(ext string) bool { return ext == ".wav" }

// simpleFormats is what libsndfile decodes, which is what paplay and pw-play
// use. m4a/aac are deliberately absent: libsndfile has no MP4 demuxer.
func simpleFormats(ext string) bool {
	switch ext {
	case ".wav", ".mp3", ".ogg", ".flac", ".opus":
		return true
	}
	return false
}

// soundTools is tried in order. The first installed tool that can decode the
// file wins.
var soundTools = []soundTool{
	{
		name: "paplay",
		args: func(path string, volume int) []string {
			if volume < 0 {
				return []string{path}
			}
			// paplay takes 0..65536, where 65536 is unity gain.
			return []string{"--volume=" + strconv.Itoa(volume*65536/100), path}
		},
		handles: simpleFormats,
	},
	{
		name: "pw-play",
		args: func(path string, volume int) []string {
			if volume < 0 {
				return []string{path}
			}
			return []string{"--volume=" + strconv.FormatFloat(float64(volume)/100, 'f', 2, 64), path}
		},
		handles: simpleFormats,
	},
	{
		name: "aplay",
		args: func(path string, _ int) []string {
			// aplay has no volume flag at all; the level is the sink's.
			return []string{"-q", path}
		},
		handles: wavOnly,
	},
	{
		name: "ffplay",
		args: func(path string, volume int) []string {
			args := []string{"-nodisp", "-autoexit", "-loglevel", "quiet", "-hide_banner"}
			if volume >= 0 {
				args = append(args, "-volume", strconv.Itoa(volume))
			}
			return append(args, path)
		},
		handles: allFormats,
	},
	{
		name: "mpv",
		args: func(path string, volume int) []string {
			args := []string{"--no-video", "--really-quiet", "--no-terminal"}
			if volume >= 0 {
				args = append(args, "--volume="+strconv.Itoa(volume))
			}
			return append(args, path)
		},
		handles: allFormats,
	},
}

// soundToolFor picks the tool to use for a file, or reports why none can.
func soundToolFor(path string) (soundTool, error) {
	ext := strings.ToLower(filepath.Ext(path))
	installed := 0
	for _, tool := range soundTools {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		installed++
		if tool.handles(ext) {
			return tool, nil
		}
	}
	if installed == 0 {
		return soundTool{}, fmt.Errorf("%w: none of %s is installed, so this host cannot play a sound; install one of them (pipewire-bin or pulseaudio-utils are the usual packages)",
			ErrUnsupported, soundToolNames())
	}
	return soundTool{}, fmt.Errorf("%w: the installed players (%s) cannot decode %s; install ffmpeg or mpv for that format",
		ErrUnsupported, soundToolNames(), ext)
}

func soundToolNames() string {
	names := make([]string, 0, len(soundTools))
	for _, t := range soundTools {
		names = append(names, t.name)
	}
	return strings.Join(names, ", ")
}

// Available reports whether at least one player is installed.
func (linuxSound) SoundAvailable() bool {
	for _, tool := range soundTools {
		if _, err := exec.LookPath(tool.name); err == nil {
			return true
		}
	}
	return false
}

func (linuxSound) PlayFile(ctx context.Context, path string, volume int, blocking bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("platform: %s cannot be played: %w", path, err)
	}
	tool, err := soundToolFor(path)
	if err != nil {
		return err
	}

	if !blocking {
		// Detached, so the press returns as soon as playback starts and the
		// child is not killed when the action's context ends. StartDetached
		// reaps it, so a short sound leaves no zombie behind.
		return StartDetached(DetachedCommand(tool.name, tool.args(path, volume)...))
	}

	// Blocking playback is owned work: it is bound to the action's context so a
	// cancelled press stops the sound instead of playing on unattended.
	cmd := CommandContext(ctx, tool.name, tool.args(path, volume)...)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("platform: %s could not play %s: %w", tool.name, path, err)
	}
	return nil
}
