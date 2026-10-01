//go:build linux

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// linuxLauncher starts programs through the desktop's own opener.
type linuxLauncher struct {
	Unsupported
}

// Open starts a program.
//
// A resolved path or a bare command name is started directly and detached, so
// the application outlives the button press; a URI goes through xdg-open.
func (linuxLauncher) Open(_ context.Context, target string, opts LaunchOptions) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("platform: target must not be empty")
	}
	if strings.Contains(target, "://") {
		return linuxOpenURL(target)
	}
	name := target
	if strings.ContainsRune(target, os.PathSeparator) {
		name = ExpandPath("", target)
		if _, err := os.Stat(name); err != nil {
			return fmt.Errorf("platform: %s does not exist", name)
		}
	} else if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("platform: %s is not on PATH", name)
	}
	return startDetached(name, opts)
}

// OpenURL opens a URL in the user's default browser.
func (linuxLauncher) OpenURL(_ context.Context, rawURL string) error {
	return linuxOpenURL(rawURL)
}

// linuxOpenURL validates and hands a URL to xdg-open.
func linuxOpenURL(rawURL string) error {
	url, err := SanitizeURL(rawURL)
	if err != nil {
		return err
	}
	// xdg-open's exit code is meaningless (it is often a detached helper that
	// reports failure after the browser opened), so the result is not treated
	// as authoritative; only a failure to start the tool is.
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: xdg-open is not available, so URLs cannot be opened: %w", err)
	}
	return cmd.Process.Release()
}

// OpenFolder opens a directory in the user's file manager.
func (linuxLauncher) OpenFolder(_ context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("platform: folder path must not be empty")
	}
	abs := ExpandPath("", path)
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("platform: %s does not exist", abs)
	}
	if !st.IsDir() {
		return fmt.Errorf("platform: %s is not a directory", abs)
	}
	cmd := exec.Command("xdg-open", abs)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: xdg-open is not available, so folders cannot be opened: %w", err)
	}
	return cmd.Process.Release()
}

// terminals lists the emulators tried in order, with the flag that sets their
// working directory. The order puts the session's own choice first and the
// universally installed fallbacks last.
var terminals = []struct {
	name    string
	dirFlag string
}{
	{"gnome-terminal", "--working-directory"},
	{"konsole", "--workdir"},
	{"xfce4-terminal", "--working-directory"},
	{"kitty", "--directory"},
	{"alacritty", "--working-directory"},
	{"foot", "--working-directory"},
	{"x-terminal-emulator", "--working-directory"},
	{"xterm", "-e"},
}

// OpenTerminal opens a terminal emulator.
//
// $TERMINAL wins when it is set, because that is the user telling us their
// preference; otherwise the first installed emulator from the list above is
// used. An emulator that takes no working-directory flag gets a command that
// changes directory itself.
func (linuxLauncher) OpenTerminal(_ context.Context, opts TerminalOptions) error {
	dir := ExpandPath("", opts.Dir)
	if dir != "" {
		st, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("platform: terminal working directory %s does not exist", dir)
		}
		if !st.IsDir() {
			return fmt.Errorf("platform: terminal working directory %s is not a directory", dir)
		}
	}
	command := strings.TrimSpace(opts.Command)

	name, dirFlag := linuxTerminalChoice()
	if name == "" {
		return fmt.Errorf("%w: no terminal emulator was found on PATH; install one (xterm is the smallest) or set $TERMINAL", ErrUnsupported)
	}

	var args []string
	if dir != "" {
		if dirFlag == "" {
			// No directory flag: change directory inside the shell we start.
			inner := "cd " + shellQuote(dir) + " && exec ${SHELL:-sh}"
			if command != "" {
				inner = "cd " + shellQuote(dir) + " && exec ${SHELL:-sh} -c " + shellQuote(command)
			}
			args = append(args, "-e", "sh", "-c", inner)
		} else {
			args = append(args, dirFlag, dir)
			if command != "" {
				args = append(args, "-e", "sh", "-c", command)
			}
		}
	} else if command != "" {
		args = append(args, "-e", "sh", "-c", command)
	}

	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: could not start %s: %w", name, err)
	}
	return cmd.Process.Release()
}

// linuxTerminalChoice returns the emulator to use and the flag it sets its
// working directory with (empty when it has none).
func linuxTerminalChoice() (string, string) {
	if term := strings.TrimSpace(os.Getenv("TERMINAL")); term != "" {
		if _, err := exec.LookPath(term); err == nil {
			return term, terminalDirFlag(term)
		}
	}
	for _, t := range terminals {
		if _, err := exec.LookPath(t.name); err == nil {
			return t.name, t.dirFlag
		}
	}
	return "", ""
}

// terminalDirFlag returns the working-directory flag for a known emulator.
func terminalDirFlag(name string) string {
	base := strings.ToLower(name)
	for _, t := range terminals {
		if t.name == base {
			return t.dirFlag
		}
	}
	return ""
}
