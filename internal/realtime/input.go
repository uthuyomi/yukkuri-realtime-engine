package realtime

import (
	"bytes"
	"context"
	"fmt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"log"
	"sync"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
)

type TurnState string

const (
	TurnIdle        TurnState = "idle"
	TurnListening   TurnState = "listening"
	TurnSpeaking    TurnState = "speaking"
	TurnPossibleEnd TurnState = "possible_end"
	TurnWaiting     TurnState = "waiting"
	TurnComplete    TurnState = "complete"
)

type EndpointConfig struct {
	MinDelay        time.Duration
	MaxDelay        time.Duration
	MaxTurnDuration time.Duration
}

func DefaultEndpointConfig() EndpointConfig {
	return EndpointConfig{300 * time.Millisecond, 2500 * time.Millisecond, 120 * time.Second}
}
func (c EndpointConfig) Validate() error {
	if c.MinDelay <= 0 || c.MaxDelay < c.MinDelay || c.MaxTurnDuration < c.MaxDelay || c.MaxTurnDuration > 10*time.Minute {
		return fmt.Errorf("invalid endpoint delays or maximum turn duration")
	}
	return nil
}

// InputUpdate separates input decisions from STT, generation and transport.
// Audio ownership transfers at commit; Context cancels obsolete transcription.
type InputUpdate struct {
	EventType      string              `json:"-"`
	Generation     string              `json:"-"`
	Interruption   *InterruptionUpdate `json:"-"`
	Speculation    *SpeculationUpdate  `json:"-"`
	SpeculationKey *SpeculationKey     `json:"-"`
	State          TurnState           `json:"state"`
	TurnID         string              `json:"turn_id,omitempty"`
	Reason         string              `json:"reason,omitempty"`
	Error          string              `json:"error,omitempty"`
	Audio          []byte              `json:"-"`
	Context        context.Context     `json:"-"`
}
type inputRuntime struct {
	provider       turndetection.Provider
	config         EndpointConfig
	state          TurnState
	turnID         string
	epoch          uint64
	end            time.Time
	attempted      bool
	hasSpeech      bool
	endpointReady  bool
	endpointReason string
	predictCancel  context.CancelFunc
	wake           chan struct{}
	updates        chan InputUpdate
	done           chan struct{}
}
type prediction struct {
	epoch  uint64
	result turndetection.Result
	err    error
}

// ConfigureInput must be called once before exposing a session to a transport.
func (s *Session) ConfigureInput(p turndetection.Provider, c EndpointConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if p == nil || p.AudioWindow() <= 0 || p.AudioWindow() > 60*time.Second {
		return fmt.Errorf("invalid turn detector")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input != nil {
		return fmt.Errorf("input runtime already configured")
	}
	s.input = &inputRuntime{provider: p, config: c, state: TurnIdle, wake: make(chan struct{}, 1), updates: make(chan InputUpdate, 64), done: make(chan struct{})}
	go s.runInput()
	return nil
}
func (s *Session) InputUpdates() <-chan InputUpdate { return s.input.updates }
func (s *Session) TurnState() TurnState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input == nil {
		return TurnIdle
	}
	return s.input.state
}
func (s *Session) inputNotifyLocked(u InputUpdate) {
	select {
	case s.input.updates <- u:
	default:
		log.Printf("input event queue full: session=%s; closing session", s.id)
		s.cancel()
	} // bounded backpressure, never block barge-in
}
func (s *Session) transitionLocked(state TurnState, reason string) {
	s.input.state = state
	s.inputNotifyLocked(InputUpdate{State: state, TurnID: s.input.turnID, Reason: reason})
}
func (s *Session) wakeInputLocked() {
	select {
	case s.input.wake <- struct{}{}:
	default:
	}
}
func (s *Session) invalidateInputLocked() {
	s.invalidateSpeculationLocked("input_revision")
	s.resetEndpointLocked()
}
func (s *Session) resetEndpointLocked() {
	s.input.epoch++
	if s.input.predictCancel != nil {
		s.input.predictCancel()
		s.input.predictCancel = nil
	}
	s.input.end = time.Time{}
	s.input.attempted = false
	s.input.endpointReady = false
	s.input.endpointReason = ""
	s.wakeInputLocked()
}
func (s *Session) StartRealtimeInput(f InputAudioFormatData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if s.input == nil {
		return fmt.Errorf("turn detector is not configured")
	}
	if f.SampleRate != 16000 || f.Channels != 1 || f.Encoding != "pcm_s16le" {
		return fmt.Errorf("realtime input requires PCM16 LE 16000 Hz mono")
	}
	if s.inputAudioActive {
		return fmt.Errorf("input audio already active")
	}
	s.invalidateInputLocked()
	f.Mode = "realtime"
	s.inputAudioFormat = f
	s.inputAudioBuffer = bytes.Buffer{}
	s.inputAudioActive = true
	s.transitionLocked(TurnListening, "stream_started")
	return nil
}
func (s *Session) RealtimeInputActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputAudioActive && s.inputAudioFormat.Mode == "realtime"
}
func (s *Session) SpeechStart() error {
	return s.SpeechStartWithInterruption("", "")
}

