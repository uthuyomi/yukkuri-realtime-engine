package realtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
)

type InterruptionState string

const (
	InterruptionIdle      InterruptionState = "idle"
	InterruptionPaused    InterruptionState = "paused"
	InterruptionConfirmed InterruptionState = "confirmed"
	InterruptionRecovered InterruptionState = "recovered"
)

type InterruptionConfig struct{ DecisionWindow time.Duration }

func DefaultInterruptionConfig() InterruptionConfig {
	return InterruptionConfig{1500 * time.Millisecond}
}

type InterruptionUpdate struct {
	InterruptionID string                 `json:"interruption_id"`
	TurnID         string                 `json:"turn_id,omitempty"`
	State          InterruptionState      `json:"state"`
	Decision       backchannel.Kind       `json:"decision,omitempty"`
	Reason         string                 `json:"reason"`
	Error          string                 `json:"error,omitempty"`
	Playback       audio.PlaybackSnapshot `json:"playback"`
}
type interruptionRuntime struct {
	provider             backchannel.Provider
	config               InterruptionConfig
	state                InterruptionState
	id, generation, turn string
	started              time.Time
	revision             uint64
	attempted            uint64
	notBefore            time.Time
	cancel               context.CancelFunc
	resumed              bool
	samples, clipped     int
	energy               float64
	endResult            *backchannel.Observation
	semantic             *backchannel.SemanticEvidence
}
type classificationJob struct {
	ctx         context.Context
	cancel      context.CancelFunc
	provider    backchannel.Provider
	observation backchannel.Observation
	revision    uint64
	id          string
}
type classification struct {
	revision uint64
	id       string
	result   backchannel.Result
	err      error
}

func (s *Session) ConfigureInterruption(p backchannel.Provider, c InterruptionConfig) error {
	if p == nil || c.DecisionWindow < 300*time.Millisecond || c.DecisionWindow > 5*time.Second {
		return fmt.Errorf("invalid interruption configuration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruption != nil {
		return fmt.Errorf("interruption runtime already configured")
	}
	s.interruption = &interruptionRuntime{provider: p, config: c, state: InterruptionIdle}
	return nil
}
func (s *Session) InterruptionState() InterruptionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruption == nil {
		return InterruptionIdle
	}
	return s.interruption.state
}
func (s *Session) interruptionPendingLocked() bool {
	return s.interruption != nil && s.interruption.state == InterruptionPaused
}
func (s *Session) invalidateInterruptionLocked() {
	if s.interruption == nil {
		return
	}
	i := s.interruption
	if i.cancel != nil {
		i.cancel()
		i.cancel = nil
	}
	i.revision++
	i.state = InterruptionIdle
	if s.input != nil {
		s.wakeInputLocked()
	}
}
func (s *Session) outputRecoverableLocked() bool {
	if s.generationID == "" || s.generationCtx == nil || s.generationCtx.Err() != nil {
		return false
	}
	return !s.generationDone || (s.timeline != nil && s.timeline.Snapshot().BufferedFrames() > 0)
}
func (s *Session) interruptionEventLocked(kind string, decision backchannel.Kind, reason, errText string) {
	i := s.interruption
	u := InterruptionUpdate{InterruptionID: i.id, TurnID: i.turn, State: i.state, Decision: decision, Reason: reason, Error: errText}
	if s.timeline != nil && s.timeline.Snapshot().GenerationID == i.generation {
		u.Playback = s.timeline.Snapshot()
	}
	s.inputNotifyLocked(InputUpdate{EventType: kind, Generation: i.generation, Interruption: &u})
}

