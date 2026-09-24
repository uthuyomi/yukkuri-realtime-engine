package realtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/conversation"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

type Session struct {
	id string

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	closeOnce sync.Once
	closeDone chan struct{}

	generationID        string
	generationCtx       context.Context
	generationCancel    context.CancelFunc
	pipeline            *speech.Pipeline
	timeline            *audio.PlaybackTimeline
	audioFlow           *audio.FlowController
	audioCreditRequired bool

	inputAudioFormat    InputAudioFormatData
	inputAudioBuffer    bytes.Buffer
	inputAudioActive    bool
	input               *inputRuntime
	responseCancel      context.CancelFunc
	interruption        *interruptionRuntime
	generationDone      bool
	speculation         *speculationRuntime
	conversation        *conversation.Runtime
	conversationUpdates chan ConversationUpdate
	responseContext     context.Context
	responseTurn        string
	responseUsed        bool
	generationTextOnly  bool
	generationRequest   llm.Request
}

func NewSession(parent context.Context) *Session {
	s, err := NewSessionWithConversation(parent, conversation.DefaultConfig())
	if err != nil {
		panic(err)
	}
	return s
}
func NewSessionWithConversation(parent context.Context, c conversation.Config) (*Session, error) {
	r, err := conversation.New(newID("conv"), newID("item"), c)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)

	return &Session{
		id:           newID("sess"),
		closeDone:    make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		conversation: r, conversationUpdates: make(chan ConversationUpdate, 64),
	}, nil
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) StartGeneration() (
	string,
	context.Context,
	*speech.Pipeline,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateSpeculationLocked("generation_replaced")
	if s.responseCancel != nil {
		s.responseCancel()
		s.responseCancel = nil
	}
	return s.startGenerationLocked()
}

// StartGenerationForInput atomically rejects transcripts superseded by speech,
// input cancellation or generation cancellation.
func (s *Session) StartGenerationForInput(ctx context.Context) (string, context.Context, *speech.Pipeline) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.ctx.Err() != nil {
		return "", nil, nil
	}
	return s.startGenerationLocked()
}

func (s *Session) startGenerationLocked() (string, context.Context, *speech.Pipeline) {
	return s.startGenerationWithTurnLocked(newID("turn"), false)
}
func (s *Session) startGenerationWithTurnLocked(turn string, textOnly bool) (string, context.Context, *speech.Pipeline) {
	if s.ctx.Err() != nil {
		return "", nil, nil
	}
	s.finalizeConversationLocked()
	s.invalidateInterruptionLocked()
	s.generationDone = false

	if s.generationCancel != nil {
		s.generationCancel()
	}

	if s.pipeline != nil {
		s.pipeline.Cancel()
	}

	generationID := newID("gen")

	ctx, cancel :=
		context.WithCancel(s.ctx)

	pipeline := speech.NewPipeline(
		ctx,
		speech.ChunkerConfig{
			SoftLimit: 30,
			HardLimit: 60,
		},
	)

	timeline :=
		audio.NewPlaybackTimeline(
			generationID,
		)

	s.generationID = generationID
	s.generationCtx = ctx
	s.generationCancel = cancel
	s.pipeline = pipeline
	s.timeline = timeline
	s.audioFlow = nil
	s.generationTextOnly = textOnly
	s.generationRequest = llm.Request{}
	if textOnly {
		s.timeline = nil
	}
	s.conversation.BeginAssistant(newID("item"), turn, generationID, textOnly)
	s.notifyConversationLocked(s.conversation.Assistant(generationID))

	return generationID, ctx, pipeline
}

func (s *Session) CancelGeneration() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateSpeculationLocked("generation_cancelled")
	return s.cancelGenerationLocked()
}

func (s *Session) CancelGenerationID(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" && id != s.generationID {
		return ""
	}
	s.invalidateSpeculationLocked("generation_cancelled")
	return s.cancelGenerationLocked()
}

func (s *Session) cancelGenerationLocked() string {
	s.finalizeConversationLocked()
	s.invalidateInterruptionLocked()
	if s.timeline != nil {
		s.timeline.SetPaused(false)
	}

	id := s.generationID
	if s.responseCancel != nil {
		s.responseCancel()
		s.responseCancel = nil
	}

	if s.generationCancel != nil {
		s.generationCancel()
	}

	if s.pipeline != nil {
		s.pipeline.Cancel()
	}

	s.generationID = ""
	s.generationCtx = nil
	s.generationCancel = nil
	s.pipeline = nil

	// timeline intentionally remains available.
	//
	// Cancellation ends generation, but the final
	// playback state is still useful for logging,
	// interruption handling and observability.

	return id
}

func (s *Session) MarkGenerationDone(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.generationID || s.generationCtx == nil || s.generationCtx.Err() != nil || s.generationDone {
		return false
	}
	s.generationDone = true
	p := s.conversation.Assistant(id)
	before := p.Status
	s.conversation.Done(id, true)
	s.syncConversationPlaybackLocked()
	if p.Status != before {
		s.notifyConversationLocked(p)
	}
	return true // context stays alive while sent audio is still queued
}

func (s *Session) TimelineForGeneration(id string) *audio.PlaybackTimeline {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.generationID {
		return nil
	}
	return s.timeline
}

func (s *Session) CurrentGeneration() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.generationID
}

func (s *Session) Pipeline() *speech.Pipeline {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.pipeline
}

func (s *Session) Timeline() *audio.PlaybackTimeline {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.timeline
}

func (s *Session) StartInputAudio(
	format InputAudioFormatData,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inputAudioBuffer.Reset()
	s.inputAudioFormat = format
	s.inputAudioActive = true
}

func (s *Session) AppendInputAudio(
	data []byte,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.inputAudioActive {
		return false
	}

	return audio.AppendPCM16(&s.inputAudioBuffer, data, protocol.MaxInputBytes) == nil
}

func (s *Session) CommitInputAudio() (
	[]byte,
	InputAudioFormatData,
	bool,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.inputAudioActive {
		return nil, InputAudioFormatData{}, false
	}

	audio := s.inputAudioBuffer.Bytes()

	format := s.inputAudioFormat

	s.inputAudioBuffer = bytes.Buffer{}
	s.inputAudioFormat = InputAudioFormatData{}
	s.inputAudioActive = false

	return audio, format, true
}

func (s *Session) NewInputResponseContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newInputResponseContextLocked(newID("turn"))
}

func (s *Session) closeRuntime() {
	s.cancel()
	s.CancelGeneration()
	if s.input != nil {
		<-s.input.done
	}
	if s.speculation != nil {
		s.speculation.workers.Wait()
	}
}

func newID(prefix string) string {
	var buffer [12]byte

	if _, err := rand.Read(buffer[:]); err != nil {
		panic(fmt.Sprintf(
			"failed to generate realtime ID: %v",
			err,
		))
	}

	return prefix +
		"_" +
		hex.EncodeToString(buffer[:])
}

// CloseWithin bounds public shutdown even if an external provider violates its
// context contract; cancellation happens before waiting for runtime workers.
func (s *Session) startClose() <-chan struct{} {
	s.cancel()
	s.closeOnce.Do(func() { go func() { defer close(s.closeDone); s.closeRuntime() }() })
	return s.closeDone
}
func (s *Session) Close() { <-s.startClose() }
func (s *Session) CloseWithin(ctx context.Context) bool {
	select {
	case <-s.startClose():
		return true
	case <-ctx.Done():
		return false
	}
}
func (s *Session) InputAudioBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputAudioBuffer.Len()
}
