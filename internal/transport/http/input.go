package httptransport

import (
	"log"
	"sync"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

func (s *Server) consumeInputUpdates(session *realtime.Session, writer *realtimeWriter) {
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case <-session.Context().Done():
			return
		case update := <-session.InputUpdates():
			event, err := realtime.NewEvent("input_audio.turn", session.ID(), "", update)
			if err == nil {
				err = writer.Event(session.Context(), event)
			}
			if err != nil {
				log.Printf("input event failed: %v", err)
				session.Close()
				return
			}
			if len(update.Audio) > 0 {
				workers.Add(1)
				go func(u realtime.InputUpdate) {
					defer workers.Done()
					s.transcribeInputAudio(u.Context, session, writer, u.Audio, realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
				}(update)
			}
		}
	}
}