func (s *Session) SpeechStartWithInterruption(generation, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inputAudioActive || s.inputAudioFormat.Mode != "realtime" {
		return fmt.Errorf("realtime input is not active")
	}
	if s.input.state == TurnSpeaking {
		return nil
	}
	if s.responseCancel != nil {
		s.responseCancel()
		s.responseCancel = nil
	}
	freshTurn := s.input.state == TurnListening
	if freshTurn {
		s.input.turnID = newID("turn")
		s.input.hasSpeech = false
	}
	s.invalidateInputLocked()
	s.transitionLocked(TurnSpeaking, "vad_start")
	if s.interruptionPendingLocked() {
		s.interruption.resumed = true
		if generation == s.generationID && id != "" {
			s.interruption.id = id
		}
		s.confirmInterruptionLocked("speech_resumed", "")
	} else if generation != "" && id != "" {
		return s.beginInterruptionLocked(generation, id, freshTurn)
	}
	return nil
}
func (s *Session) SpeechEnd() error {
	return s.endSpeech(true)
}

func (s *Session) VADMisfire() error { return s.endSpeech(false) }

func (s *Session) endSpeech(valid bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inputAudioActive || s.inputAudioFormat.Mode != "realtime" {
		return fmt.Errorf("realtime input is not active")
	}
	if s.input.state != TurnSpeaking {
		return nil
	} // duplicate end cannot extend the deadline
	if !valid && !s.input.hasSpeech {
		if s.interruptionPendingLocked() {
			s.recoverInterruptionLocked(backchannel.FalseInterruption, "vad_misfire")
			return nil
		}
		s.invalidateInputLocked()
		s.inputAudioBuffer = bytes.Buffer{}
		s.input.turnID = ""
		s.transitionLocked(TurnListening, "vad_misfire")
		return nil
	}
	if valid {
		s.input.hasSpeech = true
	}
	s.input.end = time.Now()
	s.interruptionChangedLocked()
	s.input.attempted = false
	s.transitionLocked(TurnPossibleEnd, "vad_end")
	s.wakeInputLocked()
	return nil
}
func (s *Session) CancelInput(stop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input == nil {
		return
	}
	if stop {
		s.invalidateSpeculationLocked("input_stopped")
	} else {
		s.invalidateSpeculationLocked("input_cancelled")
	}
	if s.interruptionPendingLocked() {
		s.recoverInterruptionLocked(backchannel.FalseInterruption, "input_cancelled")
	}
	s.invalidateInputLocked()
	if s.responseCancel != nil {
		s.responseCancel()
		s.responseCancel = nil
	}
	s.inputAudioBuffer = bytes.Buffer{}
	s.input.turnID = ""
	if stop {
		s.inputAudioActive = false
		s.transitionLocked(TurnIdle, "input_stopped")
	} else if s.inputAudioActive {
		s.transitionLocked(TurnListening, "input_cancelled")
	}
}

// AppendRealtimeAudio retains a bounded pre-roll while listening and the complete
// current turn while speaking/waiting. Silence never accumulates without bound.
func (s *Session) AppendRealtimeAudio(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inputAudioActive || s.inputAudioFormat.Mode != "realtime" {
		return fmt.Errorf("realtime input is not active")
	}
	if len(data)%2 != 0 || len(data) > 16384 {
		return fmt.Errorf("invalid PCM frame size")
	}
	if s.input.state == TurnListening {
		const preRoll = 16000 // 500 ms, covers VAD activation latency
		if len(data) >= preRoll {
			s.inputAudioBuffer.Reset()
			data = data[len(data)-preRoll:]
		} else if n := s.inputAudioBuffer.Len() + len(data) - preRoll; n > 0 {
			s.inputAudioBuffer.Next(n)
		}
	} else if s.inputAudioBuffer.Len()+len(data) > int(s.input.config.MaxTurnDuration.Seconds()*32000) {
		if s.interruptionPendingLocked() {
			s.confirmInterruptionLocked("input_limit", "")
		}
		s.invalidateInputLocked()
		s.inputAudioBuffer = bytes.Buffer{}
		s.inputAudioActive = false
		s.transitionLocked(TurnIdle, "input_limit")
		return fmt.Errorf("maximum input turn duration exceeded; restart input")
	}
	s.observeInterruptionAudioLocked(data)
	_, _ = s.inputAudioBuffer.Write(data)
	return nil
}
func (s *Session) commitTurnLocked(reason string) {
	if s.interruptionPendingLocked() {
		s.input.endpointReady = true
		s.input.endpointReason = reason
		return
	}
	var key *SpeculationKey
	if s.speculation != nil && s.speculation.current != nil {
		w := s.speculation.current
		if s.validSpeculationLocked(w) && !w.committed {
			w.committed = true
			w.committedAt = time.Now()
			w.state = SpeculationCommitted
			k := w.key
			key = &k
		}
	}
	s.resetEndpointLocked()
	if s.inputAudioBuffer.Len() == 0 {
		s.transitionLocked(TurnListening, "empty_turn")
		return
	}
	ctx := s.newInputResponseContextLocked(s.input.turnID)
	s.input.state = TurnComplete
	s.inputNotifyLocked(InputUpdate{State: TurnComplete, TurnID: s.input.turnID, Reason: reason, Audio: s.inputAudioBuffer.Bytes(), Context: ctx, SpeculationKey: key})
	// Transfer backing storage to STT, rather than copying the whole utterance.
	s.inputAudioBuffer = bytes.Buffer{}
	s.input.turnID = ""
	s.transitionLocked(TurnListening, "next_turn")
}

