// Package config holds the host's persistent settings.
//
// The configuration is a single JSON file that is safe to hand-edit: unknown
// fields are rejected so a typo is an error rather than a silently ignored
// setting, and every value is range-checked before the host starts listening.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// DefaultPort is the LAN port the agent listens on. It is in the dynamic range
// and unused by any common service, which matters because we advertise it over
// mDNS rather than relying on the user remembering it.
const DefaultPort = 8765

// Config is the full host configuration.
type Config struct {
	// HostID is a stable random identifier, generated on first run. It is what
	// the client pins when it remembers a host, so it must never change.
	HostID string `json:"host_id"`
	// HostName is the friendly name shown during discovery and pairing.
	HostName string `json:"host_name"`

	Bind string `json:"bind"`
	Port int    `json:"port"`

	TLS           TLS       `json:"tls"`
	Pairing       Pairing   `json:"pairing"`
	RateLimit     RateLimit `json:"rate_limit"`
	Discovery     Discovery `json:"discovery"`
	Logging       Logging   `json:"logging"`
	Session       Session   `json:"session"`
	Engine        Engine    `json:"engine"`
	ProfilesDir   string    `json:"profiles_dir"`
	ScriptsDir    string    `json:"scripts_dir"`
	SoundsDir     string    `json:"sounds_dir"`
	ActiveProfile string    `json:"active_profile"`

	// AllowAbsolutePaths permits run_script/run_folder to reference paths
	// outside the profile and scripts directories. Off by default because it
	// turns a profile into "run anything on this machine".
	AllowAbsolutePaths bool `json:"allow_absolute_paths"`
	// InsecureAllowPlaintext permits pairing over an unencrypted LAN socket.
	// Off by default; see docs/SECURITY.md §5.
	InsecureAllowPlaintext bool `json:"insecure_allow_plaintext"`
}

// TLS configures the transport security layer.
type TLS struct {
	Enabled      bool   `json:"enabled"`
	AutoGenerate bool   `json:"auto_generate"`
	CertFile     string `json:"cert_file,omitempty"`
	KeyFile      string `json:"key_file,omitempty"`
	ValidDays    int    `json:"valid_days,omitempty"`
}

// Pairing configures the PIN handshake.
type Pairing struct {
	PINLength     int `json:"pin_length"`
	PINTTLSeconds int `json:"pin_ttl_seconds"`
	MaxAttempts   int `json:"max_attempts"`
}

// RateLimit is the per-device token bucket.
type RateLimit struct {
	PerSecond float64 `json:"per_second"`
	Burst     int     `json:"burst"`
}

// Discovery configures mDNS advertising.
type Discovery struct {
	MDNS    bool   `json:"mdns"`
	Service string `json:"service"`
	Domain  string `json:"domain"`
}

// Logging configures the structured logger and its rotation.
type Logging struct {
	Level      string `json:"level"`
	Format     string `json:"format"` // text | json
	MaxSizeMB  int    `json:"max_size_mb"`
	MaxBackups int    `json:"max_backups"`
	Console    bool   `json:"console"`
}

// Session configures connection lifetimes.
type Session struct {
	HeartbeatMS    int `json:"heartbeat_ms"`
	IdleTimeoutMS  int `json:"idle_timeout_ms"`
	HelloTimeoutMS int `json:"hello_timeout_ms"`
	MaxClients     int `json:"max_clients"`
	WriteTimeoutMS int `json:"write_timeout_ms"`
}

// Engine configures action execution.
type Engine struct {
	MaxConcurrentActions   int `json:"max_concurrent_actions"`
	QueueDepth             int `json:"queue_depth"`
	DefaultActionTimeoutMS int `json:"default_action_timeout_ms"`
	MaxMacroDurationMS     int `json:"max_macro_duration_ms"`
	MaxMacroSteps          int `json:"max_macro_steps"`
	TelemetryMinIntervalMS int `json:"telemetry_min_interval_ms"`
}

