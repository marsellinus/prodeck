package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/logging"
	"github.com/mobiledeck/mobiledeck/host/internal/profile"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// runtimePathsFor builds the paths the CLI needs from the resolved store paths.
func runtimePathsFor(paths store.Paths) runtimePaths {
	return runtimePaths{
		runtimeFile: paths.RuntimeFile(),
		pidFile:     paths.PIDFile(),
		logFile:     paths.LogFile(),
		root:        paths.Root,
	}
}

// cmdStatus reports whether a host is running and what it is doing.
func cmdStatus(env Env, args []string) int {
	fs, g := newFlagSet(env, "status", "Show whether a host is running and where it listens.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	client, state, err := connectRuntime(runtimePathsFor(paths))
	if err != nil {
		if errors.Is(err, errHostUnreachable) {
			if g.json {
				return emitJSON(env, map[string]any{"running": false, "config_dir": paths.Root})
			}
			if !g.quiet {
				fmt.Fprintf(env.Stdout, "not running (config dir %s)\n", paths.Root)
			}
			return ExitNotRunning
		}
		return fail(env, err)
	}

	var status map[string]any
	if err := client.get("/api/v1/admin/status", &status); err != nil {
		return fail(env, err)
	}
	if g.json {
		status["running"] = true
		status["pid"] = state.PID
		return emitJSON(env, status)
	}

	fmt.Fprintf(env.Stdout, "running (pid %d)\n", state.PID)
	fmt.Fprintf(env.Stdout, "  listening   %v\n", status["addr"])
	if addrs, ok := status["addresses"].([]any); ok {
		for _, a := range addrs {
			fmt.Fprintf(env.Stdout, "  reachable   %v\n", a)
		}
	}
	fmt.Fprintf(env.Stdout, "  version     %v\n", status["version"])
	fmt.Fprintf(env.Stdout, "  os          %v\n", status["os"])
	if tlsOn, _ := status["tls"].(bool); tlsOn {
		fmt.Fprintf(env.Stdout, "  tls         on, fingerprint %v\n", status["fingerprint"])
	} else {
		fmt.Fprintln(env.Stdout, "  tls         off")
	}
	fmt.Fprintf(env.Stdout, "  profiles    %v\n", joinAny(status["profiles"]))
	sessions, _ := status["sessions"].([]any)
	fmt.Fprintf(env.Stdout, "  connected   %d device(s)%s\n", len(sessions), prefixIf(len(sessions) > 0, " "+joinAny(sessions)))
	if pairing, ok := status["pairing"].(map[string]any); ok {
		if open, _ := pairing["open"].(bool); open {
			fmt.Fprintf(env.Stdout, "  pairing     open, PIN %v (expires in %vs)\n", pairing["pin"], pairing["expires_in_s"])
		} else {
			fmt.Fprintln(env.Stdout, "  pairing     closed")
		}
	}
	return ExitOK
}

// cmdPair opens a pairing window on the running host and prints the PIN.
func cmdPair(env Env, args []string) int {
	fs, g := newFlagSet(env, "pair", "Open a pairing window and print the PIN.")
	ttl := fs.Int("ttl", 120, "how long the PIN stays valid, in seconds")
	closeOnly := fs.Bool("close", false, "close the pairing window instead of opening one")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	client, _, err := connectRuntime(runtimePathsFor(paths))
	if err != nil {
		if errors.Is(err, errHostUnreachable) {
			// Pairing needs a live host to issue a PIN the running process will
			// actually honour, so this is a hard error rather than a local
			// fallback.
			fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
			fmt.Fprintln(env.Stderr, "the PIN must be issued by the running host, so start it first")
			return ExitNotRunning
		}
		return fail(env, err)
	}

	_ = ttl
	_ = closeOnly

	var resp struct {
		PIN        string `json:"pin"`
		ExpiresInS int    `json:"expires_in_s"`
		HostName   string `json:"host_name"`
	}
	if err := client.post("/api/v1/admin/pair", map[string]any{}, &resp); err != nil {
		return fail(env, err)
	}

	if g.json {
		return emitJSON(env, resp)
	}
	// The PIN is printed in a fixed-width block so it is easy to read across a
	// desk, and to a phone camera if the user photographs the screen.
	fmt.Fprintf(env.Stdout, "\n  MobileDeck host: %s\n\n", resp.HostName)
	fmt.Fprintf(env.Stdout, "  PIN  %s\n\n", spaced(resp.PIN))
	fmt.Fprintf(env.Stdout, "  valid for %d seconds; enter it on the phone now\n\n", resp.ExpiresInS)
	return ExitOK
}

// cmdDevices lists and mutates paired devices.
func cmdDevices(env Env, args []string) int {
	fs, g := newFlagSet(env, "devices", "List, rename, enable, disable, revoke or re-scope paired devices.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	_, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}
	client, _, err := connectRuntime(runtimePathsFor(paths))
	if err != nil {
		return fail(env, err)
	}

	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}

	switch sub {
	case "", "list":
		var resp struct {
			Devices []map[string]any `json:"devices"`
		}
		if err := client.get("/api/v1/admin/devices", &resp); err != nil {
			return fail(env, err)
		}
		if g.json {
			return emitJSON(env, resp)
		}
		if len(resp.Devices) == 0 {
			fmt.Fprintln(env.Stdout, "no devices are paired; run `mobiledeck pair` to add one")
			return ExitOK
		}
		fmt.Fprintf(env.Stdout, "%-22s %-20s %-8s %-9s %s\n", "ID", "NAME", "CONNECTED", "DISABLED", "SCOPES")
		for _, d := range resp.Devices {
			fmt.Fprintf(env.Stdout, "%-22s %-20s %-8v %-9v %s\n",
				str(d["id"]), truncate(str(d["name"]), 20),
				yesNo(d["connected"]), yesNo(d["disabled"]), joinAny(d["scopes"]))
		}
		return ExitOK

	case "rename":
		if len(rest) < 3 {
			return usageErr(env, "devices rename <id> <new name>")
		}
		if err := client.patch("/api/v1/admin/devices/"+rest[1], map[string]any{"name": strings.Join(rest[2:], " ")}, nil); err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "renamed %s\n", rest[1])
		return ExitOK

	case "enable", "disable":
		if len(rest) < 2 {
			return usageErr(env, "devices "+sub+" <id>")
		}
		if err := client.patch("/api/v1/admin/devices/"+rest[1], map[string]any{"disabled": sub == "disable"}, nil); err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "%sd %s\n", sub, rest[1])
		return ExitOK

	case "revoke":
		if len(rest) < 2 {
			return usageErr(env, "devices revoke <id>")
		}
		if err := client.delete("/api/v1/admin/devices/"+rest[1], nil); err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "revoked %s; its token no longer works\n", rest[1])
		return ExitOK

	case "scopes":
		if len(rest) < 2 {
			return usageErr(env, "devices scopes <id> [scope ...]   (no scopes given: print the current ones)")
		}
		if len(rest) == 2 {
			var d map[string]any
			if err := client.get("/api/v1/admin/devices/"+rest[1], &d); err != nil {
				return fail(env, err)
			}
			fmt.Fprintln(env.Stdout, joinAny(d["scopes"]))
			return ExitOK
		}
		// Validate before sending: a typo should be caught locally with the list
		// of valid scopes, not by a server error.
		if _, err := auth.ParseScopes(rest[2:]); err != nil {
			fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
			fmt.Fprintf(env.Stderr, "known scopes: %s\n", auth.ScopeList(auth.AllScopes()))
			return ExitUsage
		}
		var d map[string]any
		if err := client.put("/api/v1/admin/devices/"+rest[1]+"/scopes", map[string]any{"scopes": rest[2:]}, &d); err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "scopes for %s: %s\n", rest[1], joinAny(d["scopes"]))
		return ExitOK

	case "scopes-available":
		for _, s := range auth.AllScopes() {
			risk := ""
			for _, hr := range auth.HighRiskScopes() {
				if s == hr {
					risk = "  (high risk, not granted by default)"
				}
			}
			fmt.Fprintf(env.Stdout, "%-18s%s\n", s, risk)
		}
		return ExitOK

	default:
		return usageErr(env, "devices [list|rename|enable|disable|revoke|scopes|scopes-available]")
	}
}

