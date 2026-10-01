//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// createNewConsole gives the child its own console window (CREATE_NEW_CONSOLE).
// Without it a terminal launched by an agent that has no console of its own
// would open a window nobody can see.
const createNewConsole = 0x00000010

// windowsLauncher starts programs through the Windows shell.
type windowsLauncher struct {
	Unsupported
}

// Open starts a program.
//
// A real executable is started directly with CreateProcess, which passes the
// arguments through verbatim; anything else (a document, a shortcut, a batch
// file, a registered URI handler) goes through ShellExecuteW, because only the
// shell knows how to dispatch it.
//
// The distinction matters for safety, not just tidiness: ShellExecuteW builds a
// command line that the target's handler re-parses, so a document opened with
// an argument containing `&` would be split into a second command. CreateProcess
// has no such re-parsing step, which is why an application's arguments are only
// ever handed to it directly.
func (windowsLauncher) Open(_ context.Context, target string, opts LaunchOptions) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("platform: target must not be empty")
	}
	if path, ok := windowsExecutable(target); ok {
		return startDetached(path, opts)
	}
	if strings.ContainsAny(target, `/\`) {
		// A path-shaped target that is not an image: it must exist for the
		// shell to open it, and "does not exist" is more useful than the
		// shell's own message.
		if _, err := os.Stat(ExpandPath("", target)); err != nil {
			return fmt.Errorf("platform: %s does not exist", ExpandPath("", target))
		}
	}
	return shellExecute(target, opts)
}

// OpenURL opens a URL in the user's default browser.
//
// The URL is validated first: ShellExecuteW runs a local executable given a
// bare path, so an unchecked "url" field would be an arbitrary-execution
// primitive (common.go, SanitizeURL).
func (windowsLauncher) OpenURL(_ context.Context, rawURL string) error {
	url, err := SanitizeURL(rawURL)
	if err != nil {
		return err
	}
	return shellExecute(url, LaunchOptions{})
}

// OpenFolder opens a directory in Explorer.
func (windowsLauncher) OpenFolder(ctx context.Context, path string) error {
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
	// explorer.exe returns a non-zero exit code even on success, so the result is
	// deliberately not inspected. It is detached for the same reason a terminal
	// is: the file manager window belongs to the user, and tearing it down when
	// the action returns would close the folder the user just opened.
	return StartDetached(DetachedCommand("explorer.exe", abs))
}

// OpenTerminal opens a terminal emulator, preferring Windows Terminal and
// falling back to the classic console host.
func (windowsLauncher) OpenTerminal(ctx context.Context, opts TerminalOptions) error {
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

	// Windows Terminal is preferred when present. It creates its own window and
	// its own console, so it does not have the problem described below.
	if _, err := exec.LookPath("wt.exe"); err == nil {
		var args []string
		if dir != "" {
			args = append(args, "-d", dir)
		}
		if command != "" {
			args = append(args, "cmd.exe", "/K", command)
		}
		return startDetachedConsole("wt.exe", args, dir)
	}

	// No Windows Terminal: a classic console window.
	//
	// The console is created by `start` rather than by CREATE_NEW_CONSOLE on our
	// own child, and that indirection is the point. A child given a brand new
	// console has no console input buffer yet: the console is attached after the
	// process starts, cmd.exe reads end-of-input from it, and the window closes
	// immediately. Measured here: a child with CREATE_NEW_CONSOLE running
	// `cmd /K` exits in well under a second, while the same shell started through
	// `start` keeps running.
	//
	// `start` also makes the terminal a grandchild, which is correct for a
	// different reason: the window belongs to the user, not to the host.
	// The empty string is the window title, and it must be present and empty.
	// `start` treats its first non-option argument as the program to run, so a
	// named title is taken for the executable and the command fails with
	// "cannot find the file". Go quotes the empty argument as "", which is
	// exactly what start expects.
	args := []string{"/C", "start", "", "cmd.exe", "/K"}
	if command != "" {
		args = append(args, command)
	}
	return startDetachedConsole("cmd.exe", args, dir)
}

// startDetachedConsole starts a terminal that outlives the button press.
//
// The command is deliberately detached from the action's context. A terminal is
// something the user asked to open, not work the deck owns, and attaching the
// context made the window appear and be force-killed about a hundred
// milliseconds later, because the engine cancels the action's context the moment
// Run returns (see DetachedCommand).
func startDetachedConsole(name string, args []string, dir string) error {
	cmd := DetachedCommand(name, args...)
	cmd.Dir = dir
	if err := StartDetached(cmd); err != nil {
		return fmt.Errorf("platform: open terminal %s: %w", name, err)
	}
	return nil
}

// windowsExecutable reports the path to a program CreateProcess can start
// directly, and whether the target was one.
//
// Only .exe and .com are images: CreateProcess cannot run a batch file or a
// shortcut, so those must go to the shell even when a PATH lookup finds them.
func windowsExecutable(target string) (string, bool) {
	if strings.ContainsAny(target, `/\`) {
		abs := ExpandPath("", target)
		if !isWindowsImage(abs) {
			return "", false
		}
		if _, err := os.Stat(abs); err != nil {
			return "", false
		}
		return abs, true
	}
	// A bare name is resolved against PATH and PATHEXT, so "notepad" becomes
	// notepad.exe; a hit that is not an image is left to the shell.
	found, err := exec.LookPath(target)
	if err != nil || !isWindowsImage(found) {
		return "", false
	}
	return found, true
}

func isWindowsImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe", ".com":
		return true
	default:
		return false
	}
}

// shellExecute hands a path or URL to the shell.
//
// ShellExecuteW is used rather than `cmd /c start ""` because the latter
// re-parses its argument string, so a target containing & or ^ would turn into
// a second command.
func shellExecute(file string, opts LaunchOptions) error {
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return fmt.Errorf("platform: %q is not a valid Windows path: %w", file, err)
	}
	var argsPtr *uint16
	if len(opts.Args) > 0 {
		joined, err := joinArgs(file, opts.Args)
		if err != nil {
			return err
		}
		argsPtr, err = windows.UTF16PtrFromString(joined)
		if err != nil {
			return fmt.Errorf("platform: arguments for %s: %w", file, err)
		}
	}
	var dirPtr *uint16
	if opts.Dir != "" {
		dirPtr, err = windows.UTF16PtrFromString(ExpandPath("", opts.Dir))
		if err != nil {
			return fmt.Errorf("platform: working directory for %s: %w", file, err)
		}
	}
	// ShellExecuteW returns as soon as the target is launched; the process it
	// starts is not a child the agent can or should wait for.
	if err := windows.ShellExecute(0, nil, filePtr, argsPtr, dirPtr, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("platform: the shell could not open %s: %w", file, err)
	}
	return nil
}

// joinArgs renders the argument list for ShellExecuteW, using the quoting rules
// of whatever will re-parse it.
//
// This is a mitigation, not a guarantee, and it is worth being explicit about
// why. ShellExecuteW hands the string to the target's registered handler, and
// different handlers re-parse it differently: a document's application uses the
// C runtime rules, while cmd.exe's batch parser is a separate, older grammar
// whose exact handling of `&` inside quotes has varied between Windows
// versions. Applications never come through here — Open starts them with
// CreateProcess, where arguments are passed verbatim — so this path is reached
// only for documents, shortcuts, batch files and URI handlers.
//
// A batch file's arguments go through cmd.exe's parser, so the safest rendering
// for a script is one it reads back literally; a metacharacter that cmd would
// still interpret is rejected rather than quoted and hoped for.
func joinArgs(file string, args []string) (string, error) {
	if strings.EqualFold(filepath.Ext(file), ".bat") || strings.EqualFold(filepath.Ext(file), ".cmd") {
		quoted := make([]string, len(args))
		for i, a := range args {
			if strings.ContainsAny(a, "&|<>^") {
				return "", fmt.Errorf("platform: the argument %q contains a cmd metacharacter, which a batch file would re-interpret; pass it through a script instead", a)
			}
			quoted[i] = quoteWindowsArg(a)
		}
		return strings.Join(quoted, " "), nil
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quoteWindowsArg(a)
	}
	return strings.Join(quoted, " "), nil
}

// quoteWindowsArg renders one argument so CommandLineToArgvW returns it
// unchanged. The rule is the one the C runtime uses: an argument is quoted if
// it is empty or contains a space, tab or quote; inside the quotes, a backslash
// run before a quote is doubled and the quote is escaped.
func quoteWindowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := range len(s) {
		switch c := s[i]; c {
		case '\\':
			backslashes++
		case '"':
			// The backslashes precede a quote, so they must be doubled and the
			// quote escaped.
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			if backslashes > 0 {
				b.WriteString(strings.Repeat(`\`, backslashes))
				backslashes = 0
			}
			b.WriteByte(c)
		}
	}
	// Trailing backslashes would otherwise escape the closing quote.
	if backslashes > 0 {
		b.WriteString(strings.Repeat(`\`, backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}
