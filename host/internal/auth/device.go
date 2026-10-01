package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// TokenBytes is the entropy of a device token. 32 bytes matches the security
// level of the SHA-256 that protects it at rest.
const TokenBytes = 32

// Device is a paired client. The token is never stored: only its hash.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Platform  string    `json:"platform,omitempty"`
	Model     string    `json:"model,omitempty"`
	TokenHash string    `json:"token_hash"` // hex sha256 of the token
	Scopes    []Scope   `json:"scopes"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	LastIP    string    `json:"last_ip,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"`
	// TokenFingerprint is a short, non-secret prefix of the token hash, shown
	// in `mobiledeck devices` so an operator can distinguish two phones without
	// ever handling a usable credential.
	TokenFingerprint string `json:"token_fingerprint"`
}

// ScopeSet returns the device's granted scopes as a set.
func (d Device) ScopeSet() ScopeSet { return NewScopeSet(d.Scopes) }

// deviceFile is the on-disk shape of devices.json.
type deviceFile struct {
	Schema  int      `json:"schema"`
	Devices []Device `json:"devices"`
}

const deviceSchema = 1

// deviceStore persists devices atomically and holds them in memory for
// lock-free reads.
type deviceStore struct {
	path string

	mu      sync.RWMutex
	devices map[string]Device
}

func newDeviceStore(path string) (*deviceStore, error) {
	s := &deviceStore{path: path, devices: map[string]Device{}}
	raw, err := store.ReadFileOr(path, nil)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return s, nil
	}
	var f deviceFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", path, err)
	}
	if f.Schema != deviceSchema {
		return nil, fmt.Errorf("auth: %s has schema %d, this build understands %d", path, f.Schema, deviceSchema)
	}
	for _, d := range f.Devices {
		if d.ID == "" || d.TokenHash == "" {
			return nil, fmt.Errorf("auth: %s contains a device without an id or token hash", path)
		}
		s.devices[d.ID] = d
	}
	return s, nil
}

// List returns devices sorted by creation time, newest last.
func (s *deviceStore) List() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Get returns one device.
func (s *deviceStore) Get(id string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	return d, ok
}

// Put inserts or replaces a device and flushes to disk.
func (s *deviceStore) Put(d Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.devices[d.ID]
	s.devices[d.ID] = d
	if err := s.flushLocked(); err != nil {
		// Roll back so memory and disk cannot disagree: a device that was not
		// persisted must not be usable.
		if existed {
			s.devices[d.ID] = prev
		} else {
			delete(s.devices, d.ID)
		}
		return err
	}
	return nil
}

// Delete removes a device.
func (s *deviceStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.devices[id]
	if !existed {
		return false, nil
	}
	delete(s.devices, id)
	if err := s.flushLocked(); err != nil {
		s.devices[id] = prev
		return false, err
	}
	return true, nil
}

// touch updates LastSeen/LastIP without a full device rewrite by the caller.
func (s *deviceStore) touch(id, ip string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return
	}
	d.LastSeen = at
	if ip != "" {
		d.LastIP = ip
	}
	s.devices[id] = d
	// Persisting on every connection would wear the disk on a busy LAN; the
	// in-memory value is what `devices` prints, and it is flushed on the next
	// write. Losing a timestamp on a hard kill is harmless.
}

func (s *deviceStore) flushLocked() error {
	f := deviceFile{Schema: deviceSchema, Devices: make([]Device, 0, len(s.devices))}
	for _, d := range s.devices {
		f.Devices = append(f.Devices, d)
	}
	sort.Slice(f.Devices, func(i, j int) bool { return f.Devices[i].ID < f.Devices[j].ID })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: encode devices: %w", err)
	}
	b = append(b, '\n')
	if err := store.WriteFileAtomic(s.path, b, 0o600); err != nil {
		return err
	}
	return nil
}

// newToken returns a fresh token and the record fields derived from it.
func newToken() (token, hash, fingerprint string, err error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("auth: generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	hash = hashToken(token)
	return token, hash, fingerprintOf(hash), nil
}

// hashToken is the one-way function protecting tokens at rest.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// fingerprintOf is the first 12 hex characters of the hash: enough to tell two
// devices apart in a listing, useless as a credential.
func fingerprintOf(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// verifyToken compares a presented token against a stored hash in constant
// time, so timing cannot be used to recover the hash byte by byte.
func verifyToken(presented, storedHash string) bool {
	got := hashToken(presented)
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// randomDigits returns n uniformly random decimal digits. It uses rejection
// sampling so every digit is equally likely, which matters because a biased PIN
// generator would quietly weaken the only thing standing between a LAN peer and
// the machine.
func randomDigits(n int) (string, error) {
	if n < 1 || n > 12 {
		return "", fmt.Errorf("auth: pin length %d must be 1..12", n)
	}
	out := make([]byte, n)
	buf := make([]byte, n*2)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("auth: generate pin: %w", err)
		}
		for _, b := range buf {
			if i >= n {
				break
			}
			// Reject the top of the byte range so the modulo is unbiased.
			if b >= 250 {
				continue
			}
			out[i] = '0' + b%10
			i++
		}
	}
	return string(out), nil
}

// ensurePathWritable fails early with a clear message when the config
// directory is not writable, instead of failing on the first pairing attempt.
func ensurePathWritable(path string) error {
	dir := path[:strings.LastIndexAny(path, `/\`)+1]
	if dir == "" {
		dir = "."
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("auth: %s does not exist", dir)
		}
		return fmt.Errorf("auth: %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("auth: %s is not a directory", dir)
	}
	return nil
}

var _ = fs.ErrNotExist