// BeginInterruption also supports a new output generation arriving during an
// already active speech segment. IDs are client correlation tokens, not decisions.
func (s *Session) BeginInterruption(generation, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginInterruptionLocked(generation, id, false)
}
func (s *Session) beginInterruptionLocked(generation, id string, freshTurn bool) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if s.interruption == nil || s.input == nil || !s.inputAudioActive || s.inputAudioFormat.Mode != "realtime" {
		return fmt.Errorf("interruption runtime requires realtime input")
	}
	if generation == "" || id == "" || len(id) > 128 {
		return fmt.Errorf("generation and bounded interruption ID required")
	}
	if generation != s.generationID {
		return nil
	} // stale generation can never affect current output
	if s.input.state != TurnSpeaking && s.input.state != TurnPossibleEnd && s.input.state != TurnWaiting {
		return nil
	}
	i := s.interruption
	if s.interruptionPendingLocked() && i.generation == generation && i.id == id {
		return nil
	}
	// New speech in the same pending window is evidence of a real turn. Do not
	// reset the original deadline, even if the browser uses a new pause token.
	if s.interruptionPendingLocked() && i.generation == generation {
		i.id = id
		i.resumed = true
		s.confirmInterruptionLocked("speech_resumed", "")
		return nil
	}
	s.invalidateInterruptionLocked()
	i.id = id
	i.generation = generation
	i.turn = s.input.turnID
	i.started = time.Now()
	i.samples = 0
	i.clipped = 0
	i.energy = 0
	i.resumed = !freshTurn
	i.endResult = nil
	i.semantic = nil
	i.attempted = 0
	i.notBefore = i.started.Add(800 * time.Millisecond)
	if i.resumed {
		i.notBefore = i.started
	}
	i.state = InterruptionPaused
	if !s.outputRecoverableLocked() {
		i.state = InterruptionRecovered
		s.interruptionEventLocked("interruption.recovered", backchannel.Ambiguous, "output_drained", "")
		return nil // the input remains a normal user turn
	}
	if s.timeline != nil {
		s.timeline.SetPaused(true)
	}
	s.interruptionEventLocked("interruption.suspected", backchannel.Ambiguous, "speech_start", "")
	s.wakeInputLocked()
	return nil
}
func (s *Session) observeInterruptionAudioLocked(pcm []byte) {
	if !s.interruptionPendingLocked() || s.input.state != TurnSpeaking {
		return
	}
	i := s.interruption
	for n := 0; n+1 < len(pcm); n += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[n:]))) / 32768
		i.energy += v * v
		i.samples++
		if math.Abs(v) >= 0.98 {
			i.clipped++
		}
	}
}
func (s *Session) interruptionChangedLocked() {
	if !s.interruptionPendingLocked() {
		return
	}
	i := s.interruption
	i.revision++
	i.notBefore = s.input.end.Add(250 * time.Millisecond)
	if i.cancel != nil {
		i.cancel()
		i.cancel = nil
	}
	s.wakeInputLocked()
}
func (s *Session) interruptionObservationLocked(now time.Time) backchannel.Observation {
	i := s.interruption
	o := backchannel.Observation{OutputActive: s.outputRecoverableLocked(), Speaking: s.input.state == TurnSpeaking, Resumed: i.resumed, Samples: i.samples, SpeechDuration: time.Duration(i.samples) * time.Second / 16000, Semantic: i.semantic}
	if i.samples > 0 {
		o.RMS = math.Sqrt(i.energy / float64(i.samples))
		o.ClippedFraction = float64(i.clipped) / float64(i.samples)
	}
	if !s.input.end.IsZero() {
		o.SilenceDuration = now.Sub(s.input.end)
	}
	if s.timeline != nil {
		o.BufferedAudio = s.timeline.Snapshot().BufferedDuration()
	}
	if i.endResult != nil {
		o.EndComplete = i.endResult.EndComplete
		o.EndProbability = i.endResult.EndProbability
	}
	return o
}

