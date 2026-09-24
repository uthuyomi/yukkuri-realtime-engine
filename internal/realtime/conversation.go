package realtime

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/conversation"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

type ConversationUpdate struct {
	ConversationID  string              `json:"conversation_id"`
	ItemID          string              `json:"item_id"`
	Role            conversation.Role   `json:"role"`
	Status          conversation.Status `json:"status"`
	TurnID          string              `json:"turn_id,omitempty"`
	GenerationID    string              `json:"generation_id,omitempty"`
	ContentBytes    int                 `json:"content_bytes"`
	SentTextBytes   int                 `json:"sent_text_bytes"`
	PlayedChunks    int                 `json:"played_chunks"`
	GeneratedFrames int64               `json:"generated_frames"`
	SentFrames      int64               `json:"sent_frames"`
	PlayedFrames    int64               `json:"played_frames"`
}

func (s *Session) ConversationID() string { return s.conversation.ID }
func (s *Session) ConversationSnapshot() conversation.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversation.Snapshot()
}
func (s *Session) ConversationUpdates() <-chan ConversationUpdate { return s.conversationUpdates }
func (s *Session) InputTurnID(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx != s.responseContext {
		return ""
	}
	return s.responseTurn
}
func (s *Session) notifyConversationLocked(p *conversation.Item) {
	if p == nil {
		return
	}
	n := 0
	for _, c := range p.Chunks {
		if c.Played {
			n++
		}
	}
	u := ConversationUpdate{ConversationID: s.conversation.ID, ItemID: p.ID, Role: p.Role, Status: p.Status, TurnID: p.TurnID, GenerationID: p.GenerationID, ContentBytes: len(p.Content), SentTextBytes: len(p.SentText), PlayedChunks: n, GeneratedFrames: p.GeneratedFrames, SentFrames: p.SentFrames, PlayedFrames: p.PlayedFrames}
	select {
	case s.conversationUpdates <- u:
	default:
	} // bounded, metadata only
}
func (s *Session) syncConversationPlaybackLocked() {
	p := s.conversation.Assistant(s.generationID)
	if p == nil || s.timeline == nil {
		return
	}
	before := p.Status
	t := s.timeline.Snapshot()
	s.conversation.Progress(s.generationID, t.SentFrames, t.PlayedFrames)
	if p.Status != before {
		s.notifyConversationLocked(p)
	}
}
func (s *Session) finalizeConversationLocked() {
	s.syncConversationPlaybackLocked()
	p := s.conversation.Assistant(s.generationID)
	if p == nil {
		return
	}
	before := p.Status
	s.conversation.Finalize(s.generationID)
	if before != p.Status {
		s.notifyConversationLocked(p)
	}
}
func (s *Session) activeConversationGenerationLocked(id string) bool {
	return id != "" && id == s.generationID && s.generationCtx != nil && s.generationCtx.Err() == nil && s.ctx.Err() == nil
}
func (s *Session) AppendAssistantText(id, text string, sent bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) {
		return context.Canceled
	}
	return s.conversation.Append(id, text, sent)
}
func (s *Session) MarkAssistantTextDone(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) {
		return false
	}
	p := s.conversation.Assistant(id)
	before := p.Status
	s.conversation.Done(id, false)
	if before != p.Status {
		s.notifyConversationLocked(p)
	}
	return true
}
func (s *Session) GenerationTextOnly(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return id == s.generationID && s.generationTextOnly
}
func (s *Session) GenerationRequest(id string) (llm.Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) {
		return llm.Request{}, false
	}
	return llm.Request{Messages: append([]llm.Message(nil), s.generationRequest.Messages...)}, true
}
func (s *Session) StartGenerationMode(textOnly bool) (string, context.Context, *speech.Pipeline) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateSpeculationLocked("generation_replaced")
	if s.responseCancel != nil {
		s.responseCancel()
	}
	return s.startGenerationWithTurnLocked(newID("turn"), textOnly)
}

