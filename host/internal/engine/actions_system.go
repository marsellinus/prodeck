package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// systemControl covers the session and machine power verbs.
//
// Every verb that can lose a user's work requires an explicit confirm flag in
// the parameters, in addition to the system.power scope. That is two
// independent barriers, which is deliberate for actions that cannot be undone
// (docs/SECURITY.md §4).
type systemControl struct {
	Base
	plat *platform.Platform
	verb string
}

type powerParams struct {
	// Confirm must be true. It exists so a misconfigured button cannot shut the
	// machine down on a stray tap.
	Confirm bool `json:"confirm,omitempty"`
}

// destructivePowerVerbs are the ones that need Confirm.
var destructivePowerVerbs = map[string]bool{
	"system.shutdown": true,
	"system.restart":  true,
	"system.sleep":    true,
}

func (a *systemControl) Validate(raw json.RawMessage) error {
	var p powerParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if destructivePowerVerbs[a.verb] && !p.Confirm {
		return fmt.Errorf("%s requires \"confirm\": true in its parameters, because it cannot be undone; the client shows a confirmation sheet before sending it", a.verb)
	}
	return nil
}

func (a *systemControl) Run(ctx context.Context, req Request) (Result, error) {
	caps := a.plat.Power.Capabilities()
	switch a.verb {
	case "system.lock":
		if !caps.Lock {
			return Result{}, fmt.Errorf("%w: locking this session", platform.ErrUnsupported)
		}
		return powerResult("locked the session", a.plat.Power.Lock(ctx))
	case "system.sleep":
		if !caps.Sleep {
			return Result{}, fmt.Errorf("%w: suspending this machine", platform.ErrUnsupported)
		}
		return powerResult("suspending", a.plat.Power.Sleep(ctx))
	case "system.shutdown":
		if !caps.Shutdown {
			return Result{}, fmt.Errorf("%w: shutting down this machine (the agent runs unprivileged and will not elevate)", platform.ErrUnsupported)
		}
		return powerResult("shutting down", a.plat.Power.Shutdown(ctx))
	case "system.restart":
		if !caps.Restart {
			return Result{}, fmt.Errorf("%w: restarting this machine (the agent runs unprivileged and will not elevate)", platform.ErrUnsupported)
		}
		return powerResult("restarting", a.plat.Power.Restart(ctx))
	default:
		return Result{}, fmt.Errorf("%w: system verb %q", ErrUnknownAction, a.verb)
	}
}

func powerResult(detail string, err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	return Result{Detail: detail}, nil
}

// systemStats returns one telemetry metric on demand, so a button can act on a
// reading without subscribing to the whole stream.
type systemStats struct {
	Base
	plat *platform.Platform
}

func (a *systemStats) Validate(raw json.RawMessage) error {
	var p struct {
		Metric string `json:"metric,omitempty"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	return nil
}

func (a *systemStats) Run(ctx context.Context, req Request) (Result, error) {
	var p struct {
		Metric string `json:"metric,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	values, err := a.plat.Metrics.Sample(ctx)
	if err != nil {
		return Result{}, err
	}
	if p.Metric == "" {
		return Result{Output: map[string]any{"values": values}}, nil
	}
	v, ok := values[p.Metric]
	if !ok {
		return Result{}, fmt.Errorf("%w: metric %q is not produced by this host", platform.ErrUnsupported, p.Metric)
	}
	return Result{Output: map[string]any{"metric": p.Metric, "value": v}}, nil
}

// systemInfo reports host capabilities, used by the CLI and by the client to
// grey out buttons that cannot work here.
type systemInfo struct {
	Base
	plat *platform.Platform
	host string
	os   string
	ver  string
}

func (a *systemInfo) Validate(raw json.RawMessage) error {
	var p noParams
	return decodeParams(raw, &p)
}

func (a *systemInfo) Run(ctx context.Context, req Request) (Result, error) {
	return Result{Output: map[string]any{
		"host":    a.host,
		"os":      a.os,
		"version": a.ver,
		"power":   a.plat.Power.Capabilities(),
		"metrics": a.plat.Metrics.Available(),
		"shells":  a.plat.Shell.SupportedInterpreters(),
	}}, nil
}

func registerSystem(r *Registry, plat *platform.Platform, hostName, version string) error {
	verbs := []string{"system.lock", "system.sleep", "system.shutdown", "system.restart"}
	for _, v := range verbs {
		if err := r.Register(&systemControl{Base: NewBase(v, auth.ScopeSystemPower), plat: plat, verb: v}); err != nil {
			return err
		}
	}
	if err := r.Register(&systemStats{Base: NewBase("system.stats", auth.ScopeSystemRead), plat: plat}); err != nil {
		return err
	}
	return r.Register(&systemInfo{
		Base: NewBase("system.info", auth.ScopeSystemRead),
		plat: plat, host: hostName, os: plat.OSName, ver: version,
	})
}
