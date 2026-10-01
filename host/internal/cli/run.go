package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/logging"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/profiles"
	"github.com/mobiledeck/mobiledeck/host/internal/server"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
	"github.com/mobiledeck/mobiledeck/host/internal/telemetry"
	"github.com/mobiledeck/mobiledeck/host/internal/tlsutil"
)

// runtimeState is what a running host publishes for the CLI to find it.
type runtimeState struct {
	PID        int    `json:"pid"`
	Addr       string `json:"addr"`
	Port       int    `json:"port"`
	AdminToken string `json:"admin_token"`
	StartedAt  int64  `json:"started_at"`
	Version    string `json:"version"`
	ConfigDir  string `json:"config_dir"`
}

// host is the assembled running host, kept in one value so the assembly order
// is visible in one place.
type host struct {
	cfg      config.Config
	paths    store.Paths
	log      *logging.SetupResult
	auth     *auth.Manager
	profiles *profiles.Registry
	engine   *engine.Engine
	metrics  *telemetry.Collector
	srv      *server.Server
	lock     *store.Lock
	plat     *platform.Platform
}

// build assembles the host: config, logging, auth, profiles, engine, telemetry,
// server. It returns a fully wired host that has not started listening yet.
func build(cfg config.Config, paths store.Paths, version string, console bool) (*host, error) {
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

	// The engine is created before the registry so the registry can close over
	// it, and the profile registry is created after the registry so it can
	// validate against the real action set. That order is the whole coupling
	// between "what a profile may reference" and "what this host can run".
	var h *host
	emit := func(ev engine.Event) {
		if h != nil && h.srv != nil {
			h.srv.EmitEvent(ev)
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
		TLS:       tlsMat,
	})
	if err != nil {
		profReg.Close()
		logRes.Close()
		return nil, err
	}

	h = &host{
		cfg: cfg, paths: paths, log: logRes, auth: authMgr,
		profiles: profReg, engine: eng, metrics: metrics, srv: srv, plat: plat,
	}
	return h, nil
}

// close releases everything the host holds.
func (h *host) close() {
	if h.profiles != nil {
		h.profiles.Close()
	}
	if h.metrics != nil {
		h.metrics.Close()
	}
	if h.engine != nil {
		h.engine.CancelAll()
	}
	if h.log != nil {
		h.log.Close()
	}
}

// cmdRun runs the host in the foreground until interrupted.
func cmdRun(env Env, args []string) int {
	fs, g := newFlagSet(env, "run", "Run the MobileDeck host in the foreground.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	cfg, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	// A single-instance lock: two hosts sharing a config directory would fight
	// over the same device store and port.
	lock, err := store.AcquireLock(paths.PIDFile())
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
			return ExitAlreadyUp
		}
		return fail(env, err)
	}
	defer lock.Release()

	h, err := build(cfg, paths, versionOf(env), true)
	if err != nil {
		return fail(env, err)
	}
	defer h.close()

	ctx, wasSignalled, stopSignals := signalContext()
	defer stopSignals()

	// Bind before printing anything: with an ephemeral port the address is not
	// known until the socket exists, and a banner that says "listening" with no
	// port is worse than no banner.
	if err := h.srv.Listen(); err != nil {
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitError
	}

	// Publish the runtime state so `mobiledeck status` and the other
	// subcommands can reach this process, then remove it on the way out.
	if err := writeRuntime(paths.RuntimeFile(), h, env); err != nil {
		h.log.Logger.Warn("could not publish the runtime state; the CLI subcommands will not find this host", "error", err)
	}
	defer os.Remove(paths.RuntimeFile())

	if !g.quiet {
		printStartup(env, h)
	}

	h.log.Logger.Info("host started",
		"version", versionOf(env), "os", runtime.GOOS, "config_dir", paths.Root,
		"profiles_dir", cfg.ProfilesDir, "tls", cfg.TLS.Enabled)

	if err := h.srv.Serve(ctx); err != nil {
		if wasSignalled() {
			return ExitOK
		}
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitError
	}
	if wasSignalled() {
		h.log.Logger.Info("stopped by signal")
	}
	return ExitOK
}