// Only server-created committed-input contexts can commit user items. A context
// is consumed once, so duplicate STT/promotion completions cannot create output.
func (s *Session) beginConversationResponseLocked(ctx context.Context, text string, textOnly bool) (string, context.Context, *speech.Pipeline, error) {
	if ctx == nil || ctx != s.responseContext || ctx.Err() != nil || s.ctx.Err() != nil || s.responseUsed {
		return "", nil, nil, context.Canceled
	}
	if strings.TrimSpace(text) == "" {
		s.responseUsed = true
		return "", nil, nil, nil
	}
	// Validate without mutating history first.
	if _, err := s.conversation.Context(text); err != nil {
		return "", nil, nil, err
	}
	s.finalizeConversationLocked()
	if _, err := s.conversation.CommitUser(newID("item"), s.responseTurn, text); err != nil {
		return "", nil, nil, err
	}
	s.responseUsed = true
	s.notifyConversationLocked(s.conversation.User(s.responseTurn))
	req, err := s.conversation.Context("")
	if err != nil {
		return "", nil, nil, err
	}
	id, gctx, p := s.startGenerationWithTurnLocked(s.responseTurn, textOnly)
	s.generationRequest = req
	return id, gctx, p, nil
}
func (s *Session) StartConversationResponse(ctx context.Context, text string, textOnly bool) (string, context.Context, *speech.Pipeline, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginConversationResponseLocked(ctx, text, textOnly)
}

// Text input is a committed user turn, never a privileged prompt. Reject mixing
// it into an active microphone turn; the client can cancel/stop that input first.
func (s *Session) NewTextResponseContext() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inputAudioActive {
		return nil, errors.New("stop audio input before committing text")
	}
	s.invalidateSpeculationLocked("text_turn")
	return s.newInputResponseContextLocked(newID("turn")), nil
}
func (s *Session) newInputResponseContextLocked(turn string) context.Context {
	if s.responseCancel != nil {
		s.responseCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.responseCancel = cancel
	s.responseContext = ctx
	s.responseTurn = turn
	s.responseUsed = false
	return ctx
}
func (s *Session) RegisterSpeechChunk(id string, chunk speech.Chunk, sampleRate, channels int, frames int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) {
		return context.Canceled
	}
	if s.timeline == nil {
		return errors.New("text-only generation has no audio timeline")
	}
	t := s.timeline.Snapshot()
	if sampleRate <= 0 || channels <= 0 || frames <= 0 || (t.GeneratedFrames > 0 && (t.SampleRate != sampleRate || t.Channels != channels)) {
		return errors.New("invalid or changing generation audio format")
	}
	text := chunk.SourceText
	if text == "" {
		text = chunk.Text
	}
	if err := s.conversation.AddChunk(id, chunk.Sequence, text, t.GeneratedFrames, t.GeneratedFrames+frames); err != nil {
		return err
	}
	s.timeline.SetFormat(sampleRate, channels)
	s.timeline.AddGenerated(frames)
	return nil
}
func (s *Session) RecordAudioSent(id string, frames int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) || s.timeline == nil {
		return
	}
	s.timeline.AddSent(frames)
	s.reconcileAudioPlaybackLocked()
	s.syncConversationPlaybackLocked()
}
func (s *Session) RecordPlayback(id string, seconds float64, source ...*int64) error {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return errors.New("invalid playback position")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.activeConversationGenerationLocked(id) {
		return nil
	}
	if len(source) > 0 && source[0] != nil {
		return s.recordSourcePlaybackLocked(*source[0])
	}
	s.recordPlaybackLocked(seconds)
	return nil
}
func (s *Session) recordSourcePlaybackLocked(frames int64) error {
	if frames < 0 {
		return errors.New("invalid source playback position")
	}
	if s.timeline == nil {
		return nil
	}
	if s.audioFlow != nil {
		s.audioFlow.Acknowledge(frames)
	}
	snap := s.timeline.Snapshot()
	if frames > snap.SentFrames {
		frames = snap.SentFrames
	}
	s.timeline.SetPlayed(frames)
	s.syncConversationPlaybackLocked()
	return nil
}
func (s *Session) recordPlaybackLocked(seconds float64) {
	if s.timeline == nil {
		return
	}
	snap := s.timeline.Snapshot()
	if snap.SampleRate <= 0 {
		return
	}
	frames := seconds * float64(snap.SampleRate)
	if s.audioFlow != nil {
		// Retain a legitimate ACK racing the successful-write accounting; the
		// reservation ceiling also keeps a huge seconds value safe to convert.
		s.audioFlow.Acknowledge(int64(min(frames, float64(s.audioFlow.Snapshot().Reserved))))
	}
	if frames > float64(snap.SentFrames) {
		frames = float64(snap.SentFrames)
	}
	s.timeline.SetPlayed(int64(frames))
	s.syncConversationPlaybackLocked()
}
