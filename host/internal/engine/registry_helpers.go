package engine

import (
	"encoding/json"

	"github.com/mobiledeck/mobiledeck/host/internal/auth"
)

// Base supplies the Type and Scope methods that every action shares. It is
// exported so actions in other packages (core actions and plugins) embed it
// without duplicating two trivial methods.
type Base struct {
	typ   string
	scope auth.Scope
}

// NewBase constructs an embedded Base. Every action needs exactly these two
// facts, so they are constructor arguments rather than struct literals: an
// action cannot be registered without declaring its scope.
func NewBase(typ string, scope auth.Scope) Base {
	return Base{typ: typ, scope: scope}
}

func (b Base) Type() string      { return b.typ }
func (b Base) Scope() auth.Scope { return b.scope }

// DecodeParams unmarshals action parameters strictly. Unknown fields are
// rejected because a misspelled parameter in a profile is a bug the user cannot
// see otherwise: the button would appear configured and do something else.
func DecodeParams(raw json.RawMessage, v any) error { return decodeParams(raw, v) }

// Emit sends an event to clients. A non-empty DeviceID targets one session;
// an empty one broadcasts.
func (e *Engine) Emit(ev Event) { e.emit(ev) }
