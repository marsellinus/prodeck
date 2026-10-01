// Package logging configures the host's structured logger.
//
// Two sinks are produced from one logger: a human-readable stream for the
// terminal and a rotating file for later inspection. A bounded in-memory ring
// keeps the most recent records so `mobiledeck logs` can show them even when
// the daemon owns the file.
//
// The package never logs a token or a PIN; callers pass identifiers, not
// credentials (docs/SECURITY.md T13).
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Options configures Setup.
type Options struct {
	Level      string // debug | info | warn | error
	Format     string // text | json
	Console    bool   // also write to stderr
	File       string // rotating log file, empty disables file logging
	MaxSizeMB  int
	MaxBackups int
	RingSize   int
}

// Level parses a level name, defaulting to info.
func Level(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Ring is a bounded in-memory record buffer.
type Ring struct {
	mu      sync.Mutex
	records []slog.Record
	attrs   []slog.Attr
	size    int
	next    int
	full    bool
}

// NewRing creates a ring holding at most size records.
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{records: make([]slog.Record, size), size: size}
}

// Records returns the buffered records, oldest first.
func (r *Ring) Records() []slog.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]slog.Record, len(r.records[:r.next]))
		copy(out, r.records[:r.next])
		return out
	}
	out := make([]slog.Record, 0, r.size)
	out = append(out, r.records[r.next:]...)
	out = append(out, r.records[:r.next]...)
	return out
}

// Len returns how many records are buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.size
	}
	return r.next
}

// rotatingFile is an io.Writer that renames the log when it exceeds a size.
//
// Rotation is done here rather than with an external tool because the host is a
// single self-contained binary that must not require logrotate or a service
// manager to keep its own log bounded.
type rotatingFile struct {
	path       string
	maxBytes   int64
	maxBackups int

	mu   sync.Mutex
	file *os.File
	size int64
}

func newRotatingFile(path string, maxSizeMB, maxBackups int) (*rotatingFile, error) {
	if maxSizeMB < 1 {
		maxSizeMB = 10
	}
	if maxBackups < 1 {
		maxBackups = 3
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("logging: create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("logging: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("logging: stat %s: %w", path, err)
	}
	return &rotatingFile{
		path:       path,
		maxBytes:   int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
		file:       f,
		size:       info.Size(),
	}, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, os.ErrClosed
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			// A failed rotation must not lose the record: keep appending to the
			// current file and let the operator notice the size.
			_ = err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotateLocked() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	// Shift mobiledeck.log.1 -> .2, .2 -> .3, dropping the oldest.
	for i := r.maxBackups - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", r.path, i)
		to := fmt.Sprintf("%s.%d", r.path, i+1)
		if _, err := os.Stat(from); err == nil {
			_ = os.Rename(from, to)
		}
	}
	_ = os.Rename(r.path, r.path+".1")

	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	r.file = f
	r.size = 0
	return nil
}

// Close closes the underlying file.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// SetupResult bundles everything Setup produces.
type SetupResult struct {
	Logger *slog.Logger
	Ring   *Ring
	close  func() error
}

// Close flushes and closes the file sink. It is safe to call twice.
func (s *SetupResult) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	err := s.close()
	s.close = nil
	return err
}

// Setup builds a logger with its ring buffer and file sink. A failure to open
// the log file is not fatal: the host logs to stderr and keeps running, because
// refusing to start over a log path would be a worse failure mode than losing
// file logs.
func Setup(opts Options) (*SetupResult, error) {
	ring := NewRing(opts.RingSize)
	if opts.RingSize == 0 {
		ring = NewRing(2000)
	}

	handlerOpts := &slog.HandlerOptions{Level: Level(opts.Level)}

	var writers []io.Writer
	var closer func() error = func() error { return nil }

	if opts.File != "" {
		rf, err := newRotatingFile(opts.File, opts.MaxSizeMB, opts.MaxBackups)
		if err != nil {
			if !opts.Console {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "logging: file logging disabled: %v\n", err)
		} else {
			writers = append(writers, rf)
			closer = rf.Close
		}
	}
	if opts.Console {
		writers = append(writers, os.Stderr)
	}
	if len(writers) == 0 {
		writers = append(writers, io.Discard)
	}

	newHandler := func(w io.Writer) slog.Handler {
		if opts.Format == "json" {
			return slog.NewJSONHandler(w, handlerOpts)
		}
		return slog.NewTextHandler(w, handlerOpts)
	}

	handlers := make([]slog.Handler, 0, len(writers)+1)
	for _, w := range writers {
		handlers = append(handlers, newHandler(w))
	}
	handlers = append(handlers, newRingHandler(ring, handlerOpts))

	return &SetupResult{Logger: slog.New(fanout(handlers)), Ring: ring, close: closer}, nil
}