// printStartup writes the human-facing banner.
func printStartup(env Env, h *host) {
	fmt.Fprintf(env.Stdout, "MobileDeck %s\n", versionOf(env))
	fmt.Fprintf(env.Stdout, "  host       %s (%s)\n", h.cfg.HostName, h.cfg.HostID)
	fmt.Fprintf(env.Stdout, "  listening  %s\n", h.srv.Addr())
	for _, addr := range h.srv.LocalAddresses() {
		fmt.Fprintf(env.Stdout, "  reachable  %s\n", addr)
	}
	if h.cfg.TLS.Enabled {
		fmt.Fprintf(env.Stdout, "  tls        on, fingerprint %s\n", h.srv.Fingerprint())
	} else {
		fmt.Fprintf(env.Stdout, "  tls        off\n")
	}
	ids := h.profiles.IDs()
	fmt.Fprintf(env.Stdout, "  profiles   %d (%s)\n", len(ids), strings.Join(ids, ", "))
	fmt.Fprintf(env.Stdout, "  config     %s\n", h.paths.Root)
	fmt.Fprintf(env.Stdout, "\nRun `mobiledeck pair` in another terminal to add a phone.\n\n")
}

// writeRuntime publishes the state the CLI subcommands need.
func writeRuntime(path string, h *host, env Env) error {
	state := runtimeState{
		PID:        os.Getpid(),
		Addr:       h.srv.Addr(),
		Port:       h.srv.Port(),
		AdminToken: h.auth.AdminToken(),
		StartedAt:  time.Now().UnixMilli(),
		Version:    versionOf(env),
		ConfigDir:  h.paths.Root,
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// readRuntime reads the published runtime state.
func readRuntime(path string) (runtimeState, error) {
	var state runtimeState
	raw, err := store.ReadFileOr(path, nil)
	if err != nil {
		return state, err
	}
	if raw == nil {
		return state, errNotRunning
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("mobiledeck: %s is corrupt: %w", path, err)
	}
	return state, nil
}

var errNotRunning = errors.New("no host is running for this configuration directory")

// cmdStart launches the host as a background process.
func cmdStart(env Env, args []string) int {
	fs, g := newFlagSet(env, "start", "Start the MobileDeck host in the background.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	cfg, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	if pid, running, err := store.ReadPID(paths.PIDFile()); err != nil {
		return fail(env, err)
	} else if running {
		fmt.Fprintf(env.Stderr, "mobiledeck: a host is already running (pid %d)\n", pid)
		return ExitAlreadyUp
	}

	exe, err := os.Executable()
	if err != nil {
		return fail(env, fmt.Errorf("cannot determine the executable path: %w", err))
	}

	// Rebuild the argument list from the resolved configuration rather than
	// forwarding the original argv, so the child is not affected by a flag that
	// only made sense in the parent's context.
	childArgs := []string{"run", "--config-dir", paths.Root, "--quiet"}
	if g.bind != "" {
		childArgs = append(childArgs, "--bind", cfg.Bind)
	}
	if g.port != 0 {
		childArgs = append(childArgs, "--port", fmt.Sprint(cfg.Port))
	}

	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return fail(env, fmt.Errorf("cannot start the background host: %w", err))
	}

	// Wait for the child to publish its runtime state, so a subsequent
	// `mobiledeck pair` cannot race it.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := readRuntime(paths.RuntimeFile()); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if _, err := readRuntime(paths.RuntimeFile()); err != nil {
		fmt.Fprintf(env.Stderr, "mobiledeck: the background host did not come up; check %s\n", paths.LogFile())
		return ExitError
	}

	state, _ := readRuntime(paths.RuntimeFile())
	if !g.quiet {
		fmt.Fprintf(env.Stdout, "started (pid %d), listening on %s\n", cmd.Process.Pid, state.Addr)
		fmt.Fprintf(env.Stdout, "logs: %s\n", paths.LogFile())
	}
	return ExitOK
}

