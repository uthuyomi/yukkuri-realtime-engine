package httptransport

import (
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
	"log"
)

// Failed output is terminal, including when playback was speculatively paused.
// Cancellation is ID-guarded so a late provider failure cannot cancel new output.
func (s *Server) cancelFailedGeneration(session *realtime.Session, writer *realtimeWriter, id string) {
	if session.CancelGenerationID(id) == "" {
		return
	}
	event, err := realtime.NewEvent("generation.cancelled", session.ID(), id, map[string]string{"reason": "output_failed"})
	if err == nil {
		err = writer.Event(session.Context(), event)
	}
	if err != nil {
		log.Printf("output cancellation event failed: %v", err)
	}
}
