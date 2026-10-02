package engine

import (
	"fmt"

	"github.com/mobiledeck/mobiledeck/host/internal/platform"
)

// BuildRegistry creates the registry with every core action registered.
//
// Registration order is explicit and each group is a separate function, so a
// new domain of actions is added by adding one call here rather than by editing
// a switch. A failure to register is fatal at start-up: it means two action
// types collide, which is always a programming error.
func BuildRegistry(plat *platform.Platform, hostName, version string, e *Engine) (*Registry, error) {
	r := NewRegistry()

	groups := []struct {
		name string
		fn   func() error
	}{
		{"keyboard", func() error { return registerKeyboard(r, plat) }},
		{"mouse", func() error { return registerMouse(r, plat) }},
		{"apps", func() error { return registerApps(r, plat, e) }},
		{"scripts", func() error { return registerScripts(r, plat, e) }},
		{"media", func() error { return registerMedia(r, plat) }},
		{"sounds", func() error { return registerSounds(r, plat, e) }},
		{"system", func() error { return registerSystem(r, plat, hostName, version) }},
		{"flow", func() error { return registerFlow(r, e) }},
		{"navigation", func() error { return registerNav(r, e) }},
	}
	for _, g := range groups {
		if err := g.fn(); err != nil {
			return nil, fmt.Errorf("engine: registering %s actions: %w", g.name, err)
		}
	}
	return r, nil
}