// cmdStop stops a background host.
func cmdStop(env Env, args []string) int {
	fs, g := newFlagSet(env, "stop", "Stop a background MobileDeck host.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	pid, running, err := store.ReadPID(paths.PIDFile())
	if err != nil {
		return fail(env, err)
	}
	if !running {
		if !g.quiet {
			fmt.Fprintln(env.Stdout, "no host is running")
		}
		return ExitOK
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fail(env, fmt.Errorf("cannot find pid %d: %w", pid, err))
	}
	if err := terminate(proc); err != nil {
		return fail(env, fmt.Errorf("cannot signal pid %d: %w", pid, err))
	}

	// Give the host a moment to close its sessions cleanly, then report.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, alive, _ := store.ReadPID(paths.PIDFile()); !alive {
			os.Remove(paths.RuntimeFile())
			if !g.quiet {
				fmt.Fprintf(env.Stdout, "stopped (pid %d)\n", pid)
			}
			return ExitOK
		}
		time.Sleep(150 * time.Millisecond)
	}

	fmt.Fprintf(env.Stderr, "mobiledeck: pid %d did not exit; it may be finishing a running action\n", pid)
	return ExitError
}

// cmdRestart stops then starts.
func cmdRestart(env Env, args []string) int {
	fs, g := newFlagSet(env, "restart", "Stop then start the MobileDeck host.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if code := cmdStop(env, args); code != ExitOK && code != ExitNotRunning {
		return code
	}
	_ = fs
	_ = g
	return cmdStart(env, args)
}

// cmdInit creates a default configuration.
func cmdInit(env Env, args []string) int {
	fs, g := newFlagSet(env, "init", "Create a default configuration and exit.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}
	if err := paths.Ensure(); err != nil {
		return fail(env, err)
	}
	if err := config.EnsureExists(paths.Config); err != nil {
		return fail(env, err)
	}
	// Seed an example profile so a first-run user has something to press.
	seeded, err := seedExampleProfile(paths.ProfilesDir)
	if err != nil {
		return fail(env, err)
	}
	if !g.quiet {
		fmt.Fprintf(env.Stdout, "configuration: %s\n", paths.Config)
		fmt.Fprintf(env.Stdout, "profiles:      %s\n", paths.ProfilesDir)
		if seeded {
			fmt.Fprintln(env.Stdout, "seeded the example profile \"development\"")
		}
	}
	return ExitOK
}

// cmdDoctor checks the environment and reports what will not work.
func cmdDoctor(env Env, args []string) int {
	fs, g := newFlagSet(env, "doctor", "Check the environment and report anything that will not work.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	cfg, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	plat := platform.New()
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	var checks []check
	add := func(name string, ok bool, detail string) {
		checks = append(checks, check{Name: name, OK: ok, Detail: detail})
	}

	add("config", true, paths.Config)
	add("profiles directory", true, cfg.ProfilesDir)
	add("writable config directory", dirWritable(paths.Root), paths.Root)

	// Input control is the one capability that genuinely fails on a headless
	// Linux box, so it is probed rather than assumed.
	inErr := probeInput(plat)
	add("input control", inErr == nil, describeErr(inErr, "keyboard and mouse injection will work"))

	add("shell", len(plat.Shell.SupportedInterpreters()) > 0,
		strings.Join(plat.Shell.SupportedInterpreters(), ", "))

	metrics := plat.Metrics.Available()
	add("telemetry", len(metrics) > 0, strings.Join(metrics, ", "))

	caps := plat.Power.Capabilities()
	add("lock screen", caps.Lock, "")
	add("power off / restart", caps.Shutdown && caps.Restart,
		"the agent runs unprivileged and will not elevate; use the desktop's own power controls if this is false")

	if cfg.IsLoopback() {
		add("reachable from a phone", false,
			"the host is bound to loopback; a phone on the LAN cannot reach it, pass --bind 0.0.0.0")
	} else {
		add("reachable from a phone", true, "bound to "+cfg.Bind)
	}

	if !cfg.TLS.Enabled && !cfg.IsLoopback() && !cfg.InsecureAllowPlaintext {
		add("pairing", false, "TLS is off on a LAN bind; pairing will be refused until you enable TLS or pass --insecure-allow-plaintext")
	} else {
		add("pairing", true, "")
	}

	if g.json {
		return emitJSON(env, map[string]any{
			"os":     plat.OSName,
			"checks": checks,
		})
	}

	failed := 0
	for _, c := range checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
			failed++
		}
		if c.Detail != "" {
			fmt.Fprintf(env.Stdout, "%s  %-26s %s\n", mark, c.Name, c.Detail)
		} else {
			fmt.Fprintf(env.Stdout, "%s  %s\n", mark, c.Name)
		}
	}
	if failed > 0 {
		fmt.Fprintf(env.Stdout, "\n%d check(s) need attention.\n", failed)
		return ExitError
	}
	fmt.Fprintln(env.Stdout, "\nall checks passed.")
	return ExitOK
}

