package realtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

type Session struct {
	id string

	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex

	generationID     string
	generationCtx    context.Context
	generationCancel context.CancelFunc
	pipeline         *speech.Pipeline
	timeline         *audio.PlaybackTimeline

	inputAudioFormat InputAudioFormatData
	inputAudioBuffer bytes.Buffer
	inputAudioActive bool
	input            *inputRuntime
	responseCancel   context.CancelFunc
}

func NewSession(parent context.Context) *Session {
	ctx, cancel := context.WithCancel(parent)

	return &Session{
		id:     newID("sess"),
		ctx:    ctx,
		cancel: cancel,
	}
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

	return generationID, ctx, pipeline
}

func (s *Session) CancelGeneration() string {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	_, _ = s.inputAudioBuffer.Write(data)

	return true
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
	if s.responseCancel != nil {
		s.responseCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.responseCancel = cancel
	return ctx
}

func (s *Session) Close() {
	s.cancel()
	s.CancelGeneration()
	if s.input != nil {
		<-s.input.done
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
