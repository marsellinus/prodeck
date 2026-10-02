// Package profiles loads, validates, watches and serves the profile documents
// that clients render.
//
// The registry is host-authoritative: the file on disk is the single source of
// truth, the client holds only a cache, and a profile that fails validation is
// never served. That rule is what makes hand-editing a profile safe
// (docs/adr/0007-profile-as-directory-of-json.md).
package profiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/profile"
	"github.com/mobiledeck/mobiledeck/host/internal/store"
)

// ErrNotFound means no profile with that id is loaded.
var ErrNotFound = errors.New("profiles: no such profile")

// Entry is one loaded profile plus its provenance.
type Entry struct {
	Doc      *profile.Profile
	Dir      string
	Path     string
	Revision string
	LoadedAt time.Time
}

// Registry holds the loaded profiles and watches their directories.
type Registry struct {
	root    string
	dir     string // the profiles/ directory itself
	log     *slog.Logger
	actions profile.ActionTypeSet

	mu      sync.RWMutex
	entries map[string]*Entry
	order   []string
	lastErr map[string]error

	watchStop chan struct{}
	watchWG   sync.WaitGroup
	onChange  func(profileID, revision string)
}

// Options configures the registry.
type Options struct {
	// Dir is the profiles directory. It is created if missing.
	Dir string
	// Actions validates action types against what the host can execute.
	Actions profile.ActionTypeSet
	// Watch enables periodic re-scan so editing a file updates connected
	// clients without restarting the host.
	Watch bool
	// WatchInterval is how often to re-scan. A poll rather than an inotify
	// watcher because the host must work identically on Windows, Linux and
	// macOS, and a profile edit is a rare, human-paced event.
	WatchInterval time.Duration
	// OnChange is called when a profile's content changes on disk.
	OnChange func(profileID, revision string)
}

// New creates a registry and loads every profile it finds.
func New(opts Options, log *slog.Logger) (*Registry, error) {
	if log == nil {
		log = slog.Default()
	}
	if opts.WatchInterval <= 0 {
		opts.WatchInterval = 2 * time.Second
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("profiles: create %s: %w", opts.Dir, err)
	}
	r := &Registry{
		dir:      opts.Dir,
		log:      log,
		actions:  opts.Actions,
		entries:  make(map[string]*Entry),
		lastErr:  make(map[string]error),
		onChange: opts.OnChange,
	}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	if opts.Watch {
		r.startWatch(opts.WatchInterval)
	}
	return r, nil
}

// Dir returns the profiles directory.
func (r *Registry) Dir() string { return r.dir }

// Reload re-reads every profile from disk. Profiles that fail validation are
// removed from the registry but their previous good version is not resurrected:
// an operator who breaks a file must see the error, not silently keep the old
// layout.
func (r *Registry) Reload() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("profiles: read %s: %w", r.dir, err)
	}

	loaded := make(map[string]*Entry)
	failures := make(map[string]error)
	ids := make([]string, 0, len(entries))

	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		dir := filepath.Join(r.dir, de.Name())
		path := filepath.Join(dir, "profile.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // a directory without profile.json is simply not a profile
			}
			failures[de.Name()] = err
			continue
		}
		doc, err := profile.Load(raw, r.actions)
		if err != nil {
			failures[de.Name()] = err
			r.log.Error("profile rejected", "profile", de.Name(), "path", path, "error", err)
			continue
		}
		// The directory name is authoritative for the id: it is what the
		// operator types in the CLI, and a mismatch between the folder and the
		// document would make `mobiledeck profiles` lie.
		if doc.ID != de.Name() {
			failures[de.Name()] = fmt.Errorf("profile: /id is %q but the directory is named %q; they must match", doc.ID, de.Name())
			r.log.Error("profile rejected", "profile", de.Name(), "error", failures[de.Name()])
			continue
		}
		doc.ApplyDefaults()
		doc.Dir = dir
		doc.SourcePath = path
		loaded[doc.ID] = &Entry{
			Doc:      doc,
			Dir:      dir,
			Path:     path,
			Revision: RevisionOf(raw),
			LoadedAt: time.Now(),
		}
		ids = append(ids, doc.ID)
	}

	sort.Strings(ids)

	r.mu.Lock()
	prev := r.entries
	r.entries = loaded
	r.order = ids
	r.lastErr = failures
	r.mu.Unlock()

	// Notify about changes after the swap, so a listener that immediately reads
	// the registry sees the new content.
	for id, e := range loaded {
		if old, ok := prev[id]; !ok || old.Revision != e.Revision {
			if r.onChange != nil {
				r.onChange(id, e.Revision)
			}
		}
	}
	return nil
}

// ReloadOne re-reads a single profile.
func (r *Registry) ReloadOne(id string) (*Entry, error) {
	path := filepath.Join(r.dir, id, "profile.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profiles: read %s: %w", path, err)
	}
	doc, err := profile.Load(raw, r.actions)
	if err != nil {
		r.mu.Lock()
		r.lastErr[id] = err
		r.mu.Unlock()
		return nil, err
	}
	if doc.ID != id {
		return nil, fmt.Errorf("profiles: /id is %q but the directory is named %q", doc.ID, id)
	}
	doc.ApplyDefaults()
	doc.Dir = filepath.Join(r.dir, id)
	doc.SourcePath = path

	entry := &Entry{Doc: doc, Dir: doc.Dir, Path: path, Revision: RevisionOf(raw), LoadedAt: time.Now()}
	r.mu.Lock()
	r.entries[id] = entry
	if !contains(r.order, id) {
		r.order = append(r.order, id)
		sort.Strings(r.order)
	}
	delete(r.lastErr, id)
	r.mu.Unlock()
	return entry, nil
}

