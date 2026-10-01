package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
)

// delay pauses a macro.
type delay struct {
	Base
}

type delayParams struct {
	MS int `json:"ms"`
}

// MaxDelayMS bounds a single pause. A deck button that sleeps for an hour is
// indistinguishable from a hang, and the execution would hold a worker slot.
const MaxDelayMS = 600000

func (a *delay) Validate(raw json.RawMessage) error {
	var p delayParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if p.MS < 0 || p.MS > MaxDelayMS {
		return fmt.Errorf("ms must be 0..%d, got %d", MaxDelayMS, p.MS)
	}
	return nil
}

func (a *delay) Run(ctx context.Context, req Request) (Result, error) {
	var p delayParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	timer := time.NewTimer(time.Duration(p.MS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-timer.C:
	}
	return Result{Detail: fmt.Sprintf("waited %dms", p.MS)}, nil
}

// macro runs a sequence of actions.
//
// Its steps are ordinary action objects, not a separate DSL. That single choice
// gives nesting, per-step timeouts, cancellation and forward compatibility with
// every action added later, including plugin actions, for free
// (docs/adr/0005-action-registry.md).
type macro struct {
	Base
	engine *Engine
}

type macroParams struct {
	Steps []struct {
		Type    string          `json:"type"`
		Params  json.RawMessage `json:"params,omitempty"`
		OnError string          `json:"on_error,omitempty"`
		Label   string          `json:"label,omitempty"`
	} `json:"steps"`
	StopOnError    *bool `json:"stop_on_error,omitempty"`
	StepTimeoutMS  int   `json:"step_timeout_ms,omitempty"`
	TotalTimeoutMS int   `json:"total_timeout_ms,omitempty"`
}

func (a *macro) Validate(raw json.RawMessage) error {
	var p macroParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("steps must contain at least one action")
	}
	if len(p.Steps) > a.engine.opts.MaxMacroSteps {
		return fmt.Errorf("steps has %d entries; the maximum is %d", len(p.Steps), a.engine.opts.MaxMacroSteps)
	}
	for i, s := range p.Steps {
		if s.Type == "" {
			return fmt.Errorf("steps[%d].type must not be empty", i)
		}
		switch s.OnError {
		case "", "abort", "continue":
		default:
			return fmt.Errorf("steps[%d].on_error must be \"abort\" or \"continue\", got %q", i, s.OnError)
		}
		// Validate the step against the registry so a broken macro is rejected
		// before it runs halfway and leaves the machine in a partial state.
		action, ok := a.engine.lookup(s.Type)
		if !ok {
			return fmt.Errorf("%w: steps[%d] references %q", ErrUnknownAction, i, s.Type)
		}
		if err := action.Validate(orEmptyRaw(s.Params)); err != nil {
			return fmt.Errorf("steps[%d] (%s): %w", i, s.Type, err)
		}
	}
	if p.StepTimeoutMS < 0 || p.StepTimeoutMS > MaxDelayMS {
		return fmt.Errorf("step_timeout_ms must be 0..%d, got %d", MaxDelayMS, p.StepTimeoutMS)
	}
	if p.TotalTimeoutMS < 0 || p.TotalTimeoutMS > MaxDelayMS*10 {
		return fmt.Errorf("total_timeout_ms is out of range")
	}
	return nil
}

func (a *macro) Run(ctx context.Context, req Request) (Result, error) {
	var p macroParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	if req.Depth >= a.engine.opts.MaxMacroDepth {
		return Result{}, fmt.Errorf("macro nesting exceeds the limit of %d", a.engine.opts.MaxMacroDepth)
	}

	total := a.engine.opts.MaxMacroTimeout
	if p.TotalTimeoutMS > 0 {
		total = time.Duration(p.TotalTimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, total)
	defer cancel()

	stopOnError := true
	if p.StopOnError != nil {
		stopOnError = *p.StopOnError
	}

	stepTimeout := a.engine.opts.DefaultTimeout
	if p.StepTimeoutMS > 0 {
		stepTimeout = time.Duration(p.StepTimeoutMS) * time.Millisecond
	}

	executed := 0
	for i, step := range p.Steps {
		if err := runCtx.Err(); err != nil {
			return Result{Output: map[string]any{"steps_run": executed, "steps_total": len(p.Steps)}},
				fmt.Errorf("macro stopped at step %d: %w", i, err)
		}

		action, ok := a.engine.lookup(step.Type)
		if !ok {
			return Result{}, fmt.Errorf("%w: step %d references %q", ErrUnknownAction, i, step.Type)
		}
		// Each step carries the caller's scopes: a macro cannot be used to
		// escalate, which is the whole point of checking here as well.
		if !req.Scopes.Has(action.Scope()) {
			err := fmt.Errorf("%w: step %d (%s) needs the %q scope", ErrForbidden, i, step.Type, action.Scope())
			if stopOnError && step.OnError != "continue" {
				return Result{Output: map[string]any{"steps_run": executed, "failed_step": i}}, err
			}
			continue
		}

		stepCtx, stepCancel := context.WithTimeout(runCtx, stepTimeout)
		subReq := req
		subReq.ActionType = step.Type
		subReq.Params = orEmptyRaw(step.Params)
		subReq.Depth = req.Depth + 1
		subReq.ExecutionID = req.ExecutionID // one cancellation domain for the whole macro

		_, err := a.engine.executeStep(stepCtx, subReq, action)
		stepCancel()
		if err != nil {
			if stopOnError && step.OnError != "continue" {
				return Result{Output: map[string]any{"steps_run": executed, "failed_step": i, "failed_type": step.Type}},
					fmt.Errorf("macro aborted at step %d (%s): %w", i, step.Type, err)
			}
			a.engine.log.Warn("macro step failed, continuing",
				"step", i, "type", step.Type, "error", err, "action", req.ActionKey())
		}
		executed++
	}
	return Result{
		Output: map[string]any{"steps_run": executed, "steps_total": len(p.Steps)},
		Detail: fmt.Sprintf("ran %d steps", executed),
	}, nil
}

// executeStep runs one macro step with the same bookkeeping as a top-level
// action, minus the queue admission: the outer macro already holds a slot.
func (e *Engine) executeStep(ctx context.Context, req Request, action Action) (Result, error) {
	started := time.Now()
	res, err := action.Run(ctx, req)
	e.log.Debug("macro step",
		"type", req.Type(), "action", req.ActionKey(),
		"duration_ms", time.Since(started).Milliseconds(), "ok", err == nil)
	return res, err
}

// Type reports the action type of a request, used only for logging.
func (r Request) Type() string { return r.ActionType }

func orEmptyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// noop does nothing. It exists so a button can be a deliberate placeholder, and
// so tests have a side-effect-free action to press.
type noop struct {
	Base
}

func (a *noop) Validate(raw json.RawMessage) error {
	var p noParams
	return decodeParams(raw, &p)
}

func (a *noop) Run(ctx context.Context, req Request) (Result, error) {
	return Result{Detail: "did nothing"}, nil
}

func registerFlow(r *Registry, e *Engine) error {
	actions := []Action{
		&delay{Base: NewBase("delay", auth.ScopeNone)},
		&macro{Base: NewBase("macro", auth.ScopeNone), engine: e},
		&noop{Base: NewBase("noop", auth.ScopeNone)},
	}
	for _, a := range actions {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