// Called by the existing input timer loop. No inference occurs under s.mu.
func (s *Session) prepareInterruptionLocked(now time.Time, canStart bool) (*classificationJob, time.Duration) {
	if !s.interruptionPendingLocked() {
		return nil, time.Hour
	}
	i := s.interruption
	deadline := i.started.Add(i.config.DecisionWindow)
	if !now.Before(deadline) {
		s.confirmInterruptionLocked("decision_timeout", "")
		return nil, time.Hour
	}
	delay := deadline.Sub(now)
	if !canStart || i.attempted == i.revision {
		return nil, delay
	}
	if now.Before(i.notBefore) {
		if d := i.notBefore.Sub(now); d < delay {
			delay = d
		}
		return nil, delay
	}
	i.attempted = i.revision
	ctx, cancel := context.WithDeadline(s.ctx, deadline)
	i.cancel = cancel
	return &classificationJob{ctx, cancel, i.provider, s.interruptionObservationLocked(now), i.revision, i.id}, delay
}
func (s *Session) applyClassificationLocked(c classification) {
	if !s.interruptionPendingLocked() || s.ctx.Err() != nil {
		return
	}
	i := s.interruption
	if c.id != i.id || c.revision != i.revision || i.generation != s.generationID {
		return
	}
	if !time.Now().Before(i.started.Add(i.config.DecisionWindow)) {
		s.confirmInterruptionLocked("decision_timeout", "")
		return
	}
	if c.err != nil {
		s.confirmInterruptionLocked("classifier_failed", c.err.Error())
		return
	}
	switch c.result.Kind {
	case backchannel.Backchannel, backchannel.FalseInterruption:
		if s.input.state == TurnSpeaking || i.resumed {
			s.confirmInterruptionLocked("speech_resumed", "")
			return
		}
		s.recoverInterruptionLocked(c.result.Kind, c.result.Reason)
	case backchannel.Interruption:
		s.confirmInterruptionLocked(c.result.Reason, "")
	case backchannel.Ambiguous: // retain original deadline
	default:
		s.confirmInterruptionLocked("classifier_failed", "invalid classifier decision")
	}
}
func (s *Session) confirmInterruptionLocked(reason, errText string) {
	if !s.interruptionPendingLocked() {
		return
	}
	i := s.interruption
	if i.generation != s.generationID {
		s.invalidateInterruptionLocked()
		return
	}
	generation := i.generation
	s.cancelGenerationLocked() // leaves current input intact
	i.state = InterruptionConfirmed
	s.interruptionEventLocked("interruption.confirmed", backchannel.Interruption, reason, errText)
	s.inputNotifyLocked(InputUpdate{EventType: "generation.cancelled", Generation: generation})
	if s.input.endpointReady {
		s.commitTurnLocked(s.input.endpointReason)
	}
}
func (s *Session) recoverInterruptionLocked(kind backchannel.Kind, reason string) {
	if !s.interruptionPendingLocked() {
		return
	}
	if s.generationID != s.interruption.generation || s.generationCtx == nil || s.generationCtx.Err() != nil {
		s.invalidateInterruptionLocked()
		return
	}
	i := s.interruption
	if i.cancel != nil {
		i.cancel()
		i.cancel = nil
	}
	i.revision++
	i.state = InterruptionRecovered
	if s.timeline != nil {
		s.timeline.SetPaused(false)
	}
	s.interruptionEventLocked("interruption.recovered", kind, reason, "")
	if kind == backchannel.Backchannel {
		s.interruptionEventLocked("input_audio.backchannel", kind, reason, "")
	}
	s.invalidateInputLocked()
	s.inputAudioBuffer = bytes.Buffer{}
	s.input.turnID = ""
	s.input.hasSpeech = false
	s.transitionLocked(TurnListening, "interruption_recovered")
}

// Playback acknowledgements use the rendered position (not receipt time) and
// cannot move a different generation's timeline or resolve a different pause.
func (s *Session) PlaybackPaused(generation, id string, seconds float64) error {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return fmt.Errorf("invalid playback position")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.interruptionPendingLocked() || generation != s.generationID || id != s.interruption.id {
		return nil
	}
	if s.timeline != nil {
		snap := s.timeline.Snapshot()
		s.timeline.SetPlayed(int64(seconds * float64(snap.SampleRate)))
		s.timeline.SetPaused(true)
	}
	return nil
}
func (s *Session) PlaybackFailed(generation, id, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation == "" || generation != s.generationID {
		return
	}
	if id != "" && (s.interruption == nil || s.interruption.id != id || s.interruption.generation != generation) {
		return
	}
	if s.interruptionPendingLocked() {
		s.confirmInterruptionLocked(reason, "")
		return
	}
	s.cancelGenerationLocked()
	if s.input != nil {
		s.inputNotifyLocked(InputUpdate{EventType: "generation.cancelled", Generation: generation})
	}
}

// Future streaming STT can supply evidence without adding a second Whisper
// process. Final decisions are never retroactively changed by late transcripts.
func (s *Session) SupplyInterruptionSemantic(generation, turn, id string, e backchannel.SemanticEvidence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.interruptionPendingLocked() || generation != s.generationID || turn != s.interruption.turn || id != s.interruption.id {
		return
	}
	copyEvidence := e
	if e.Confidence != nil {
		v := *e.Confidence
		copyEvidence.Confidence = &v
	}
	s.interruption.semantic = &copyEvidence
	s.interruptionChangedLocked()
}
