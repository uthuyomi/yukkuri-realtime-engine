package httptransport

import (
	"log"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

func (s *Server) consumeInputUpdates(session *realtime.Session, writer *realtimeWriter) {
	for {
		select {
		case <-session.Context().Done():
			return
		case update := <-session.InputUpdates():
			kind := update.EventType
			if update.Error != "" {
				update.Error = "turn detection failed"
			}
			var data any = update
			if kind == "" {
				kind = "input_audio.turn"
			}
			if update.Interruption != nil {
				safe := *update.Interruption
				if safe.Error != "" {
					safe.Error = "interruption classification failed"
				}
				data = safe
			}
			if update.Speculation != nil {
				data = update.Speculation
			}
			event, err := realtime.NewEvent(kind, session.ID(), update.Generation, data)
			if err == nil {
				err = writer.Event(session.Context(), event)
			}
			if err != nil {
				log.Printf("input event failed: %v", err)
				session.Close()
				return
			}
			if len(update.Audio) > 0 {
				u := update
				writer.Go(u.Context, func() {
					if u.SpeculationKey != nil {
						if p := session.PromoteSpeculation(u.Context, *u.SpeculationKey); p != nil {
							s.runPromotedInput(session, writer, p)
							return
						}
					}
					s.transcribeInputAudio(u.Context, session, writer, u.Audio, realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
				})
			}
		}
	}
}
