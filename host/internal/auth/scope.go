// Package auth owns everything that decides whether a device may act: the PIN
// handshake, per-device tokens, scopes, rate limits, and the audit log.
//
// It is deliberately free of HTTP and WebSocket concepts. The server asks
// "may this device run this action?" and gets a yes or a specific reason, which
// is what keeps the permission model testable without a network
// (docs/SECURITY.md §2).
package auth

import (
	"fmt"
	"sort"
	"strings"
)

// Scope is a permission a device may hold (docs/PROTOCOL.md §9).
type Scope string

// The complete scope set for protocol v1.
const (
	ScopeKeyboard      Scope = "keyboard"
	ScopeMouse         Scope = "mouse"
	ScopeMedia         Scope = "media"
	ScopeApps          Scope = "apps"
	ScopeScripts       Scope = "scripts"
	ScopeSystemRead    Scope = "system.read"
	ScopeSystemPower   Scope = "system.power"
	ScopeProfilesWrite Scope = "profiles.write"
	ScopePlugins       Scope = "plugins"
	ScopeNone          Scope = ""
)

// AllScopes lists every scope in a stable order, for the CLI and the docs.
func AllScopes() []Scope {
	return []Scope{
		ScopeKeyboard, ScopeMouse, ScopeMedia, ScopeApps, ScopeScripts,
		ScopeSystemRead, ScopeSystemPower, ScopeProfilesWrite, ScopePlugins,
	}
}

// HighRiskScopes are never granted by default. Pairing with these requires an
// explicit opt-in on the host, because they turn a phone into a shell or a
// power switch (docs/SECURITY.md §4).
func HighRiskScopes() []Scope { return []Scope{ScopeScripts, ScopeSystemPower} }

// DefaultScopes is what a newly paired device receives.
func DefaultScopes() []Scope {
	return []Scope{ScopeKeyboard, ScopeMouse, ScopeMedia, ScopeApps, ScopeSystemRead, ScopeProfilesWrite}
}

// ParseScope validates one scope name.
func ParseScope(s string) (Scope, error) {
	sc := Scope(strings.TrimSpace(strings.ToLower(s)))
	for _, known := range AllScopes() {
		if sc == known {
			return sc, nil
		}
	}
	return ScopeNone, fmt.Errorf("auth: unknown scope %q; known scopes are %s", s, ScopeList(AllScopes()))
}

// ParseScopes validates a list, rejecting duplicates.
func ParseScopes(list []string) ([]Scope, error) {
	out := make([]Scope, 0, len(list))
	seen := make(map[Scope]bool, len(list))
	for _, raw := range list {
		sc, err := ParseScope(raw)
		if err != nil {
			return nil, err
		}
		if seen[sc] {
			return nil, fmt.Errorf("auth: scope %q is listed twice", sc)
		}
		seen[sc] = true
		out = append(out, sc)
	}
	return out, nil
}

// ScopeList renders scopes for humans, sorted for stable output.
func ScopeList(scopes []Scope) string {
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, string(s))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ScopeSet is a membership test for a device's granted scopes.
type ScopeSet map[Scope]bool

// NewScopeSet builds a set from a slice.
func NewScopeSet(scopes []Scope) ScopeSet {
	set := make(ScopeSet, len(scopes))
	for _, s := range scopes {
		set[s] = true
	}
	return set
}

// Has reports whether a scope is granted. An empty scope means "no permission
// needed", which is how navigation actions work.
func (s ScopeSet) Has(sc Scope) bool {
	if sc == ScopeNone {
		return true
	}
	return s[sc]
}

// Sorted returns the granted scopes in a stable order.
func (s ScopeSet) Sorted() []Scope {
	out := make([]Scope, 0, len(s))
	for sc := range s {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