// Get returns a loaded profile.
func (r *Registry) Get(id string) (*profile.Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return nil, false
	}
	return e.Doc, true
}

// Entry returns the full entry for a profile.
func (r *Registry) Entry(id string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	return e, ok
}

// DirOf returns a profile's directory, satisfying engine.Options.ProfileDir.
func (r *Registry) DirOf(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.entries[id]; ok {
		return e.Dir
	}
	return ""
}

// List returns the loaded profiles in a stable order.
func (r *Registry) List() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.order))
	for _, id := range r.order {
		if e, ok := r.entries[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

// IDs returns the loaded profile ids.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Errors returns the load failures, so `mobiledeck profiles` can report a broken
// file instead of pretending it does not exist.
func (r *Registry) Errors() map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]error, len(r.lastErr))
	for k, v := range r.lastErr {
		out[k] = v
	}
	return out
}

// Default returns the profile to use when none is chosen: the first loaded one
// in sorted order, or nil when the registry is empty.
func (r *Registry) Default() *Entry {
	list := r.List()
	if len(list) == 0 {
		return nil
	}
	return list[0]
}

// SoundReferenced reports whether any loaded profile still plays a sound file.
//
// It is the lookup the admin API injects into the engine so deleting a sound
// that a button depends on is refused. It walks every loaded document rather
// than only the active one because a profile a phone is not currently showing
// still breaks the moment it is opened.
func (r *Registry) SoundReferenced(file string) bool {
	if file == "" {
		return false
	}
	r.mu.RLock()
	docs := make([]*profile.Profile, 0, len(r.entries))
	for i := range r.order {
		if e, ok := r.entries[r.order[i]]; ok {
			docs = append(docs, e.Doc)
		}
	}
	r.mu.RUnlock()

	for _, doc := range docs {
		for _, used := range doc.SoundFiles() {
			if sameFileName(used, file) {
				return true
			}
		}
	}
	return false
}

// sameFileName compares two sound file names the way the host's filesystem
// does. Windows and macOS are case-insensitive, so a profile naming "Boom.wav"
// and a delete of "boom.wav" are the same file there; comparing byte-for-byte
// would let the delete through and leave a button pointing at nothing.
func sameFileName(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Revision is a content hash of a profile document, used by the client to tell
// whether its cache is stale and by the host to detect a real change.
func RevisionOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// RegistryRevision is a hash over every profile revision, advertised in
// `/api/v1/info` so a client can tell in one round trip whether anything moved.
func (r *Registry) RegistryRevision() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h := sha256.New()
	for _, id := range r.order {
		if e, ok := r.entries[id]; ok {
			fmt.Fprintf(h, "%s=%s\n", id, e.Revision)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// startWatch polls the profiles directory and reloads on change.
func (r *Registry) startWatch(interval time.Duration) {
	r.watchStop = make(chan struct{})
	r.watchWG.Add(1)
	go func() {
		defer r.watchWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.watchStop:
				return
			case <-ticker.C:
				if err := r.Reload(); err != nil {
					r.log.Warn("profile rescan failed", "error", err)
				}
			}
		}
	}()
}

// Close stops the watcher.
func (r *Registry) Close() {
	if r.watchStop == nil {
		return
	}
	close(r.watchStop)
	r.watchWG.Wait()
	r.watchStop = nil
}

// Export renders a profile back to JSON, for `profile.export` and for the CLI.
// The output is the normalised document (defaults applied), which is what a user
// editing by hand should start from.
func (r *Registry) Export(id string) ([]byte, error) {
	e, ok := r.Entry(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	b, err := json.MarshalIndent(e.Doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("profiles: encode %s: %w", id, err)
	}
	return append(b, '\n'), nil
}

// Import validates a profile document and writes it to disk. overwrite=false
// refuses to clobber an existing profile, which is the safe default for a
// restore-from-backup flow.
func (r *Registry) Import(raw []byte, overwrite bool) (*Entry, error) {
	doc, err := profile.Load(raw, r.actions)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(r.dir, doc.ID)
	if _, err := os.Stat(filepath.Join(dir, "profile.json")); err == nil && !overwrite {
		return nil, fmt.Errorf("profiles: %q already exists; pass overwrite to replace it", doc.ID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("profiles: create %s: %w", dir, err)
	}
	if err := store.WriteFileAtomic(filepath.Join(dir, "profile.json"), raw, 0o600); err != nil {
		return nil, err
	}
	return r.ReloadOne(doc.ID)
}

// Save writes a profile document to disk and reloads it. It is the write path
// used by the (future) layout editor and by the CLI.
func (r *Registry) Save(id string, doc *profile.Profile) error {
	if err := doc.Validate(r.actions); err != nil {
		return err
	}
	if doc.ID != id {
		return fmt.Errorf("profiles: document id %q does not match the target %q", doc.ID, id)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("profiles: encode %s: %w", id, err)
	}
	b = append(b, '\n')
	path := filepath.Join(r.dir, id, "profile.json")
	if err := store.WriteFileAtomic(path, b, 0o600); err != nil {
		return err
	}
	_, err = r.ReloadOne(id)
	return err
}

// ActionTypes returns the action type set the registry validates against. The
// engine uses it to build the same view.
func (r *Registry) ActionTypes() profile.ActionTypeSet { return r.actions }

// BuildActionSet turns an engine registry into the validation predicate the
// profile loader uses. It is the single place that couples "what a profile may
// reference" to "what this host can run", which is why an uninstalled plugin
// action is rejected at load time rather than at press time.
func BuildActionSet(reg *engine.Registry) profile.ActionTypeSet {
	return func(t string) bool { return reg.Has(t) }
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var _ = strings.TrimSpace
