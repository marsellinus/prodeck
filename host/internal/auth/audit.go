package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// Event kinds written to the audit log.
const (
	EventPINIssued     = "pin.issued"
	EventPairAttempt   = "pair.attempt"
	EventPairSuccess   = "pair.success"
	EventAuth          = "auth"
	EventActionRun     = "action.run"
	EventDeviceRevoked = "device.revoked"
	EventDeviceUpdated = "device.updated"
	EventServerStart   = "server.start"
	EventServerStop    = "server.stop"
)

// Event is one audit record. Fields are added over time; consumers must ignore
// unknown ones, and the writer never emits a secret (docs/SECURITY.md §7).
type Event struct {
	TS         int64     `json:"ts"`
	Kind       string    `json:"event"`
	DeviceID   string    `json:"device_id,omitempty"`
	DeviceName string    `json:"device_name,omitempty"`
	IP         string    `json:"ip,omitempty"`
	OK         bool      `json:"ok,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Scopes     []string  `json:"scopes,omitempty"`
	ActionType string    `json:"action_type,omitempty"`
	ActionID   string    `json:"action_id,omitempty"`
	By         string    `json:"by,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
}

// Audit is an append-only JSONL security log.
//
// It is intentionally separate from the application log: the application log is
// for debugging and may be rotated away or set to debug level, while the audit
// trail must survive and must never contain secrets.
type Audit struct {
	path string

	mu   sync.Mutex
	file *os.File
}

// OpenAudit opens (or creates) the audit log, appending to any existing file.
func OpenAudit(path string) (*Audit, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("auth: create audit directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("auth: open audit log %s: %w", path, err)
	}
	return &Audit{path: path, file: f}, nil
}

// Record appends one event. It never returns an error: an audit write failure
// must not take down the host, and the failure is reported through the standard
// logger by the caller of the wrapping method instead.
func (a *Audit) Record(e Event) {
	if a == nil {
		return
	}
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return
	}
	_, _ = a.file.Write(b)
}

// Close flushes and closes the log.
func (a *Audit) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}

// Path returns the audit log location.
func (a *Audit) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Tail reads the last n records. It is used by the CLI so an operator can see
// security events without a JSON tool.
func TailAudit(path string, n int) ([]Event, error) {
	raw, err := store.ReadFileOr(path, nil)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	lines := splitLines(raw)
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// A partially written final line is expected after a hard kill;
			// skipping it is better than refusing to show anything.
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
