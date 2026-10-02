// Package engine executes actions.
//
// It is the only place that decides *what* a button press does. It knows
// nothing about HTTP, WebSocket, or profiles-on-disk: it receives a validated
// request, looks the action up in a registry, checks the device's scope, and
// runs it.
//
// The registry is the extension point. Adding an action is adding a file and a
// Register call; there is no central switch over action names anywhere in the
// host, which is what keeps plugin support from degenerating into one large
// if/else (docs/adr/0005-action-registry.md).
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
)

// Sentinel errors the server maps onto protocol error codes.
var (
	ErrUnknownAction    = errors.New("engine: unknown action type")
	ErrForbidden        = errors.New("engine: the device lacks the scope this action requires")
	ErrInvalidParams    = errors.New("engine: invalid action parameters")
	ErrBusy             = errors.New("engine: the action queue is full")
	ErrCancelled        = errors.New("engine: the execution was cancelled")
	ErrUnknownExecution = errors.New("engine: unknown execution id")
)

// Request is everything an action needs to run. It is deliberately a flat value
// rather than a pointer into server state, so an action cannot reach back into
// a session and so requests are trivially loggable.
type Request struct {
	ExecutionID string
	DeviceID    string
	ProfileID   string
	PageID      string
	ButtonID    string
	ActionType  string
	Params      json.RawMessage
	Scopes      auth.ScopeSet
	// Depth is the macro nesting level, used to bound recursion.
	Depth int
}

// ActionKey is the stable identifier used for logs, audit records and button
// state, e.g. "development/home/terminal".
func (r Request) ActionKey() string {
	parts := make([]string, 0, 3)
	for _, p := range []string{r.ProfileID, r.PageID, r.ButtonID} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return r.ActionType
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "/" + p
	}
	return out
}

