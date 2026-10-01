package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// OSName returns the canonical platform key used by profile platform maps.
func OSName() string { return runtime.GOOS }

// SupportedOS lists the platform map keys a profile may use.
func SupportedOS() []string { return []string{"windows", "linux", "darwin"} }

// ResolveTarget resolves a target against the running operating system.
func ResolveTarget(raw json.RawMessage) (string, error) {
	return ResolveTargetFor(OSName(), raw)
}

// ResolveTargetFor resolves a parameter that may be either a plain string or a
// per-platform map, e.g.
//
//	"terminal"                                    -> "terminal"
//	{"windows":"wt.exe","linux":"x-terminal-emulator"} -> "wt.exe" on Windows
//
// The OS name is a parameter rather than a global read so the resolution follows
// the platform the host was built with, not the one the process happens to run
// on. That keeps the behaviour testable and keeps "which OS am I" to a single
// source of truth.
//
// A map that has no entry for this platform is an error, not a silent empty
// string: a button that does nothing on this host is exactly the failure the
// profile validation is supposed to prevent.
func ResolveTargetFor(osName string, raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", fmt.Errorf("platform: target is missing")
	}
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("platform: target: %w", err)
		}
		if s == "" {
			return "", fmt.Errorf("platform: target must not be empty")
		}
		return s, nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("platform: target must be a string or a platform map: %w", err)
	}
	if v, ok := m[osName]; ok && v != "" {
		return v, nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "", fmt.Errorf("platform: target has no entry for %s (it defines %s)", osName, strings.Join(keys, ", "))
}

// SanitizeURL parses and validates a URL, rejecting anything that is not a
// well-formed absolute URL with a scheme the user's browser can be handed.
//
// The allow-list matters: on Windows, `ShellExecute` will happily run a local
// executable given a bare path, so an unchecked "URL" field would be an
// arbitrary-execution primitive.
func SanitizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("platform: url must not be empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("platform: url %q is not a valid URL: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "steam", "obs", "vscode", "slack", "zoommtg", "spotify":
		// Accepted: browser and application URI schemes that are safe to hand
		// to the OS opener.
	case "file":
		// Refused on purpose. A file URL handed to the OS opener is an
		// arbitrary-file primitive, and on Windows ShellExecute will run an
		// executable it is pointed at. The legitimate need is served by
		// open_folder and launch_application, which resolve paths through the
		// confinement check (docs/SECURITY.md T6).
		return "", fmt.Errorf("platform: file URLs are not accepted; use open_folder or launch_application so the path is checked against the allowed directories")
	case "":
		return "", fmt.Errorf("platform: url %q has no scheme; prefix it with https:// (a bare path would be executed as a program, not opened)", raw)
	default:
		return "", fmt.Errorf("platform: url scheme %q is not allowed", u.Scheme)
	}
	return u.String(), nil
}

// ExpandPath expands a leading ~ and makes the path absolute against base.
// It does not verify existence: that is the caller's job, with a better error
// message than "no such file".
func ExpandPath(base, path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimLeft(p[1:], `/\`))
		}
	}
	if !filepath.IsAbs(p) && base != "" {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// ConfinePath resolves path and guarantees it stays inside one of roots. It
// resolves symlinks so a link inside an allowed root cannot point outside it
// (docs/SECURITY.md T7).
//
// A non-existent file is reported as such before the containment check, because
// "not found" is a more useful error than "outside the allowed roots" when the
// real problem is a typo.
func ConfinePath(path string, roots []string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("platform: path must not be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("platform: resolve %q: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("platform: %s does not exist", abs)
	}
	// Resolve symlinks on both sides so the comparison is apples to apples.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("platform: resolve symlinks of %s: %w", abs, err)
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootAbs = r
		}
		if contains(rootAbs, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("platform: %s is outside the allowed directories (%s); move the script into the profile or the scripts directory, or start the host with --allow-absolute-paths",
		resolved, strings.Join(roots, ", "))
}

// contains reports whether child is root or lives inside it.
func contains(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

// CommandContext builds an *exec.Cmd that is killed as a process group, so a
// script that spawns children cannot leave orphans behind when it is cancelled
// or times out. The group handling itself is per-OS (see exec_unix/exec_windows).
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

// TruncateCapture limits captured output and reports whether it was cut.
func TruncateCapture(b []byte) (string, bool) {
	if len(b) <= MaxCaptureBytes {
		return string(b), false
	}
	return string(b[:MaxCaptureBytes]), true
}

// MergeEnv returns the process environment plus overrides. Keys present in both
// are overridden, which is what a profile author expects.
func MergeEnv(overrides map[string]string) []string {
	env := os.Environ()
	if len(overrides) == 0 {
		return env
	}
	// On Windows environment variable names are case-insensitive, so a
	// case-sensitive override map would silently fail to replace PATH. Compare
	// case-insensitively there.
	fold := runtime.GOOS == "windows"
	seen := make(map[string]int, len(env))
	for i, kv := range env {
		name := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			name = kv[:idx]
		}
		if fold {
			name = strings.ToUpper(name)
		}
		seen[name] = i
	}
	for k, v := range overrides {
		entry := k + "=" + v
		key := k
		if fold {
			key = strings.ToUpper(k)
		}
		if i, ok := seen[key]; ok {
			env[i] = entry
		} else {
			env = append(env, entry)
		}
	}
	return env
}
