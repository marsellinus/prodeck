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

	"github.com/mobiledeck/mobiledeck/host/internal/app"
	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
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

	h, err := app.Build(cfg, paths, versionOf(env), true)
	if err != nil {
		return fail(env, err)
	}
	defer h.Close()

	ctx, wasSignalled, stopSignals := signalContext()
	defer stopSignals()

	// Bind before printing anything: with an ephemeral port the address is not
	// known until the socket exists, and a banner that says "listening" with no
	// port is worse than no banner.
	if err := h.Server.Listen(); err != nil {
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitError
	}

	// Publish the runtime state so `mobiledeck status` and the other
	// subcommands can reach this process, then remove it on the way out.
	if err := writeRuntime(paths.RuntimeFile(), h, env); err != nil {
		h.Log.Logger.Warn("could not publish the runtime state; the CLI subcommands will not find this host", "error", err)
	}
	defer os.Remove(paths.RuntimeFile())

	if !g.quiet {
		printStartup(env, h)
	}

	h.Log.Logger.Info("host started",
		"version", versionOf(env), "os", runtime.GOOS, "config_dir", paths.Root,
		"profiles_dir", cfg.ProfilesDir, "tls", cfg.TLS.Enabled)

	if err := h.Server.Serve(ctx); err != nil {
		if wasSignalled() {
			return ExitOK
		}
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitError
	}
	if wasSignalled() {
		h.Log.Logger.Info("stopped by signal")
	}
	return ExitOK
}

// printStartup writes the human-facing banner.
func printStartup(env Env, h *app.Host) {
	fmt.Fprintf(env.Stdout, "MobileDeck %s\n", versionOf(env))
	fmt.Fprintf(env.Stdout, "  host       %s (%s)\n", h.Config.HostName, h.Config.HostID)
	fmt.Fprintf(env.Stdout, "  listening  %s\n", h.Server.Addr())
	for _, addr := range h.Server.LocalAddresses() {
		fmt.Fprintf(env.Stdout, "  reachable  %s\n", addr)
	}
	if h.Config.TLS.Enabled {
		fmt.Fprintf(env.Stdout, "  tls        on, fingerprint %s\n", h.Server.Fingerprint())
	} else {
		fmt.Fprintf(env.Stdout, "  tls        off\n")
	}
	ids := h.Profiles.IDs()
	fmt.Fprintf(env.Stdout, "  profiles   %d (%s)\n", len(ids), strings.Join(ids, ", "))
	fmt.Fprintf(env.Stdout, "  config     %s\n", h.Paths.Root)
	fmt.Fprintf(env.Stdout, "\nRun `mobiledeck pair` in another terminal to add a phone.\n\n")
}

// writeRuntime publishes the state the CLI subcommands need.
func writeRuntime(path string, h *app.Host, env Env) error {
	state := runtimeState{
		PID:        os.Getpid(),
		Addr:       h.Server.Addr(),
		Port:       h.Server.Port(),
		AdminToken: h.Auth.AdminToken(),
		StartedAt:  time.Now().UnixMilli(),
		Version:    versionOf(env),
		ConfigDir:  h.Paths.Root,
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

	// Sound is a capability the platform adapter answers for itself, and the
	// directory is where a soundboard file must live to be playable. Reporting
	// both together means a user who cannot find their files sees the path.
	add("sound playback", plat.Sound.SoundAvailable(), cfg.SoundsDir)

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
