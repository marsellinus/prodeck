package engine

import (
	"time"

	"github.com/mobiledeck/mobiledeck/host/internal/proto"
)

// Protocol event types the engine emits. They are aliases rather than literals
// so a rename in the protocol package cannot silently desynchronise the two.
const (
	protoEventButtonState    = proto.TypeEventButtonState
	protoEventTelemetry      = proto.TypeEventTelemetry
	protoEventActionFinished = proto.TypeEventActionEnd
	protoEventProfileChanged = proto.TypeEventProfileChange
)

// Client-directed navigation events. These are sent to the client that caused
// them, never broadcast: opening a page on one phone must not move another
// phone's deck.
const (
	EventOpenPage      = "deck.open_page"
	EventBack          = "deck.back"
	EventChangeProfile = "deck.change_profile"
	EventNotify        = "deck.notify"
)

func nowMS() int64 { return time.Now().UnixMilli() }
