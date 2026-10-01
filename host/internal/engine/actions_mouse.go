package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// mouseClick presses and releases a mouse button.
type mouseClick struct {
	Base
	plat *platform.Platform
}

type clickParams struct {
	Button string `json:"button,omitempty"`
	Count  int    `json:"count,omitempty"`
}

func (a *mouseClick) Validate(raw json.RawMessage) error {
	var p clickParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if _, err := platform.ParseMouseButton(p.Button); err != nil {
		return err
	}
	if p.Count < 0 || p.Count > 10 {
		return fmt.Errorf("count must be 1..10, got %d", p.Count)
	}
	return nil
}

func (a *mouseClick) Run(ctx context.Context, req Request) (Result, error) {
	var p clickParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	btn, err := platform.ParseMouseButton(p.Button)
	if err != nil {
		return Result{}, err
	}
	count := p.Count
	if count == 0 {
		count = 1
	}
	if err := a.plat.Input.MouseClick(ctx, btn, count); err != nil {
		return Result{}, err
	}
	return Result{Detail: fmt.Sprintf("%s click x%d", btn, count)}, nil
}

// mouseMove moves the pointer relatively or to a normalised absolute position.
type mouseMove struct {
	Base
	plat     *platform.Platform
	absolute bool
}

type moveParams struct {
	DX *int     `json:"dx,omitempty"`
	DY *int     `json:"dy,omitempty"`
	X  *float64 `json:"x,omitempty"`
	Y  *float64 `json:"y,omitempty"`
}

func (a *mouseMove) Validate(raw json.RawMessage) error {
	var p moveParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if a.absolute {
		if p.X == nil || p.Y == nil {
			return fmt.Errorf("x and y are both required and must be fractions of the screen, 0..1")
		}
		if *p.X < 0 || *p.X > 1 || *p.Y < 0 || *p.Y > 1 {
			return fmt.Errorf("x and y must be within 0..1, got %v and %v", *p.X, *p.Y)
		}
		return nil
	}
	if p.DX == nil || p.DY == nil {
		return fmt.Errorf("dx and dy are both required")
	}
	if abs(*p.DX) > 20000 || abs(*p.DY) > 20000 {
		return fmt.Errorf("dx and dy are limited to 20000 pixels per call")
	}
	return nil
}

func (a *mouseMove) Run(ctx context.Context, req Request) (Result, error) {
	var p moveParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	if a.absolute {
		if err := a.plat.Input.MouseMoveAbsolute(ctx, *p.X, *p.Y); err != nil {
			return Result{}, err
		}
		return Result{Detail: fmt.Sprintf("moved to %.3f,%.3f", *p.X, *p.Y)}, nil
	}
	if err := a.plat.Input.MouseMove(ctx, *p.DX, *p.DY); err != nil {
		return Result{}, err
	}
	return Result{Detail: fmt.Sprintf("moved by %d,%d", *p.DX, *p.DY)}, nil
}

// mouseScroll scrolls by whole notches.
type mouseScroll struct {
	Base
	plat *platform.Platform
}

type scrollParams struct {
	DX *int `json:"dx,omitempty"`
	DY *int `json:"dy,omitempty"`
}

func (a *mouseScroll) Validate(raw json.RawMessage) error {
	var p scrollParams
	if err := decodeParams(raw, &p); err != nil {
		return err
	}
	if p.DX == nil && p.DY == nil {
		return fmt.Errorf("at least one of dx or dy is required")
	}
	for name, v := range map[string]*int{"dx": p.DX, "dy": p.DY} {
		if v != nil && abs(*v) > 100 {
			return fmt.Errorf("%s is limited to 100 notches per call, got %d", name, *v)
		}
	}
	return nil
}

func (a *mouseScroll) Run(ctx context.Context, req Request) (Result, error) {
	var p scrollParams
	if err := decodeParams(req.Params, &p); err != nil {
		return Result{}, err
	}
	var dx, dy int
	if p.DX != nil {
		dx = *p.DX
	}
	if p.DY != nil {
		dy = *p.DY
	}
	if err := a.plat.Input.MouseScroll(ctx, dx, dy); err != nil {
		return Result{}, err
	}
	return Result{Detail: fmt.Sprintf("scrolled %d,%d", dx, dy)}, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func registerMouse(r *Registry, plat *platform.Platform) error {
	actions := []Action{
		&mouseClick{Base: NewBase("mouse.click", auth.ScopeMouse), plat: plat},
		&mouseMove{Base: NewBase("mouse.move", auth.ScopeMouse), plat: plat},
		&mouseMove{Base: NewBase("mouse.move_absolute", auth.ScopeMouse), plat: plat, absolute: true},
		&mouseScroll{Base: NewBase("mouse.scroll", auth.ScopeMouse), plat: plat},
	}
	for _, a := range actions {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