// Default returns the configuration used on first run: LAN-bound, TLS on,
// conservative limits.
func Default() Config {
	host, _ := os.Hostname()
	if strings.TrimSpace(host) == "" {
		host = "mobiledeck-host"
	}
	return Config{
		HostName:  host,
		Bind:      "0.0.0.0",
		Port:      DefaultPort,
		TLS:       TLS{Enabled: true, AutoGenerate: true, ValidDays: 825},
		Pairing:   Pairing{PINLength: 6, PINTTLSeconds: 120, MaxAttempts: 5},
		RateLimit: RateLimit{PerSecond: 30, Burst: 60},
		Discovery: Discovery{MDNS: true, Service: "_mobiledeck._tcp", Domain: "local."},
		Logging:   Logging{Level: "info", Format: "text", MaxSizeMB: 10, MaxBackups: 3, Console: true},
		Session: Session{
			HeartbeatMS:    15000,
			IdleTimeoutMS:  45000,
			HelloTimeoutMS: 10000,
			MaxClients:     16,
			WriteTimeoutMS: 10000,
		},
		Engine: Engine{
			MaxConcurrentActions:   8,
			QueueDepth:             64,
			DefaultActionTimeoutMS: 30000,
			MaxMacroDurationMS:     300000,
			MaxMacroSteps:          256,
			TelemetryMinIntervalMS: 250,
		},
	}
}

// Load reads the configuration, creating a default one on first run.
func Load(path string) (Config, bool, error) {
	cfg := Default()
	raw, err := store.ReadFileOr(path, nil)
	if err != nil {
		return cfg, false, err
	}
	if raw == nil {
		if err := cfg.finish(path); err != nil {
			return cfg, false, err
		}
		if err := cfg.Save(path); err != nil {
			return cfg, false, err
		}
		return cfg, true, nil
	}

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, false, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.finish(path); err != nil {
		return cfg, false, err
	}
	return cfg, false, nil
}

// finish fills in derived and generated values, then validates.
func (c *Config) finish(path string) error {
	if c.HostID == "" {
		id, err := randomHex(8)
		if err != nil {
			return fmt.Errorf("config: generate host id: %w", err)
		}
		c.HostID = id
	}
	if c.ProfilesDir == "" {
		c.ProfilesDir = filepath.Join(filepath.Dir(path), "profiles")
	}
	if c.ScriptsDir == "" {
		c.ScriptsDir = "scripts"
	}
	if c.SoundsDir == "" {
		c.SoundsDir = filepath.Join(filepath.Dir(path), "sounds")
	}
	return c.Validate()
}

