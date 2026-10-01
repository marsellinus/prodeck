// Package cli implements the mobiledeck command.
//
// The command is a thin shell over the host: it parses arguments, assembles the
// collaborators, and either runs the server in the foreground or talks to a
// running one over its loopback admin API. No business logic lives here.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/mobiledeck/mobiledeck/host/internal/config"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// Exit codes, chosen so a script can distinguish the common failures.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitNotRunning  = 3
	ExitAlreadyUp   = 4
	ExitUnreachable = 5
)

// Env carries the process-wide dependencies so tests can substitute them.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// Args excludes the program name.
	Args []string
	// Version is the build version reported by `version`.
	Version string
}

// globalFlags are accepted by every subcommand.
type globalFlags struct {
	configDir  string
	configFile string
	bind       string
	port       int
	name       string
	logLevel   string
	logFormat  string
	tlsOff     bool
	plaintext  bool
	absPaths   bool
	verbose    bool
	json       bool
	quiet      bool
}

// Run dispatches a subcommand and returns the process exit code.
func Run(env Env) int {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}
	if env.Stdin == nil {
		env.Stdin = os.Stdin
	}

	args := env.Args
	if len(args) == 0 {
		printUsage(env.Stdout)
		return ExitOK
	}

	// A leading -h/--help before any subcommand.
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(env.Stdout)
		return ExitOK
	}
	if args[0] == "-v" || args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(env.Stdout, "mobiledeck %s (%s/%s)\n", versionOf(env), runtime.GOOS, runtime.GOARCH)
		return ExitOK
	}

	cmd, rest := args[0], args[1:]

	switch cmd {
	case "run", "serve":
		return cmdRun(env, rest)
	case "start":
		return cmdStart(env, rest)
	case "stop":
		return cmdStop(env, rest)
	case "restart":
		return cmdRestart(env, rest)
	case "status":
		return cmdStatus(env, rest)
	case "devices":
		return cmdDevices(env, rest)
	case "profiles":
		return cmdProfiles(env, rest)
	case "pair":
		return cmdPair(env, rest)
	case "logs":
		return cmdLogs(env, rest)
	case "keys":
		return cmdKeys(env, rest)
	case "actions":
		return cmdActions(env, rest)
	case "init":
		return cmdInit(env, rest)
	case "doctor":
		return cmdDoctor(env, rest)
	default:
		fmt.Fprintf(env.Stderr, "mobiledeck: unknown command %q\n\n", cmd)
		printUsage(env.Stderr)
		return ExitUsage
	}
}

func versionOf(env Env) string {
	if env.Version == "" {
		return "dev"
	}
	return env.Version
}

// newFlagSet builds a flag set with the global flags registered and a usage
// message that lists them.
func newFlagSet(env Env, name, usage string) (*flag.FlagSet, *globalFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	g := &globalFlags{}
	fs.StringVar(&g.configDir, "config-dir", "", "configuration directory (default: the per-user config directory)")
	fs.StringVar(&g.configFile, "config", "", "configuration file (default: <config-dir>/config.json)")
	fs.StringVar(&g.bind, "bind", "", "address to listen on; 0.0.0.0 for the LAN, 127.0.0.1 for loopback only")
	fs.IntVar(&g.port, "port", 0, "TCP port to listen on")
	fs.StringVar(&g.name, "name", "", "host name shown to clients during discovery and pairing")
	fs.StringVar(&g.logLevel, "log-level", "", "debug, info, warn or error")
	fs.StringVar(&g.logFormat, "log-format", "", "text or json")
	fs.BoolVar(&g.tlsOff, "no-tls", false, "disable TLS (only sensible when bound to loopback)")
	fs.BoolVar(&g.plaintext, "insecure-allow-plaintext", false, "allow pairing over an unencrypted LAN socket")
	fs.BoolVar(&g.absPaths, "allow-absolute-paths", false, "let scripts and folders reference paths outside the profile and scripts directories")
	fs.BoolVar(&g.verbose, "v", false, "verbose output")
	fs.BoolVar(&g.json, "json", false, "emit machine-readable JSON")
	fs.BoolVar(&g.quiet, "quiet", false, "only report errors")
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "%s\n\nusage: mobiledeck %s [flags]\n\nflags:\n", usage, name)
		fs.PrintDefaults()
	}
	return fs, g
}

// parseFlags parses arguments and reports a usage error.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK, false
		}
		return ExitUsage, false
	}
	return ExitOK, true
}

// resolve loads the configuration and applies the CLI overrides.
//
// Overrides are applied before Validate, so an invalid flag is caught here with
// a clear message rather than at the first request.
func resolve(g *globalFlags) (config.Config, store.Paths, error) {
	root := g.configDir
	paths, err := store.Resolve(root)
	if err != nil {
		return config.Config{}, store.Paths{}, err
	}
	configPath := g.configFile
	if configPath == "" {
		configPath = paths.Config
	}

	cfg, created, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, store.Paths{}, err
	}
	_ = created

	if g.bind != "" {
		cfg.Bind = g.bind
	}
	if g.port != 0 {
		cfg.Port = g.port
	}
	if g.name != "" {
		cfg.HostName = g.name
	}
	if g.logLevel != "" {
		cfg.Logging.Level = g.logLevel
	}
	if g.logFormat != "" {
		cfg.Logging.Format = g.logFormat
	}
	if g.tlsOff {
		cfg.TLS.Enabled = false
	}
	if g.plaintext {
		cfg.InsecureAllowPlaintext = true
	}
	if g.absPaths {
		cfg.AllowAbsolutePaths = true
	}
	// Keep the profiles directory in step with a relocated config dir.
	if g.configDir != "" && cfg.ProfilesDir == "" {
		cfg.ProfilesDir = paths.ProfilesDir
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, store.Paths{}, err
	}
	return cfg, paths, nil
}

// signalContext returns a context cancelled on SIGINT/SIGTERM, plus a function
// that reports whether the shutdown was requested by a signal.
func signalContext() (context.Context, func() bool, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	var got bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ch:
			got = true
			cancel()
		case <-ctx.Done():
		}
	}()
	stop := func() {
		signal.Stop(ch)
		cancel()
		<-done
	}
	return ctx, func() bool { return got }, stop
}

// printUsage writes the top-level help.
func printUsage(w io.Writer) {
	fmt.Fprint(w, strings.TrimLeft(`
mobiledeck - turn a phone into a stream deck for this machine

usage: mobiledeck <command> [flags]

commands:
  run         run the host in the foreground
  start       start the host in the background
  stop        stop a background host
  restart     stop then start
  status      show whether a host is running and where it listens
  pair        open a pairing window and print the PIN
  devices     list, rename, enable, disable, revoke or re-scope paired devices
  profiles    list, reload, export or import deck profiles
  logs        show recent log records, optionally following the file
  keys        list every key name a profile may use
  actions     list every action type this host provides
  doctor      check the environment and report what will not work
  init        create a default configuration without starting anything
  version     print the version

Run "mobiledeck <command> -h" for the flags of a command.
`, "\n"))
}
