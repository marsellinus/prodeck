package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// mediaControl covers every media and volume verb through one implementation.
//
// A single type with a verb table rather than eleven near-identical types: the
// eleven types would each be four lines of boilerplate and would have to be
// registered, documented and tested individually, with nothing gained.
type mediaControl struct {
	Base
	plat *platform.Platform
	verb string
}

// noParams is the empty parameter object, accepted by every verb that takes
// none. It is a struct rather than `any` so strict decoding still rejects a
// misspelled field.
type noParams struct{}

func (a *mediaControl) Validate(raw json.RawMessage) error {
	switch a.verb {
	case "volume.set":
		var p struct {
			Level int `json:"level"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return err
		}
		if p.Level < 0 || p.Level > 100 {
			return fmt.Errorf("level must be 0..100, got %d", p.Level)
		}
		return nil
	default:
		var p noParams
		return decodeParams(raw, &p)
	}
}

func (a *mediaControl) Run(ctx context.Context, req Request) (Result, error) {
	switch a.verb {
	case "media.play":
		return mediaResult("play", a.plat.Media.Play(ctx))
	case "media.pause":
		return mediaResult("pause", a.plat.Media.Pause(ctx))
	case "media.play_pause":
		return mediaResult("play/pause", a.plat.Media.PlayPause(ctx))
	case "media.stop":
		return mediaResult("stop", a.plat.Media.Stop(ctx))
	case "media.next":
		return mediaResult("next", a.plat.Media.Next(ctx))
	case "media.previous":
		return mediaResult("previous", a.plat.Media.Previous(ctx))
	case "volume.up":
		return mediaResult("volume up", a.plat.Media.VolumeUp(ctx))
	case "volume.down":
		return mediaResult("volume down", a.plat.Media.VolumeDown(ctx))
	case "volume.mute":
		return mediaResult("mute toggle", a.plat.Media.Mute(ctx))
	case "volume.set":
		var p struct {
			Level int `json:"level"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return Result{}, err
		}
		return mediaResult(fmt.Sprintf("volume %d%%", p.Level), a.plat.Media.SetVolume(ctx, p.Level))
	default:
		return Result{}, fmt.Errorf("%w: media verb %q", ErrUnknownAction, a.verb)
	}
}

func mediaResult(detail string, err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	return Result{Detail: detail}, nil
}

// nowPlaying is not a button action; it is polled by the telemetry loop to feed
// a `status` button. It exists as an action so the CLI can query it too.
type nowPlaying struct {
	Base
	plat *platform.Platform
}

func (a *nowPlaying) Validate(raw json.RawMessage) error {
	var p noParams
	return decodeParams(raw, &p)
}

func (a *nowPlaying) Run(ctx context.Context, req Request) (Result, error) {
	np, ok, err := a.plat.Media.NowPlaying(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{Output: map[string]any{"playing": ok, "track": np}}, nil
}

// mediaVerbs is the verb table, kept next to the implementation so the
// registration and the switch cannot drift apart.
var mediaVerbs = []struct {
	verb  string
	scope auth.Scope
}{
	{"media.play", auth.ScopeMedia},
	{"media.pause", auth.ScopeMedia},
	{"media.play_pause", auth.ScopeMedia},
	{"media.stop", auth.ScopeMedia},
	{"media.next", auth.ScopeMedia},
	{"media.previous", auth.ScopeMedia},
	{"volume.up", auth.ScopeMedia},
	{"volume.down", auth.ScopeMedia},
	{"volume.mute", auth.ScopeMedia},
	{"volume.set", auth.ScopeMedia},
}

func registerMedia(r *Registry, plat *platform.Platform) error {
	for _, v := range mediaVerbs {
		if err := r.Register(&mediaControl{
			Base: NewBase(v.verb, v.scope),
			plat: plat,
			verb: v.verb,
		}); err != nil {
			return err
		}
	}
	return r.Register(&nowPlaying{Base: NewBase("media.now_playing", auth.ScopeMedia), plat: plat})
}