// Validate range-checks every field. It is called on load and on every CLI
// override, so an invalid flag cannot reach a running server.
func (c *Config) Validate() error {
	// Port 0 is allowed and means "let the operating system choose a free
	// port". That is what makes it possible to run several instances on one
	// machine and to write tests that never collide; the chosen port is reported
	// on start-up.
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("config: port %d is outside 0..65535 (0 asks the operating system for a free port)", c.Port)
	}
	if strings.TrimSpace(c.HostName) == "" {
		return errors.New("config: host_name must not be empty")
	}
	if len(c.HostName) > 64 {
		return fmt.Errorf("config: host_name is %d bytes, max is 64", len(c.HostName))
	}
	if c.Bind == "" {
		return errors.New("config: bind must not be empty (use 0.0.0.0 for the LAN or 127.0.0.1 for loopback only)")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: logging.level %q must be debug, info, warn or error", c.Logging.Level)
	}
	switch c.Logging.Format {
	case "text", "json":
	default:
		return fmt.Errorf("config: logging.format %q must be text or json", c.Logging.Format)
	}
	if c.Pairing.PINLength < 4 || c.Pairing.PINLength > 12 {
		return fmt.Errorf("config: pairing.pin_length %d must be 4..12", c.Pairing.PINLength)
	}
	if c.Pairing.PINTTLSeconds < 30 || c.Pairing.PINTTLSeconds > 3600 {
		return fmt.Errorf("config: pairing.pin_ttl_seconds %d must be 30..3600", c.Pairing.PINTTLSeconds)
	}
	if c.Pairing.MaxAttempts < 1 || c.Pairing.MaxAttempts > 50 {
		return fmt.Errorf("config: pairing.max_attempts %d must be 1..50", c.Pairing.MaxAttempts)
	}
	if c.RateLimit.PerSecond <= 0 || c.RateLimit.PerSecond > 10000 {
		return fmt.Errorf("config: rate_limit.per_second %.1f must be 0..10000", c.RateLimit.PerSecond)
	}
	if c.RateLimit.Burst < 1 || c.RateLimit.Burst > 100000 {
		return fmt.Errorf("config: rate_limit.burst %d must be 1..100000", c.RateLimit.Burst)
	}
	if c.Session.HeartbeatMS < 1000 || c.Session.HeartbeatMS > 600000 {
		return fmt.Errorf("config: session.heartbeat_ms %d must be 1000..600000", c.Session.HeartbeatMS)
	}
	if c.Session.IdleTimeoutMS <= c.Session.HeartbeatMS {
		return fmt.Errorf("config: session.idle_timeout_ms (%d) must exceed session.heartbeat_ms (%d), otherwise every client is disconnected as idle",
			c.Session.IdleTimeoutMS, c.Session.HeartbeatMS)
	}
	if c.Session.MaxClients < 1 || c.Session.MaxClients > 512 {
		return fmt.Errorf("config: session.max_clients %d must be 1..512", c.Session.MaxClients)
	}
	if c.Engine.MaxConcurrentActions < 1 || c.Engine.MaxConcurrentActions > 256 {
		return fmt.Errorf("config: engine.max_concurrent_actions %d must be 1..256", c.Engine.MaxConcurrentActions)
	}
	if c.Engine.QueueDepth < c.Engine.MaxConcurrentActions {
		return fmt.Errorf("config: engine.queue_depth (%d) must be at least engine.max_concurrent_actions (%d)",
			c.Engine.QueueDepth, c.Engine.MaxConcurrentActions)
	}
	if c.Engine.MaxMacroSteps < 1 || c.Engine.MaxMacroSteps > 10000 {
		return fmt.Errorf("config: engine.max_macro_steps %d must be 1..10000", c.Engine.MaxMacroSteps)
	}
	if c.TLS.Enabled && c.TLS.ValidDays < 1 {
		return fmt.Errorf("config: tls.valid_days %d must be at least 1", c.TLS.ValidDays)
	}
	if c.Discovery.MDNS && !strings.HasPrefix(c.Discovery.Service, "_") {
		return fmt.Errorf("config: discovery.service %q must start with an underscore (DNS-SD form, e.g. _mobiledeck._tcp)", c.Discovery.Service)
	}
	return nil
}

// Save writes the configuration atomically.
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	b = append(b, '\n')
	return store.WriteFileAtomic(path, b, 0o600)
}

// IsLoopback reports whether the bind address only exposes the host locally.
// Several security defaults key off this: a loopback bind does not need TLS,
// and it cannot be reached from a phone, which is worth warning about.
func (c *Config) IsLoopback() bool {
	switch c.Bind {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(c.Bind, "127.")
}

// Heartbeat returns the heartbeat interval as a duration.
func (c *Config) Heartbeat() time.Duration {
	return time.Duration(c.Session.HeartbeatMS) * time.Millisecond
}

// IdleTimeout returns the idle timeout as a duration.
func (c *Config) IdleTimeout() time.Duration {
	return time.Duration(c.Session.IdleTimeoutMS) * time.Millisecond
}

// HelloTimeout returns the first-frame deadline as a duration.
func (c *Config) HelloTimeout() time.Duration {
	return time.Duration(c.Session.HelloTimeoutMS) * time.Millisecond
}

// WriteTimeout returns the per-frame write deadline as a duration.
func (c *Config) WriteTimeout() time.Duration {
	return time.Duration(c.Session.WriteTimeoutMS) * time.Millisecond
}

// DefaultActionTimeout returns the fallback action timeout.
func (c *Config) DefaultActionTimeout() time.Duration {
	return time.Duration(c.Engine.DefaultActionTimeoutMS) * time.Millisecond
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// EnsureExists makes sure a configuration file exists without loading it, used
// by `mobiledeck init`.
func EnsureExists(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config: stat %s: %w", path, err)
	}
	cfg := Default()
	if err := cfg.finish(path); err != nil {
		return err
	}
	return cfg.Save(path)
}
