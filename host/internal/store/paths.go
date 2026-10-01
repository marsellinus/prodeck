// Package store owns the on-disk layout of the host: where configuration,
// devices, profiles, logs and TLS material live, and how they are written.
//
// Every write in this package is atomic (temp file + rename) and every secret
// file is created 0600. Nothing here knows about the protocol.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Paths is the resolved on-disk layout (ARCHITECTURE.md §5).
type Paths struct {
	Root        string // configuration root
	Config      string // config.json
	Devices     string // devices.json
	Audit       string // audit.jsonl
	ProfilesDir string // profiles/
	LogsDir     string // logs/
	TLSDir      string // tls/
	ScriptsDir  string // scripts/, resolved relative to the working dir
}

// DefaultRoot returns the per-user configuration directory:
// %APPDATA%\mobiledeck on Windows, $XDG_CONFIG_HOME/mobiledeck elsewhere.
func DefaultRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("store: cannot determine the user config directory: %w", err)
	}
	return filepath.Join(base, "mobiledeck"), nil
}

// Resolve builds the layout for a root directory. An empty root means "use the
// platform default".
func Resolve(root string) (Paths, error) {
	if root == "" {
		var err error
		root, err = DefaultRoot()
		if err != nil {
			return Paths{}, err
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("store: resolve %q: %w", root, err)
	}
	return Paths{
		Root:        abs,
		Config:      filepath.Join(abs, "config.json"),
		Devices:     filepath.Join(abs, "devices.json"),
		Audit:       filepath.Join(abs, "audit.jsonl"),
		ProfilesDir: filepath.Join(abs, "profiles"),
		LogsDir:     filepath.Join(abs, "logs"),
		TLSDir:      filepath.Join(abs, "tls"),
		ScriptsDir:  "scripts",
	}, nil
}

// Ensure creates every directory the host needs.
func (p Paths) Ensure() error {
	for _, dir := range []string{p.Root, p.ProfilesDir, p.LogsDir, p.TLSDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("store: create %s: %w", dir, err)
		}
	}
	return nil
}

// LogFile returns the active log file path.
func (p Paths) LogFile() string { return filepath.Join(p.LogsDir, "mobiledeck.log") }

// PIDFile returns the path of the pidfile used by start/stop/status.
func (p Paths) PIDFile() string { return filepath.Join(p.LogsDir, "mobiledeck.pid") }

// RuntimeFile holds the live process's address and admin token, so the CLI can
// talk to a daemon it did not start. It is written 0600 and removed on exit.
func (p Paths) RuntimeFile() string { return filepath.Join(p.Root, "runtime.json") }

// OS reports the platform name used by platform maps in profiles.
func OS() string {
	switch runtime.GOOS {
	case "windows", "linux", "darwin":
		return runtime.GOOS
	default:
		return runtime.GOOS
	}
}
