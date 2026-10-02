package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/app"
	"github.com/mobiledeck/mobiledeck/host/internal/gui"
	"github.com/mobiledeck/mobiledeck/host/internal/server"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
	"github.com/mobiledeck/mobiledeck/host/internal/tray"
)

// cmdGUI runs the host and opens the desktop control panel.
//
// The host is started first, in this process, and the panel is only a client of
// it. That ordering is deliberate: the panel drives the same loopback admin API
// the CLI does, so there is one implementation of every operation, and closing
// the window stops the host rather than leaving an invisible one behind.
func cmdGUI(env Env, args []string) int {
	fs, g := newFlagSet(env, "gui", "Run the host and open the desktop control panel.")
	noTray := fs.Bool("no-tray", false,
		"close the window and stop the host, instead of leaving it running in the notification area")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	cfg, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	if !gui.Available() {
		fmt.Fprintln(env.Stderr, "mobiledeck: no display is available, so the control panel cannot be opened.")
		fmt.Fprintln(env.Stderr, "Start the host instead and use the command line, or the panel from a desktop session:")
		fmt.Fprintf(env.Stderr, "  mobiledeck run --config-dir %s\n", paths.Root)
		return ExitError
	}

	// A second instance would fight over the port and the device store.
	lock, err := store.AcquireLock(paths.PIDFile())
	if err != nil {
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitAlreadyUp
	}
	defer lock.Release()

	// The panel gets its own log sink: a window is not a terminal, so a
	// structured line per request would be invisible anyway, and the file keeps
	// the full record for `mobiledeck logs`.
	h, err := app.Build(cfg, paths, versionOf(env), false)
	if err != nil {
		return fail(env, err)
	}
	defer h.Close()

	if err := h.Server.Listen(); err != nil {
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitError
	}

	state := runtimeState{
		PID:        os.Getpid(),
		Addr:       h.Server.Addr(),
		Port:       h.Server.Port(),
		AdminToken: h.Auth.AdminToken(),
		StartedAt:  time.Now().UnixMilli(),
		Version:    versionOf(env),
		ConfigDir:  paths.Root,
	}
	if err := writeRuntimeState(paths.RuntimeFile(), state); err != nil {
		h.Log.Logger.Warn("could not publish the runtime state; the command line will not find this host", "error", err)
	}
	defer os.Remove(paths.RuntimeFile())

	// Serve in the background so the window can open on the main thread, which
	// the native toolkits require.
	serveCtx, stopServing := context.WithCancel(context.Background())
	defer stopServing()
	serveErr := make(chan error, 1)
	go func() { serveErr <- h.Server.Serve(serveCtx) }()

	// Wait for the listener to be genuinely reachable before the panel loads:
	// a window that opens faster than the server answers would show its first
	// request failing, which looks like a broken host.
	if !waitForHost(h.Server.Addr(), 5*time.Second) {
		fmt.Fprintln(env.Stderr, "mobiledeck: the host did not become reachable; see the log file")
		return ExitError
	}

	h.Log.Logger.Info("control panel opening", "addr", h.Server.Addr(), "config_dir", paths.Root)

	// The tray is on by default where it exists. Closing a window and losing the
	// deck the phone is using is the surprising behaviour, not the other way
	// round; --no-tray is for a machine where the panel is the only reason the
	// host is running.
	useTray := !*noTray && tray.Available()
	if !*noTray && !tray.Available() && !g.quiet {
		fmt.Fprintln(env.Stderr, "note: this build has no notification area, so closing the window stops the host")
	}

	if err := gui.Run(gui.Options{
		Addr:    h.Server.Addr(),
		Token:   h.Auth.AdminToken(),
		Title:   fmt.Sprintf("MobileDeck - %s", cfg.HostName),
		Tray:    useTray,
		Address: reachableAddress(h.Server),
	}); err != nil {
		fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
		return ExitError
	}

	// The window closed: stop the host cleanly.
	//
	// A failure here is reported but does not change the exit code. The window is
	// already gone, so the user's session is over; returning non-zero would only
	// make a launcher report an error for a normal close. It also covers the case
	// where the configuration directory disappeared underneath a running host,
	// which surfaces as an error from the server's shutdown path and is not
	// something the user can act on from here.
	stopServing()
	select {
	case err := <-serveErr:
		if err != nil {
			h.Log.Logger.Warn("the host stopped with an error while shutting down", "error", err)
			if !g.quiet {
				fmt.Fprintf(env.Stderr, "mobiledeck: the host reported an error while stopping: %v\n", err)
			}
		}
	case <-time.After(5 * time.Second):
		h.Log.Logger.Warn("the host did not stop within 5 seconds")
	}

	if !g.quiet {
		fmt.Fprintf(env.Stdout, "control panel closed; the host on %s has stopped\n", h.Server.Addr())
	}
	return ExitOK
}

// reachableAddress is the address the tray menu shows: the one a phone on the
// same network would type, not the loopback address the panel itself uses.
func reachableAddress(srv *server.Server) string {
	if addrs := srv.LocalAddresses(); len(addrs) > 0 {
		return strings.TrimPrefix(addrs[0], "http://")
	}
	// No LAN address means the host is loopback-only, which is what `--bind
	// 127.0.0.1` asks for. Showing the loopback address is still more useful
	// than showing nothing, and it is what a browser on this machine would use.
	return srv.Addr()
}

// writeRuntimeState publishes the same file `mobiledeck run` writes, so the
// command line can administer a host started from the panel.
func writeRuntimeState(path string, state runtimeState) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// waitForHost polls the health endpoint until it answers.
func waitForHost(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pingHealth(addr) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
