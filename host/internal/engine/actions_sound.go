package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/sounds"
)

// soundPlay plays one file from the host's sounds directory.
//
// The file is resolved inside that directory and nowhere else: a profile is a
// document a user edits by hand and may be shared, so a button that could name
// an arbitrary path would be an arbitrary-file-play primitive. The sounds
// package owns the name rules (a bare file name with an accepted audio
// extension) and this action adds the symlink check on top.
type soundPlay struct {
	Base
	plat   *platform.Platform
	engine *Engine
}

type soundParams struct {
	File string `json:"file"`
	// Volume is a pointer so "absent" and "0" are different requests. Absent
	// means "play at the system level"; 0 means "silent", which a platform that
	// can set a level honours and a platform that cannot must report.
	Volume *int `json:"volume,omitempty"`
	// Blocking defaults to false: a phone press must return as soon as playback
	// starts, not when the sound ends.
	Blocking *bool `json:"blocking,omitempty"`
}

func (a *soundPlay) Validate(raw json.RawMessage) error {
	var p soundParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if _, err := sounds.CleanName(p.File); err != nil {
		return err
	}
	if p.Volume != nil && (*p.Volume < 0 || *p.Volume > 100) {
		return fmt.Errorf("volume must be 0..100, got %d", *p.Volume)
	}
	return nil
}

func (a *soundPlay) Run(ctx context.Context, req Request) (Result, error) {
	var p soundParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	// The Sound adapter is a field on Platform, so a hand-built Platform that
	// predates it would be nil here. A clear unsupported error beats a panic
	// that the server would only turn into a generic 500.
	if a.plat.Sound == nil {
		return Result{}, fmt.Errorf("%w: this host has no sound adapter", platform.ErrUnsupported)
	}
	path, name, err := a.resolve(p.File)
	if err != nil {
		return Result{}, err
	}

	volume := -1
	if p.Volume != nil {
		volume = *p.Volume
	}
	blocking := false
	if p.Blocking != nil {
		blocking = *p.Blocking
	}

	if err := a.plat.Sound.PlayFile(ctx, path, volume, blocking); err != nil {
		return Result{}, err
	}
	return Result{Detail: playDetail(name, volume, a.plat.Sound)}, nil
}

// resolve turns the requested file into an absolute path inside the sounds
// directory, and returns the file name for the result.
//
// The name is validated first (a bare file name with an accepted extension),
// then the resolved path is checked against the directory again after symlinks
// are followed. That second step is what stops a link planted in the directory
// from playing a file somewhere else on the machine (docs/SECURITY.md T7).
func (a *soundPlay) resolve(file string) (path, name string, err error) {
	name, err = sounds.CleanName(file)
	if err != nil {
		return "", "", err
	}
	dir := strings.TrimSpace(a.engine.opts.SoundsDir)
	if dir == "" {
		return "", "", fmt.Errorf("no sounds directory is configured on this host")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("resolving the sounds directory: %w", err)
	}
	root, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return "", "", fmt.Errorf("the sounds directory %s is not usable: %w", absDir, err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		return "", "", fmt.Errorf("%s is not a sound on this host: %w", name, err)
	}
	if !within(root, resolved) {
		return "", "", fmt.Errorf("%s resolves outside the sounds directory", name)
	}
	return resolved, name, nil
}

// within reports whether child is root or lives inside it.
func within(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// volumeReporter is implemented by a Sound adapter that cannot apply a volume
// level. The result says so rather than claiming a level that was never set,
// because a user who asked for 20% and got 100% would otherwise be lied to.
type volumeReporter interface{ VolumeIgnored() bool }

// playDetail describes what happened, including the volume when the platform
// could actually honour it.
func playDetail(name string, volume int, snd platform.Sound) string {
	switch {
	case volume < 0:
		return "played " + name
	case isVolumeIgnored(snd):
		return fmt.Sprintf("played %s (this host cannot set the volume; the system level was used)", name)
	default:
		return fmt.Sprintf("played %s at %d%%", name, volume)
	}
}

func isVolumeIgnored(snd platform.Sound) bool {
	vr, ok := snd.(volumeReporter)
	return ok && vr.VolumeIgnored()
}

func registerSounds(r *Registry, plat *platform.Platform, e *Engine) error {
	return r.Register(&soundPlay{
		Base:   NewBase("sound.play", auth.ScopeMedia),
		plat:   plat,
		engine: e,
	})
}