func (s *Session) runInput() {
	r := s.input
	defer close(r.done)
	var workers sync.WaitGroup
	defer workers.Wait()
	results := make(chan prediction, 1)
	classifications := make(chan classification, 1)
	classifierInFlight := false
	inFlight := false
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.invalidateInterruptionLocked()
			if r.predictCancel != nil {
				r.predictCancel()
			}
			s.mu.Unlock()
			return
		}
		delay := time.Hour
		job, interruptionDelay := s.prepareInterruptionLocked(time.Now(), !classifierInFlight)
		if job != nil {
			classifierInFlight = true
			workers.Add(1)
			go func(j *classificationJob) {
				defer workers.Done()
				defer j.cancel()
				result, err := j.provider.Classify(j.ctx, j.observation)
				classifications <- classification{j.revision, j.id, result, err}
			}(job)
		}
		if !r.end.IsZero() && !r.endpointReady {
			maxAt := r.end.Add(r.config.MaxDelay)
			minAt := r.end.Add(r.config.MinDelay)
			if !time.Now().Before(maxAt) {
				s.commitTurnLocked("max_delay")
			} else {
				delay = time.Until(maxAt)
				if !r.attempted && !inFlight {
					if time.Now().Before(minAt) {
						delay = time.Until(minAt)
					} else {
						r.attempted = true
						s.transitionLocked(TurnWaiting, "detector_pending")
						s.startSpeculationLocked()
						pcm := s.inputAudioBuffer.Bytes()
						limit := int(r.provider.AudioWindow().Seconds() * 32000)
						if len(pcm) > limit {
							pcm = pcm[len(pcm)-limit:]
						}
						snapshot := append([]byte(nil), pcm...)
						ctx, cancel := context.WithDeadline(s.ctx, maxAt)
						r.predictCancel = cancel
						epoch := r.epoch
						inFlight = true
						workers.Add(1)
						go func() {
							defer workers.Done()
							defer cancel()
							result, err := r.provider.Detect(ctx, turndetection.Request{Audio: snapshot})
							results <- prediction{epoch, result, err}
						}()
					}
				}
			}
		}
		if interruptionDelay < delay {
			delay = interruptionDelay
		}
		s.mu.Unlock()
		timer.Reset(delay)
		select {
		case <-s.ctx.Done():
		case <-r.wake:
		case <-timer.C:
		case c := <-classifications:
			classifierInFlight = false
			s.mu.Lock()
			s.applyClassificationLocked(c)
			s.mu.Unlock()
		case p := <-results:
			inFlight = false
			s.mu.Lock()
			if p.epoch == r.epoch && !r.end.IsZero() && s.ctx.Err() == nil {
				if s.interruptionPendingLocked() && p.err == nil {
					prob := p.result.Probability
					s.interruption.endResult = &backchannel.Observation{EndComplete: p.result.Complete, EndProbability: &prob}
					s.interruptionChangedLocked()
				}
				if p.err != nil {
					s.invalidateSpeculationLocked("detector_failed")
					s.inputNotifyLocked(InputUpdate{State: r.state, TurnID: r.turnID, Error: p.err.Error(), Reason: "detector_failed"})
				} else if p.result.Complete {
					s.commitTurnLocked("detector_complete")
				} else {
					s.invalidateSpeculationLocked("continuation_likely")
					s.transitionLocked(TurnWaiting, "continuation_likely")
				}
			}
			s.mu.Unlock()
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}
