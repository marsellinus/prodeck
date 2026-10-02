// Package app assembles a runnable host from a configuration.
//
// It exists so the command line and the desktop GUI build the host the same way.
// The GUI must not have its own wiring: a second assembly path is a second place
// for the security defaults to be applied differently, and the one nobody tests
// is always the one that drifts.
package app

import (
	"path/filepath"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/icons"
	"github.com/mobiledeck/mobiledeck/host/internal/logging"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/profiles"
	"github.com/mobiledeck/mobiledeck/host/internal/server"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
	"github.com/mobiledeck/mobiledeck/host/internal/telemetry"
	"github.com/mobiledeck/mobiledeck/host/internal/tlsutil"
)

// Host is the assembled running host.
//
// It lives here rather than in the CLI so the command line and the desktop GUI
// share one assembly path. Two copies would drift, and the GUI's copy would be
// the one nobody tests.
type Host struct {
	Config   config.Config
	Paths    store.Paths
	Log      *logging.SetupResult
	Auth     *auth.Manager
	Profiles *profiles.Registry
	Engine   *engine.Engine
	Metrics  *telemetry.Collector
	Icons    *icons.Client
	Server   *server.Server
	Lock     *store.Lock
	Platform *platform.Platform
}

// build assembles the host: config, logging, auth, profiles, engine, telemetry,
// server. It returns a fully wired host that has not started listening yet.
func Build(cfg config.Config, paths store.Paths, version string, console bool) (*Host, error) {
	if err := paths.Ensure(); err != nil {
		return nil, err
	}

	logRes, err := logging.Setup(logging.Options{
		Level:      cfg.Logging.Level,
		Format:     cfg.Logging.Format,
		Console:    console && cfg.Logging.Console,
		File:       paths.LogFile(),
		MaxSizeMB:  cfg.Logging.MaxSizeMB,
		MaxBackups: cfg.Logging.MaxBackups,
		RingSize:   2000,
	})
	if err != nil {
		return nil, err
	}
	log := logRes.Logger

	audit, err := auth.OpenAudit(paths.Audit)
	if err != nil {
		logRes.Close()
		return nil, err
	}

	authMgr, err := auth.NewManager(paths.Devices, audit, auth.Options{
		PINLength:     cfg.Pairing.PINLength,
		PINValidFor:   time.Duration(cfg.Pairing.PINTTLSeconds) * time.Second,
		MaxAttempts:   cfg.Pairing.MaxAttempts,
		AttemptWindow: 10 * time.Minute,
		LockoutFor:    10 * time.Minute,
		PerSecond:     cfg.RateLimit.PerSecond,
		Burst:         cfg.RateLimit.Burst,
	})
	if err != nil {
		logRes.Close()
		return nil, err
	}

	plat := platform.New()
	metrics := telemetry.New(plat, log)

	// The icon cache. A failure here is not fatal: it only means image icons
	// cannot be resolved, and every other feature works, so the host reports it
	// and carries on with a nil client that resolves nothing.
	iconClient, iconErr := icons.New(filepath.Join(paths.Root, "icons"), log)
	if iconErr != nil {
		log.Warn("the icon cache could not be opened; image icons will render as placeholders",
			"error", iconErr)
		iconClient = nil
	}

	// The engine is created before the registry so the registry can close over
	// it, and the profile registry is created after the registry so it can
	// validate against the real action set. That order is the whole coupling
	// between "what a profile may reference" and "what this host can run".
	var h *Host
	emit := func(ev engine.Event) {
		if h != nil && h.Server != nil {
			h.Server.EmitEvent(ev)
		}
	}

	eng := engine.New(nil, plat, log, engine.Options{
		MaxConcurrent:      cfg.Engine.MaxConcurrentActions,
		QueueDepth:         cfg.Engine.QueueDepth,
		DefaultTimeout:     cfg.DefaultActionTimeout(),
		MaxMacroSteps:      cfg.Engine.MaxMacroSteps,
		MaxMacroTimeout:    time.Duration(cfg.Engine.MaxMacroDurationMS) * time.Millisecond,
		AllowAbsolutePaths: cfg.AllowAbsolutePaths,
		ScriptRoots:        []string{cfg.ScriptsDir},
	}, audit.Record, emit)

	reg, err := engine.BuildRegistry(plat, cfg.HostName, version, eng)
	if err != nil {
		logRes.Close()
		return nil, err
	}
	eng.SetRegistry(reg)

	var profReg *profiles.Registry
	profReg, err = profiles.New(profiles.Options{
		Dir:     cfg.ProfilesDir,
		Actions: profiles.BuildActionSet(reg),
		Watch:   true,
		OnChange: func(id, rev string) {
			emit(engine.Event{Type: "event.profile.changed", Payload: map[string]string{"profile_id": id, "revision": rev}})
		},
	}, log)
	if err != nil {
		logRes.Close()
		return nil, err
	}

	// Navigation actions reject a target that does not exist, so they need to
	// see the loaded profiles. The lookups are installed here, after the
	// registry exists, for the same reason SetProfileDir is.
	eng.SetProfileDir(profReg.DirOf)
	eng.SetProfileExists(func(id string) bool {
		_, ok := profReg.Get(id)
		return ok
	})
	eng.SetPageExists(func(profileID, pageID string) bool {
		prof, ok := profReg.Get(profileID)
		if !ok {
			return false
		}
		_, ok = prof.Page(pageID)
		return ok
	})
	// The sound action resolves files against the configured directory, and the
	// admin API asks the registry whether a file is still in use before deleting
	// it. Both lookups are injected for the same reason as the profile ones: the
	// engine and the server must not depend on where the loader keeps its state.
	eng.SetSoundsDir(cfg.SoundsDir)
	eng.SetSoundReferenced(profReg.SoundReferenced)

	// Cross-check telemetry bindings against the metrics this host produces, so
	// a profile that reads gpu.usage on a machine without a GPU sampler fails at
	// load rather than showing "--" forever.
	for _, e := range profReg.List() {
		if err := engine.ValidateButtonStates(e.Doc, metrics.Available()); err != nil {
			log.Warn("profile uses a metric this host does not produce", "profile", e.Doc.ID, "error", err)
		}
	}

	var tlsMat *tlsutil.Material
	if cfg.TLS.Enabled {
		dir := paths.TLSDir
		tlsMat, err = tlsutil.LoadOrGenerate(dir, cfg.HostName, cfg.TLS.ValidDays)
		if err != nil {
			profReg.Close()
			logRes.Close()
			return nil, err
		}
	}

	srv, err := server.New(server.Options{
		Config:    cfg,
		Log:       log,
		Auth:      authMgr,
		Profiles:  profReg,
		Engine:    eng,
		Telemetry: metrics,
		Icons:     iconClient,
		TLS:       tlsMat,
	})
	if err != nil {
		profReg.Close()
		logRes.Close()
		return nil, err
	}

	h = &Host{
		Config: cfg, Paths: paths, Log: logRes, Auth: authMgr,
		Profiles: profReg, Engine: eng, Metrics: metrics, Icons: iconClient,
		Server: srv, Platform: plat,
	}
	return h, nil
}

// close releases everything the host holds.
func (h *Host) Close() {
	if h.Profiles != nil {
		h.Profiles.Close()
	}
	if h.Metrics != nil {
		h.Metrics.Close()
	}
	if h.Engine != nil {
		h.Engine.CancelAll()
	}
	if h.Log != nil {
		h.Log.Close()
	}
}
