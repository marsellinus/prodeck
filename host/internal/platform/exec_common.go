package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// defaultCommandTimeout is applied when a profile does not set one. A command
// with no deadline could hang a button forever, and the deck has no way to
// cancel it from the UI.
const defaultCommandTimeout = 30 * time.Second

// cappedBuffer captures at most MaxCaptureBytes and remembers whether anything
// was dropped.
//
// TruncateCapture alone would cap the *returned* string while still letting a
// runaway process grow the buffer without limit, which is exactly the memory
// blow-up the cap exists to prevent. The cap therefore lives in the writer and
// TruncateCapture is applied on top for the final conversion.
type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := MaxCaptureBytes - c.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:remaining])
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	// The whole slice is accepted even when it is dropped: reporting a short
	// write would make os/exec treat the stream as broken.
	return len(p), nil
}

// runCaptured runs one child process and turns it into an ExecResult.
//
// The semantics are identical on every OS on purpose: a profile that works on
// the operator's laptop must behave the same way on a Linux workstation, so the
// timeout, the output cap and the TimedOut/Cancelled flags are decided here
// rather than per adapter. Only the process-group teardown differs, and that
// lives in exec_unix.go / exec_windows.go behind CommandContext.
func runCaptured(ctx context.Context, name string, args []string, dir string, env map[string]string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := CommandContext(runCtx, name, args...)
	if dir = ExpandPath("", dir); dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = MergeEnv(env)
	}

	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	res := ExecResult{Duration: time.Since(start)}

	var cut bool
	res.Stdout, cut = TruncateCapture(stdout.buf.Bytes())
	res.Truncated = cut || stdout.truncated
	res.Stderr, cut = TruncateCapture(stderr.buf.Bytes())
	res.Truncated = res.Truncated || cut || stderr.truncated

	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// The process never ran: a missing interpreter, a bad working
			// directory, a permission problem. That is a caller error, not a
			// command result, so it is reported as such.
			return res, fmt.Errorf("platform: could not run %s: %w", name, err)
		}
		res.ExitCode = exitErr.ExitCode()
		// A killed process also reports a non-zero code; the flags below say
		// why, which is what the client shows instead of a bare "exit 1".
		res.TimedOut, res.Cancelled = stopReason(ctx, runCtx)
	}
	return res, nil
}

// stopReason explains why a child that failed to finish was stopped. It is only
// meaningful when the command did not complete.
func stopReason(parent, run context.Context) (timedOut, cancelled bool) {
	if err := parent.Err(); err != nil {
		return errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled)
	}
	// The parent is still alive, so the only other deadline is our own.
	return errors.Is(run.Err(), context.DeadlineExceeded), false
}

// firstLine keeps a tool's error output to one line so the client's button
// status stays readable.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
