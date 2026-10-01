package server

import (
	"github.com/mobiledeck/mobiledeck/host/internal/engine"
	"github.com/mobiledeck/mobiledeck/host/internal/proto"
)

// emitEvent fans an engine event out to sessions.
//
// Fan-out is the server's job, not the engine's, because only the server knows
// which sessions are interested. The engine says what happened; the server
// decides who hears it.
func (s *Server) emitEvent(ev engine.Event) {
	env, err := proto.New(ev.Type, ev.Payload)
	if err != nil {
		s.log.Error("could not encode an engine event", "type", ev.Type, "error", err)
		return
	}

	if ev.DeviceID != "" {
		s.deliverToDevice(ev.DeviceID, env)
		return
	}
	s.broadcast(env, ev.Type, ev.Payload)
}

// deliverToDevice sends an event to every session of one device.
func (s *Server) deliverToDevice(deviceID string, env proto.Envelope) {
	s.mu.RLock()
	targets := make([]*Session, 0, 2)
	for _, sess := range s.sessions {
		if sess.device.ID == deviceID {
			targets = append(targets, sess)
		}
	}
	s.mu.RUnlock()
	for _, sess := range targets {
		sess.sendEnvelope(env)
	}
}

// broadcast sends an event to every session, filtered by relevance.
func (s *Server) broadcast(env proto.Envelope, typ string, payload any) {
	// A button state change is only meaningful to clients currently viewing
	// that profile. Sending it to everyone would make a phone on the Media
	// profile churn on every Development counter tick.
	profileID := ""
	if typ == proto.TypeEventButtonState {
		if p, ok := payload.(proto.EventButtonStatePayload); ok {
			profileID = p.ProfileID
		}
	}

	s.mu.RLock()
	targets := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if profileID != "" && sess.activeProfileLocked() != profileID {
			continue
		}
		targets = append(targets, sess)
	}
	s.mu.RUnlock()

	for _, sess := range targets {
		sess.sendEnvelope(env)
	}
}

// broadcastProfileChanged tells every client that a profile moved on disk.
func (s *Server) broadcastProfileChanged(profileID, revision string) {
	env, err := proto.New(proto.TypeEventProfileChange, proto.EventProfileChangedPayload{
		ProfileID: profileID,
		Revision:  revision,
	})
	if err != nil {
		s.log.Error("could not encode a profile-changed event", "error", err)
		return
	}
	s.mu.RLock()
	targets := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		targets = append(targets, sess)
	}
	s.mu.RUnlock()
	for _, sess := range targets {
		sess.sendEnvelope(env)
	}
}