// cmdProfiles lists, reloads, exports and imports profiles.
func cmdProfiles(env Env, args []string) int {
	fs, g := newFlagSet(env, "profiles", "List, reload, export or import deck profiles.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	cfg, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}

	// export/import and a plain listing of a stopped host work directly on
	// disk, which is what makes profile management possible without a daemon.
	switch sub {
	case "export":
		if len(rest) < 2 {
			return usageErr(env, "profiles export <id> [file]")
		}
		path := filepath.Join(cfg.ProfilesDir, rest[1], "profile.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			return fail(env, fmt.Errorf("cannot read %s: %w", path, err))
		}
		// Validate before handing it out: exporting a broken document would
		// propagate the breakage into someone's backup.
		if _, err := profile.Load(raw, nil); err != nil {
			return fail(env, fmt.Errorf("profile %q is not valid, refusing to export it: %w", rest[1], err))
		}
		if len(rest) >= 3 {
			if err := store.WriteFileAtomic(rest[2], raw, 0o600); err != nil {
				return fail(env, err)
			}
			fmt.Fprintf(env.Stdout, "wrote %s\n", rest[2])
			return ExitOK
		}
		env.Stdout.Write(raw)
		return ExitOK

	case "import":
		if len(rest) < 2 {
			return usageErr(env, "profiles import <file> [--overwrite]")
		}
		overwrite := false
		for _, a := range rest[2:] {
			if a == "--overwrite" || a == "-overwrite" {
				overwrite = true
			}
		}
		raw, err := os.ReadFile(rest[1])
		if err != nil {
			return fail(env, fmt.Errorf("cannot read %s: %w", rest[1], err))
		}
		doc, err := profile.Load(raw, nil)
		if err != nil {
			return fail(env, fmt.Errorf("%s is not a valid profile: %w", rest[1], err))
		}
		target := filepath.Join(cfg.ProfilesDir, doc.ID, "profile.json")
		if _, err := os.Stat(target); err == nil && !overwrite {
			fmt.Fprintf(env.Stderr, "mobiledeck: profile %q already exists; pass --overwrite to replace it\n", doc.ID)
			return ExitError
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fail(env, err)
		}
		if err := store.WriteFileAtomic(target, raw, 0o600); err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "imported profile %q into %s\n", doc.ID, filepath.Dir(target))
		return ExitOK

	case "validate":
		if len(rest) < 2 {
			return usageErr(env, "profiles validate <file>")
		}
		raw, err := os.ReadFile(rest[1])
		if err != nil {
			return fail(env, err)
		}
		doc, err := profile.Load(raw, nil)
		if err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "%s is valid: %d page(s), %d button(s)\n", rest[1], len(doc.Pages), countButtons(doc))
		return ExitOK
	}

	// Everything else needs the live host, because the running process owns the
	// authoritative view and the revision numbers.
	client, _, err := connectRuntime(runtimePathsFor(paths))
	if err != nil {
		return fail(env, err)
	}

	switch sub {
	case "", "list":
		var resp struct {
			Profiles []map[string]any `json:"profiles"`
			Errors   []map[string]any `json:"errors"`
			Dir      string           `json:"dir"`
		}
		if err := client.get("/api/v1/admin/profiles", &resp); err != nil {
			return fail(env, err)
		}
		if g.json {
			return emitJSON(env, resp)
		}
		fmt.Fprintf(env.Stdout, "directory: %s\n\n", resp.Dir)
		if len(resp.Profiles) == 0 {
			fmt.Fprintln(env.Stdout, "no profiles are loaded")
		}
		fmt.Fprintf(env.Stdout, "%-16s %-22s %-8s %s\n", "ID", "NAME", "PAGES", "REVISION")
		for _, p := range resp.Profiles {
			pages, _ := p["pages"].([]any)
			fmt.Fprintf(env.Stdout, "%-16s %-22s %-8d %v\n",
				str(p["id"]), truncate(str(p["name"]), 22), len(pages), p["revision"])
		}
		// A profile that failed to load is reported, not hidden: otherwise a
		// typo looks like a deleted profile.
		for _, e := range resp.Errors {
			fmt.Fprintf(env.Stderr, "\nrejected: %v\n  %v\n", e["id"], e["error"])
		}
		return ExitOK

	case "reload":
		var resp map[string]any
		if err := client.post("/api/v1/admin/profiles/reload", nil, &resp); err != nil {
			return fail(env, err)
		}
		fmt.Fprintf(env.Stdout, "reloaded; revision %v\n", resp["revision"])
		return ExitOK

	default:
		return usageErr(env, "profiles [list|reload|export|import|validate]")
	}
}

