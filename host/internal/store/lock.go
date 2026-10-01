package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrLocked means another live process already holds the lock.
var ErrLocked = errors.New("store: another mobiledeck instance is already running")

// Lock is an advisory pidfile lock. It is advisory because the host is a
// single-user tool: the goal is to give a clear error on double start, not to
// defend against a hostile local user.
type Lock struct {
	path string
	pid  int
	held bool
}

// AcquireLock takes the pidfile lock at path. A stale lock (pid not alive) is
// taken over; a live one returns ErrLocked.
func AcquireLock(path string) (*Lock, error) {
	pid := os.Getpid()
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d\n", pid)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("store: write pidfile %s: %v %v", path, werr, cerr)
			}
			return &Lock{path: path, pid: pid, held: true}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("store: create pidfile %s: %w", path, err)
		}

		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, fmt.Errorf("store: read pidfile %s: %w", path, rerr)
		}
		other, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if perr == nil && other != pid && processAlive(other) {
			return nil, fmt.Errorf("%w (pid %d)", ErrLocked, other)
		}
		// Stale: remove and retry once.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("store: clear stale pidfile %s: %w", path, err)
		}
	}
	return nil, ErrLocked
}

// Release removes the pidfile. It is safe to call twice.
func (l *Lock) Release() error {
	if l == nil || !l.held {
		return nil
	}
	l.held = false
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: remove pidfile %s: %w", l.path, err)
	}
	return nil
}

// ReadPID returns the pid recorded in a pidfile, and whether one is running.
func ReadPID(path string) (int, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("store: read pidfile %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false, fmt.Errorf("store: pidfile %s is corrupt: %w", path, err)
	}
	return pid, processAlive(pid), nil
}