// Result is what an action returns. Output is action-specific and is sent to
// the client verbatim as `action.result.output`.
type Result struct {
	Output any    `json:"output,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Action is one executable capability.
type Action interface {
	// Type is the namespaced identifier from docs/PROTOCOL.md §10.
	Type() string
	// Scope is the permission a device must hold. ScopeNone means "always
	// allowed", which is correct for pure navigation.
	Scope() auth.Scope
	// Validate checks parameters before anything runs. It must be pure and
	// must not touch the system.
	Validate(params json.RawMessage) error
	// Run performs the action. It must honour ctx cancellation.
	Run(ctx context.Context, req Request) (Result, error)
}

// Registry maps action types to implementations.
type Registry struct {
	mu      sync.RWMutex
	actions map[string]Action
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{actions: make(map[string]Action)}
}

// Register adds an action, rejecting duplicates. A duplicate is always a bug:
// it means two packages claim the same type and the winner would depend on
// initialisation order.
func (r *Registry) Register(a Action) error {
	if a == nil {
		return errors.New("engine: cannot register a nil action")
	}
	t := a.Type()
	if t == "" {
		return errors.New("engine: cannot register an action with an empty type")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.actions[t]; exists {
		return fmt.Errorf("engine: action type %q is already registered", t)
	}
	r.actions[t] = a
	return nil
}

// Lookup finds an action by type.
func (r *Registry) Lookup(typ string) (Action, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.actions[typ]
	return a, ok
}

// Has reports whether a type is registered. It satisfies profile.ActionTypeSet,
// which is how profiles are validated against what this host can actually do.
func (r *Registry) Has(typ string) bool {
	_, ok := r.Lookup(typ)
	return ok
}

// Types lists every registered type, sorted.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.actions))
	for t := range r.actions {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Descriptions returns the registered types with their required scope, for
// `mobiledeck actions`.
func (r *Registry) Descriptions() []ActionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ActionInfo, 0, len(r.actions))
	for _, a := range r.actions {
		out = append(out, ActionInfo{Type: a.Type(), Scope: string(a.Scope())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// ActionInfo describes a registered action.
type ActionInfo struct {
	Type  string `json:"type"`
	Scope string `json:"scope"`
}

// Options configures the engine.
type Options struct {
	MaxConcurrent   int
	QueueDepth      int
	DefaultTimeout  time.Duration
	MaxMacroSteps   int
	MaxMacroDepth   int
	MaxMacroTimeout time.Duration
	// ScriptRoots are the directories run_script may reference. Empty means
	// only the profile directory.
	ScriptRoots []string
	// AllowAbsolutePaths disables the confinement check entirely. It is an
	// explicit operator decision, surfaced by --allow-absolute-paths.
	AllowAbsolutePaths bool
	// ProfileDir maps a profile id to its on-disk directory, used to resolve
	// profile-relative script paths. Injected so the engine needs no loader.
	ProfileDir func(profileID string) string
	// ProfileExists and PageExists let the navigation actions reject a target
	// that does not exist. Injected for the same reason: the engine must not
	// depend on the profile loader, and the CLI builds an engine without one.
	ProfileExists func(profileID string) bool
	PageExists    func(profileID, pageID string) bool
	// SoundsDir is the only directory sound.play may play a file from. Empty
	// means the action has nothing to resolve against and refuses every file.
	SoundsDir string
	// SoundReferenced reports whether any loaded profile still uses a sound
	// file, so the admin API can refuse to delete one that a button depends on.
	// Injected for the same reason the profile lookups are: the engine must not
	// depend on the profile loader, and there is one implementation of the walk.
	SoundReferenced func(file string) bool
}

// Event is something the engine wants to push to clients.
type Event struct {
	DeviceID string // empty means broadcast
	Type     string
	Payload  any
}

// AuditFunc records a security-relevant event.
type AuditFunc func(auth.Event)

// Engine dispatches actions.
type Engine struct {
	reg   *Registry
	plat  *platform.Platform
	log   *slog.Logger
	opts  Options
	audit AuditFunc

	sem chan struct{}

	mu      sync.Mutex
	running map[string]*execution
	states  map[string]ButtonState
	counter uint64

	latches *toggleLatch

	emit func(Event)
}

// execution tracks one in-flight action so it can be cancelled.
type execution struct {
	id     string
	cancel context.CancelFunc
	req    Request
	start  time.Time
}

// New creates an engine. The platform adapters are injected, never discovered,
// which is what lets the engine be tested against fakes.
func New(reg *Registry, plat *platform.Platform, log *slog.Logger, opts Options, audit AuditFunc, emit func(Event)) *Engine {
	if opts.MaxConcurrent < 1 {
		opts.MaxConcurrent = 8
	}
	if opts.QueueDepth < opts.MaxConcurrent {
		opts.QueueDepth = opts.MaxConcurrent * 4
	}
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = 30 * time.Second
	}
	if opts.MaxMacroSteps <= 0 {
		opts.MaxMacroSteps = 256
	}
	if opts.MaxMacroDepth <= 0 {
		opts.MaxMacroDepth = 8
	}
	if opts.MaxMacroTimeout <= 0 {
		opts.MaxMacroTimeout = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	if emit == nil {
		emit = func(Event) {}
	}
	return &Engine{
		reg:     reg,
		plat:    plat,
		log:     log,
		opts:    opts,
		audit:   audit,
		sem:     make(chan struct{}, opts.QueueDepth),
		running: make(map[string]*execution),
		states:  make(map[string]ButtonState),
		latches: newToggleLatch(),
		emit:    emit,
	}
}

// Registry exposes the action registry.
func (e *Engine) Registry() *Registry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reg
}

// SetRegistry installs the action registry after construction. The engine needs
// it, and the registry needs the engine (so an action can emit events and
// resolve paths), so one of the two has to be wired second. This is that seam.
func (e *Engine) SetRegistry(reg *Registry) {
	e.mu.Lock()
	e.reg = reg
	e.mu.Unlock()
}

// SetProfileDir installs the profile-directory lookup used to resolve
// profile-relative script paths.
func (e *Engine) SetProfileDir(fn func(profileID string) string) {
	e.mu.Lock()
	e.opts.ProfileDir = fn
	e.mu.Unlock()
}

// SetProfileExists installs the profile-existence check used by the navigation
// actions.
func (e *Engine) SetProfileExists(fn func(profileID string) bool) {
	e.mu.Lock()
	e.opts.ProfileExists = fn
	e.mu.Unlock()
}

// SetPageExists installs the page-existence check used by the navigation
// actions.
func (e *Engine) SetPageExists(fn func(profileID, pageID string) bool) {
	e.mu.Lock()
	e.opts.PageExists = fn
	e.mu.Unlock()
}

// SetSoundsDir installs the directory sound.play may play from. It is set after
// construction because the configuration is loaded before the engine is built,
// and an action that runs without it refuses every file rather than guessing a
// directory.
func (e *Engine) SetSoundsDir(dir string) {
	e.mu.Lock()
	e.opts.SoundsDir = dir
	e.mu.Unlock()
}

// SetSoundReferenced installs the check that reports whether a loaded profile
// still uses a sound file. The admin API needs it to refuse a delete that would
// break a button.
func (e *Engine) SetSoundReferenced(fn func(file string) bool) {
	e.mu.Lock()
	e.opts.SoundReferenced = fn
	e.mu.Unlock()
}

// SoundReferenced reports whether any loaded profile still uses a sound file.
// It answers false when no lookup is installed, which is the case for the CLI's
// `actions` listing and for unit tests.
func (e *Engine) SoundReferenced(file string) bool {
	e.mu.Lock()
	fn := e.opts.SoundReferenced
	e.mu.Unlock()
	if fn == nil {
		return false
	}
	return fn(file)
}

// lookup finds an action, tolerating a nil registry so an engine can be
// constructed before its registry exists.
func (e *Engine) lookup(typ string) (Action, bool) {
	e.mu.Lock()
	reg := e.reg
	e.mu.Unlock()
	if reg == nil {
		return nil, false
	}
	return reg.Lookup(typ)
}

// Platform exposes the platform adapters to actions.
func (e *Engine) Platform() *platform.Platform { return e.plat }

// Options returns the engine options.
func (e *Engine) Options() Options { return e.opts }

// NewExecutionID returns a fresh execution identifier.
func NewExecutionID() string { return "ex-" + proto.NewID() }

// Execute runs one action synchronously and returns its result.
//
// The caller decides whether to wait for it: the server waits briefly and then
// switches to the accepted/finished event pair for slow actions.
func (e *Engine) Execute(ctx context.Context, req Request) (Result, error) {
	if req.ExecutionID == "" {
		req.ExecutionID = NewExecutionID()
	}

	action, ok := e.lookup(req.ActionType)
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownAction, req.ActionType)
	}
	if !req.Scopes.Has(action.Scope()) {
		e.recordAudit(auth.Event{
			Kind: auth.EventActionRun, DeviceID: req.DeviceID, ActionType: req.ActionType,
			ActionID: req.ActionKey(), OK: false, Reason: "forbidden_scope",
			Detail: "requires scope " + string(action.Scope()),
		})
		return Result{}, fmt.Errorf("%w: %q needs the %q scope", ErrForbidden, req.ActionType, action.Scope())
	}
	if err := action.Validate(req.Params); err != nil {
		e.recordAudit(auth.Event{
			Kind: auth.EventActionRun, DeviceID: req.DeviceID, ActionType: req.ActionType,
			ActionID: req.ActionKey(), OK: false, Reason: "invalid_params", Detail: err.Error(),
		})
		// Both errors are wrapped: the caller needs to know the request was
		// malformed, and the specific cause (an unknown action inside a macro,
		// say) must stay inspectable with errors.Is.
		return Result{}, fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}

	// Bounded admission: a full queue is answered immediately rather than
	// blocking the session's reader goroutine, so one device cannot stall
	// another device's traffic (ARCHITECTURE.md §3.3).
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	default:
		return Result{}, ErrBusy
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	exec := &execution{id: req.ExecutionID, cancel: cancel, req: req, start: time.Now()}
	e.mu.Lock()
	e.running[req.ExecutionID] = exec
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, req.ExecutionID)
		e.mu.Unlock()
	}()

	started := time.Now()
	res, err := action.Run(runCtx, req)
	elapsed := time.Since(started)

	ev := auth.Event{
		Kind: auth.EventActionRun, DeviceID: req.DeviceID, ActionType: req.ActionType,
		ActionID: req.ActionKey(), OK: err == nil,
	}
	if err != nil {
		ev.Reason = classify(err)
	}
	e.recordAudit(ev)

	if err != nil {
		e.log.Debug("action failed",
			"type", req.ActionType, "action", req.ActionKey(), "device", req.DeviceID,
			"duration_ms", elapsed.Milliseconds(), "error", err)
		return res, err
	}
	e.log.Debug("action ran",
		"type", req.ActionType, "action", req.ActionKey(), "device", req.DeviceID,
		"duration_ms", elapsed.Milliseconds())
	return res, nil
}

// Cancel stops a running execution. It reports whether the execution existed.
func (e *Engine) Cancel(executionID string) bool {
	e.mu.Lock()
	exec, ok := e.running[executionID]
	e.mu.Unlock()
	if !ok {
		return false
	}
	exec.cancel()
	return true
}

// Running lists in-flight execution ids.
func (e *Engine) Running() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.running))
	for id := range e.running {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// CancelAll stops every running execution. It is called on shutdown so a
// script started from the deck does not outlive the host that owns it.
func (e *Engine) CancelAll() {
	e.mu.Lock()
	execs := make([]*execution, 0, len(e.running))
	for _, exec := range e.running {
		execs = append(execs, exec)
	}
	e.mu.Unlock()
	for _, exec := range execs {
		exec.cancel()
	}
}

func (e *Engine) recordAudit(ev auth.Event) {
	if e.audit != nil {
		e.audit(ev)
	}
}

// classify turns an error into a short, non-sensitive reason string for the
// audit log.
func classify(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, platform.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrInvalidParams):
		return "invalid_params"
	default:
		return "failed"
	}
}

// ErrorCode maps an engine error to a protocol error code (docs/PROTOCOL.md §8).
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUnknownAction):
		return proto.CodeUnsupported
	case errors.Is(err, ErrForbidden):
		return proto.CodeForbidden
	case errors.Is(err, ErrInvalidParams):
		return proto.CodeInvalidArgument
	case errors.Is(err, ErrBusy):
		return proto.CodeRateLimited
	case errors.Is(err, ErrCancelled), errors.Is(err, context.Canceled):
		return proto.CodeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return proto.CodeActionFailed
	case errors.Is(err, platform.ErrUnsupported):
		return proto.CodeUnsupported
	default:
		return proto.CodeActionFailed
	}
}

// decodeParams unmarshals action parameters strictly: an unknown field is a
// typo in a profile, and silently ignoring it would produce a button that looks
// configured and does something else.
func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parameters: %w", err)
	}
	return nil
}

// SetEmitter replaces the event sink. The server needs the engine to emit into
// its fan-out, and the engine is constructed before the server exists, so the
// sink is installed afterwards rather than passed in.
func (e *Engine) SetEmitter(fn func(Event)) {
	if fn == nil {
		return
	}
	e.mu.Lock()
	e.emit = fn
	e.mu.Unlock()
}