// cmdLogs prints recent log records.
func cmdLogs(env Env, args []string) int {
	fs, g := newFlagSet(env, "logs", "Show recent log records, optionally following the file.")
	follow := fs.Bool("follow", false, "keep printing new records as they arrive")
	lines := fs.Int("n", 100, "how many recent records to show")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	cfg, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}
	_ = cfg

	level := logging.Level(cfgLogLevel(g))

	if *follow {
		return followLog(env, paths.LogFile(), level)
	}
	records, err := readLogTail(paths.LogFile(), *lines)
	if err != nil {
		return fail(env, err)
	}
	for _, line := range records {
		fmt.Fprintln(env.Stdout, line)
	}
	return ExitOK
}

func cfgLogLevel(g *globalFlags) string {
	if g.logLevel != "" {
		return g.logLevel
	}
	return "debug"
}

// readLogTail reads the last n lines of the log file and filters them by level.
//
// The file is read whole and then sliced: a rotating log is capped at a few
// megabytes by configuration, so a streaming reader would add complexity for no
// benefit, and the cap is what makes this safe.
func readLogTail(path string, n int) ([]string, error) {
	raw, err := store.ReadFileOr(path, nil)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("no log file yet at %s", path)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// followLog streams new lines from the log file.
func followLog(env Env, path string, minLevel interface{ String() string }) int {
	f, err := os.Open(path)
	if err != nil {
		return fail(env, fmt.Errorf("cannot open %s: %w", path, err))
	}
	defer f.Close()

	// Start at the end: `logs --follow` means "show me what happens next", and
	// the history is what `logs` without --follow is for.
	if _, err := f.Seek(0, 2); err != nil {
		return fail(env, err)
	}

	reader := bufio.NewReader(f)
	fmt.Fprintf(env.Stderr, "following %s (press Ctrl+C to stop)\n", path)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			fmt.Fprint(env.Stdout, line)
		}
		if err != nil {
			time.Sleep(250 * time.Millisecond)
		}
	}
}