// cmdKeys lists the key names a profile may use.
func cmdKeys(env Env, args []string) int {
	fs, g := newFlagSet(env, "keys", "List every key name a profile may use.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	names := platform.KeyNames()
	if g.json {
		return emitJSON(env, map[string]any{"keys": names, "count": len(names)})
	}
	const perLine = 6
	for i := 0; i < len(names); i += perLine {
		end := min(i+perLine, len(names))
		fmt.Fprintln(env.Stdout, strings.Join(names[i:end], "  "))
	}
	fmt.Fprintf(env.Stdout, "\n%d key names\n", len(names))
	return ExitOK
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cmdActions lists the action types this host provides.
func cmdActions(env Env, args []string) int {
	fs, g := newFlagSet(env, "actions", "List every action type this host provides.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	cfg, _, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	plat := platform.New()
	eng := engine.New(nil, plat, nil, engine.Options{}, nil, nil)
	reg, err := engine.BuildRegistry(plat, cfg.HostName, versionOf(env), eng)
	if err != nil {
		return fail(env, err)
	}

	infos := reg.Descriptions()
	if g.json {
		return emitJSON(env, map[string]any{"actions": infos, "count": len(infos)})
	}
	width := 0
	for _, a := range infos {
		if len(a.Type) > width {
			width = len(a.Type)
		}
	}
	for _, a := range infos {
		scope := a.Scope
		if scope == "" {
			scope = "-"
		}
		fmt.Fprintf(env.Stdout, "%-*s  %s\n", width, a.Type, scope)
	}
	fmt.Fprintf(env.Stdout, "\n%d action types; the second column is the scope a device needs\n", len(infos))
	return ExitOK
}

// fail reports an error and returns the generic failure code.
func fail(env Env, err error) int {
	fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
	return ExitError
}

// emitJSON writes a JSON document and returns success.
func emitJSON(env Env, v any) int {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(env, err)
	}
	return ExitOK
}

func describeErr(err error, ok string) string {
	if err == nil {
		return ok
	}
	return err.Error()
}

// dirWritable reports whether a directory exists and accepts writes.
func dirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	probe := filepath.Join(dir, ".mobiledeck-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// probeInput performs a harmless input call to prove the agent can reach the
// session's input stack. It moves the mouse by zero pixels, which every backend
// accepts and no user can perceive.
func probeInput(plat *platform.Platform) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return plat.Input.MouseMove(ctx, 0, 0)
}

var (
	_ = syscall.SIGTERM
	_ = signal.Notify
)
