package platform

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// startDetached starts a program that must outlive the button press.
//
// exec.Command rather than CommandContext on purpose: a detached application
// has to keep running when the request's context ends, so binding it to a
// context would kill the window the user just asked for. The child still gets
// its own process group, so it is not signalled with the agent's group.
func startDetached(name string, opts LaunchOptions) error {
	path := name
	if !filepath.IsAbs(name) {
		found, err := exec.LookPath(name)
		if err != nil {
			return fmt.Errorf("platform: %s is not on PATH", name)
		}
		path = found
	}
	cmd := exec.Command(path, opts.Args...)
	cmd.Dir = ExpandPath("", opts.Dir)
	if len(opts.Env) > 0 {
		cmd.Env = MergeEnv(opts.Env)
	}
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: start %s: %w", name, err)
	}
	return cmd.Process.Release()
}

// shellQuote wraps s so a POSIX shell reads it back as one literal word. It is
// used where a directory or command has to be embedded in a shell line (a
// terminal emulator that only accepts a command, or an AppleScript snippet),
// because interpolating a raw path would let a space or a quote change the
// meaning of the line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