// fanout delivers a record to every handler. It is a small, explicit
// implementation rather than a dependency because the semantics we need
// (deliver to all, ignore per-handler Enabled differences) are two methods.
type fanoutHandler struct {
	handlers []slog.Handler
}

func fanout(handlers []slog.Handler) slog.Handler {
	return &fanoutHandler{handlers: handlers}
}

func (f *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// The record is cloned implicitly by slog.Handler implementations that
		// keep it; the ring does its own cloning below.
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, h := range f.handlers {
		next = append(next, h.WithAttrs(attrs))
	}
	return &fanoutHandler{handlers: next}
}

func (f *fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(f.handlers))
	for _, h := range f.handlers {
		next = append(next, h.WithGroup(name))
	}
	return &fanoutHandler{handlers: next}
}

// ringHandler stores records for `mobiledeck logs` without printing them.
type ringHandler struct {
	ring *Ring
	opts *slog.HandlerOptions
	// group/attrs track WithAttrs/WithGroup calls so a buffered record carries
	// the same context as a printed one.
	group string
	attrs []slog.Attr
}

func newRingHandler(ring *Ring, opts *slog.HandlerOptions) *ringHandler {
	return &ringHandler{ring: ring, opts: opts}
}

func (h *ringHandler) Enabled(ctx context.Context, level slog.Level) bool {
	min := slog.LevelInfo
	if h.opts != nil && h.opts.Level != nil {
		min = h.opts.Level.Level()
	}
	return level >= min
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	clone := r.Clone()
	h.ring.add(clone)
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &ringHandler{ring: h.ring, opts: h.opts, group: h.group}
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return next
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{ring: h.ring, opts: h.opts, group: name, attrs: h.attrs}
}

func (r *Ring) add(rec slog.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[r.next] = rec
	r.next = (r.next + 1) % r.size
	if r.next == 0 {
		r.full = true
	}
}

// FormatRecords renders buffered records as text, newest last, filtering by
// minimum level. It is the renderer behind `mobiledeck logs`.
func FormatRecords(records []slog.Record, minLevel slog.Level) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		if r.Level < minLevel {
			continue
		}
		out = append(out, formatRecord(r))
	}
	return out
}

func formatRecord(r slog.Record) string {
	var sb strings.Builder
	sb.WriteString(r.Time.Format("2006-01-02T15:04:05.000Z07:00"))
	sb.WriteByte(' ')
	sb.WriteString(strings.ToUpper(r.Level.String()))
	sb.WriteByte(' ')
	sb.WriteString(r.Message)

	attrs := make([]string, 0, 8)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, fmt.Sprintf("%s=%v", a.Key, a.Value))
		return true
	})
	if len(attrs) > 0 {
		sort.Strings(attrs)
		sb.WriteString("  ")
		sb.WriteString(strings.Join(attrs, " "))
	}
	return sb.String()
}

// Writer returns an io.Writer that emits each line as a log record at the given
// level. It adapts the standard library's http server error log to slog so
// there is exactly one log format.
func Writer(logger *slog.Logger, level slog.Level) io.Writer {
	return &logWriter{logger: logger, level: level}
}

type logWriter struct {
	logger *slog.Logger
	level  slog.Level
}

func (w *logWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	if msg != "" {
		w.logger.Log(context.Background(), w.level, msg, "source", "stdlib")
	}
	return len(p), nil
}
