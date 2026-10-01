// Package proto defines the MobileDeck wire protocol (docs/PROTOCOL.md).
//
// This package is pure data: it performs no I/O, holds no global state, and
// depends only on the standard library. Everything that can be known about the
// protocol at compile time lives here, so the server, the engine, and the tests
// all agree on one set of constants.
package proto

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the protocol version implemented by this build.
const Version = 1

// MaxMessageBytes caps a single WebSocket frame. Larger frames are rejected
// before they are parsed, which is the mitigation for threat T5.
const MaxMessageBytes = 1 << 20 // 1 MiB

// Message types. The string values are the wire contract and must not change
// without a protocol version bump.
const (
	// Handshake and liveness.
	TypeHello   = "hello"
	TypeWelcome = "welcome"
	TypePing    = "ping"
	TypePong    = "pong"
	TypeError   = "error"

	// Profiles.
	TypeProfileList        = "profile.list"
	TypeProfileListResult  = "profile.list.result"
	TypeProfileGet         = "profile.get"
	TypeProfileGetResult   = "profile.get.result"
	TypeProfileSetActive   = "profile.set_active"
	TypeProfileReload      = "profile.reload"
	TypeProfileExport      = "profile.export"
	TypeProfileExportRes   = "profile.export.result"
	TypeProfileImport      = "profile.import"
	TypeEventProfileChange = "event.profile.changed"

	// Buttons.
	TypeButtonPress      = "button.press"
	TypeButtonRelease    = "button.release"
	TypeActionResult     = "action.result"
	TypeActionCancel     = "action.cancel"
	TypeEventActionEnd   = "event.action.finished"
	TypeEventButtonState = "event.button.state"

	// Telemetry.
	TypeTelemetrySubscribe = "telemetry.subscribe"
	TypeEventTelemetry     = "event.telemetry"
)

// Error codes (PROTOCOL.md §8).
const (
	CodeInvalidArgument = "invalid_argument"
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeUnsupported     = "unsupported"
	CodeRateLimited     = "rate_limited"
	CodeConflict        = "conflict"
	CodeCancelled       = "cancelled"
	CodeActionFailed    = "action_failed"
	CodeInvalidProfile  = "invalid_profile"
	CodeInternal        = "internal"
)

// WebSocket close codes (PROTOCOL.md §2.5).
const (
	CloseNormal          = 1000
	CloseTryAgainLater   = 1013
	CloseMalformed       = 4400
	CloseUnauthenticated = 4401
	CloseDeviceDisabled  = 4403
	CloseIdleTimeout     = 4408
	CloseRateLimited     = 4429
)

// Default timings, also advertised in `welcome` so the client does not guess.
const (
	DefaultHeartbeatMS = 15000
	DefaultIdleMS      = 45000
	HelloDeadlineMS    = 10000
)

// Envelope is the single frame shape for every message in both directions.
type Envelope struct {
	V       int             `json:"v"`
	ID      string          `json:"id,omitempty"`
	ReplyTo string          `json:"reply_to,omitempty"`
	Type    string          `json:"type"`
	TS      int64           `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ErrVersionUnsupported is returned by Decode when the envelope's version is
// not the one this build speaks.
var ErrVersionUnsupported = errors.New("proto: unsupported protocol version")

// Decode parses a frame and enforces the version gate. It does not validate the
// payload: that is the job of the message handler, which knows the type.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("proto: decode envelope: %w", err)
	}
	if e.Type == "" {
		return Envelope{}, errors.New("proto: envelope has no type")
	}
	if e.V != Version {
		return Envelope{}, ErrVersionUnsupported
	}
	return e, nil
}

// New builds an outbound envelope with the current version and timestamp.
func New(typ string, payload any) (Envelope, error) {
	e := Envelope{V: Version, Type: typ, TS: NowMS()}
	if payload == nil {
		e.Payload = json.RawMessage("{}")
		return e, nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("proto: encode payload for %s: %w", typ, err)
	}
	e.Payload = b
	return e, nil
}

// Reply builds an outbound envelope correlated to a request.
func Reply(req Envelope, typ string, payload any) (Envelope, error) {
	e, err := New(typ, payload)
	if err != nil {
		return Envelope{}, err
	}
	e.ReplyTo = req.ID
	return e, nil
}

// ErrorPayload is the body of an `error` message.
type ErrorPayload struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Pointer      string `json:"pointer,omitempty"`
	RetryAfterMS int    `json:"retry_after_ms,omitempty"`
}

// Errorf builds an error envelope payload.
func Errorf(code, format string, args ...any) ErrorPayload {
	return ErrorPayload{Code: code, Message: fmt.Sprintf(format, args...)}
}

// DecodePayload unmarshals a payload into v. An empty payload is treated as an
// empty object so handlers can use zero-value structs with `{}` on the wire.
func DecodePayload(e Envelope, v any) error {
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("proto: payload of %s: %w", e.Type, err)
	}
	return nil
}
