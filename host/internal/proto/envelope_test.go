package proto

import (
	"encoding/json"
	"testing"
)

// TestEnvelopeRoundTrip pins the wire shape. If a field is renamed or its JSON
// tag changes, every client breaks; this test is the thing that makes that a
// build failure instead of a support ticket.
func TestEnvelopeRoundTrip(t *testing.T) {
	env, err := New(TypeButtonPress, ButtonPressPayload{
		ProfileID: "development",
		PageID:    "home",
		ButtonID:  "terminal",
		Press:     PressInfo{Kind: "short", Count: 1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env.ID = NewID()
	env.ReplyTo = ""

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The envelope must carry exactly these keys; an extra or missing key is a
	// protocol change.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, want := range []string{"v", "id", "type", "ts", "payload"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("envelope is missing %q; got keys %v", want, keysOf(fields))
		}
	}
	if _, ok := fields["reply_to"]; ok {
		t.Error("reply_to must be omitted on a request, not sent as null")
	}

	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.V != Version {
		t.Errorf("version = %d, want %d", decoded.V, Version)
	}
	if decoded.Type != TypeButtonPress {
		t.Errorf("type = %q, want %q", decoded.Type, TypeButtonPress)
	}
	if decoded.ID != env.ID {
		t.Errorf("id = %q, want %q", decoded.ID, env.ID)
	}

	var press ButtonPressPayload
	if err := DecodePayload(decoded, &press); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if press.ButtonID != "terminal" || press.PageID != "home" || press.ProfileID != "development" {
		t.Errorf("payload round-trip lost data: %+v", press)
	}
	if press.Press.Kind != "short" {
		t.Errorf("press.kind = %q, want %q", press.Press.Kind, "short")
	}
}

// TestDecodeRejectsOtherVersions is the version gate. A peer that speaks a
// version we do not implement must be rejected explicitly, never guessed at.
func TestDecodeRejectsOtherVersions(t *testing.T) {
	for _, v := range []int{0, 2, 99} {
		raw := []byte(`{"v":` + itoa(v) + `,"type":"hello","ts":1,"payload":{}}`)
		if _, err := Decode(raw); err == nil {
			t.Errorf("Decode accepted version %d", v)
		}
	}
}

// TestDecodeIgnoresUnknownFields is the compatibility guarantee from
// docs/PROTOCOL.md §11: a newer peer may add fields, and an older one must keep
// working. This is the property that makes additive evolution safe.
func TestDecodeIgnoresUnknownFields(t *testing.T) {
	raw := []byte(`{
		"v": 1,
		"id": "abc",
		"type": "hello",
		"ts": 1780000000000,
		"payload": {
			"device_id": "android-1",
			"token": "t",
			"a_field_from_the_future": {"nested": [1,2,3]},
			"client": {"platform": "android", "also_new": true}
		},
		"an_envelope_field_from_the_future": "ignored"
	}`)
	env, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var hello HelloPayload
	if err := DecodePayload(env, &hello); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if hello.DeviceID != "android-1" || hello.Token != "t" {
		t.Errorf("known fields were lost: %+v", hello)
	}
	if hello.Client.Platform != "android" {
		t.Errorf("nested known field was lost: %+v", hello.Client)
	}
}

// TestDecodeRejectsMissingType guards the one structural invariant the envelope
// has: every message is typed.
func TestDecodeRejectsMissingType(t *testing.T) {
	if _, err := Decode([]byte(`{"v":1,"ts":1,"payload":{}}`)); err == nil {
		t.Error("Decode accepted an envelope with no type")
	}
}

// TestDecodeRejectsGarbage ensures a malformed frame produces an error rather
// than a zero-valued envelope that a handler might act on.
func TestDecodeRejectsGarbage(t *testing.T) {
	for _, raw := range []string{``, `{`, `null`, `[]`, `"hello"`, `{"v":"one"}`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("Decode accepted %q", raw)
		}
	}
}

// TestNewIDShape pins the identifier format: 26 Crockford base32 characters.
// Clients use these as map keys and in logs, so the shape is part of the
// contract even though the value is random.
func TestNewIDShape(t *testing.T) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := NewID()
		if len(id) != 26 {
			t.Fatalf("id %q has length %d, want 26", id, len(id))
		}
		for i, r := range id {
			if !containsRune(crockford, r) {
				t.Fatalf("id %q has %q at %d, which is not Crockford base32", id, r, i)
			}
		}
		if seen[id] {
			t.Fatalf("NewID produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

// TestNewIDIsMonotonic checks the ordering guarantee that makes logs readable:
// IDs generated in sequence sort in generation order, even within one
// millisecond.
func TestNewIDIsMonotonic(t *testing.T) {
	prev := ""
	for range 5000 {
		id := NewID()
		if prev != "" && id <= prev {
			t.Fatalf("ids are not monotonic: %q came after %q", id, prev)
		}
		prev = id
	}
}

// TestErrorfCarriesCode checks the error payload shape.
func TestErrorfCarriesCode(t *testing.T) {
	e := Errorf(CodeForbidden, "needs the %q scope", "scripts")
	if e.Code != CodeForbidden {
		t.Errorf("code = %q, want %q", e.Code, CodeForbidden)
	}
	if e.Message != `needs the "scripts" scope` {
		t.Errorf("message = %q", e.Message)
	}
}

// TestReplyCorrelates checks that a reply carries the request id and does not
// reuse it as its own.
func TestReplyCorrelates(t *testing.T) {
	req := Envelope{V: Version, ID: "req-1", Type: TypePing, TS: 1}
	reply, err := Reply(req, TypePong, PongPayload{Echo: "x"})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if reply.ReplyTo != "req-1" {
		t.Errorf("reply_to = %q, want %q", reply.ReplyTo, "req-1")
	}
	if reply.ID == "req-1" {
		t.Error("a reply must not reuse the request id as its own id")
	}
	if reply.Type != TypePong {
		t.Errorf("type = %q, want %q", reply.Type, TypePong)
	}
}

// TestNilPayloadBecomesEmptyObject keeps the contract that payload is always an
// object, never null, so a client can unmarshal it unconditionally.
func TestNilPayloadBecomesEmptyObject(t *testing.T) {
	env, err := New(TypeProfileList, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if string(env.Payload) != "{}" {
		t.Errorf("payload = %s, want {}", env.Payload)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