// --- small formatting helpers --------------------------------------------

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func yesNo(v any) string {
	if b, ok := v.(bool); ok && b {
		return "yes"
	}
	return "no"
}

func joinAny(v any) string {
	list, ok := v.([]any)
	if !ok {
		if list, ok := v.([]string); ok {
			return strings.Join(list, ", ")
		}
		return str(v)
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		parts = append(parts, str(item))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func spaced(s string) string {
	out := make([]string, 0, len(s))
	for _, r := range s {
		out = append(out, string(r))
	}
	return strings.Join(out, " ")
}

func prefixIf(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

func usageErr(env Env, usage string) int {
	fmt.Fprintf(env.Stderr, "usage: mobiledeck %s\n", usage)
	return ExitUsage
}

func countButtons(p *profile.Profile) int {
	n := 0
	for i := range p.Pages {
		n += len(p.Pages[i].Buttons)
	}
	return n
}

var _ = json.Marshal

// cmdToken prints the admin token the browser panel asks for.
//
// The desktop window never needs this: it is handed the token in-process. The
// browser cannot be, because the token is a full-power credential for the admin
// API and a page on the LAN is exactly where it must not leak. So the user runs
// this once on the machine, pastes the value into the panel, and the browser
// remembers it.
func cmdToken(env Env, args []string) int {
	fs, g := newFlagSet(env, "token", "Print the key the browser panel asks for.")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	_, paths, err := resolve(g)
	if err != nil {
		return fail(env, err)
	}

	_, state, err := connectRuntime(runtimePathsFor(paths))
	if err != nil {
		if errors.Is(err, errHostUnreachable) {
			// A token only exists while the host runs: it is generated per
			// process, so there is nothing to print when nothing is listening.
			fmt.Fprintf(env.Stderr, "mobiledeck: %v\n", err)
			fmt.Fprintln(env.Stderr, "the key belongs to a running host, so start it first")
			return ExitNotRunning
		}
		return fail(env, err)
	}

	if g.json {
		return emitJSON(env, map[string]any{
			"admin_token": state.AdminToken,
			"addr":        state.Addr,
			"panel_url":   "http://" + state.Addr + "/",
		})
	}
	fmt.Fprintln(env.Stdout, state.AdminToken)
	if !g.quiet {
		fmt.Fprintf(env.Stderr, "\nopen the panel at http://%s/ and paste this when it asks.\n", state.Addr)
	}
	return ExitOK
}
