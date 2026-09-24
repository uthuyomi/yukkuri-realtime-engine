package realtime

import (
	"context"
	"errors"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
)

// AudioFlow is obtained under the session lock, but Reserve must be called
// after releasing that lock (and before taking the websocket writer lock).
func (s *Session) AudioFlow(id string, rate int) (*audio.FlowController, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) {
		return nil, context.Canceled
	}
	if s.audioFlow == nil {
		f, err := audio.NewFlowController(rate)
		if err != nil {
			return nil, err
		}
		if s.audioCreditRequired {
			f.RequireCredit()
		}
		s.audioFlow = f
	}
	return s.audioFlow, nil
}

func (s *Session) UpdateAudioCredit(id string, c audio.Credit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) || s.audioFlow == nil {
		return nil
	}
	if err := s.audioFlow.Update(c); err != nil {
		return err
	}
	return s.recordSourcePlaybackLocked(s.audioFlow.Snapshot().Played)
}

func (s *Session) reconcileAudioPlaybackLocked() {
	if s.audioFlow != nil && s.timeline != nil {
		s.timeline.SetPlayed(min(s.audioFlow.Snapshot().Played, s.timeline.Snapshot().SentFrames))
	}
}

// Negotiate before generation creation so a small client never receives a
// legacy-sized window before advertising its real capacity.
func (s *Session) RequireAudioCredit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generationID != "" {
		return errors.New("configure audio flow before generation")
	}
	s.audioCreditRequired = true
	return nil
}
