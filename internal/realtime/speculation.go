package realtime

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt/limited"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

type SpeculationState string

const (
	SpeculationCandidate   SpeculationState = "candidate"
	SpeculationRunning     SpeculationState = "running"
	SpeculationReady       SpeculationState = "ready"
	SpeculationCommitted   SpeculationState = "committed"
	SpeculationPromoted    SpeculationState = "promoted"
	SpeculationInvalidated SpeculationState = "invalidated"
	SpeculationCancelled   SpeculationState = "cancelled"
)

type SpeculationConfig struct {
	Enabled            bool
	Timeout            time.Duration
	Cooldown           time.Duration
	MaxTranscriptBytes int
	MaxDeltaBytes      int
	MaxAttemptsPerTurn int
}

func DefaultSpeculationConfig() SpeculationConfig {
	return SpeculationConfig{Enabled: true, Timeout: 45 * time.Second, Cooldown: 2 * time.Second, MaxTranscriptBytes: 16 * 1024, MaxDeltaBytes: 64 * 1024, MaxAttemptsPerTurn: 3}
}
func (c SpeculationConfig) Validate() error {
	if c.Timeout <= 0 || c.Timeout > 2*time.Minute || c.Cooldown < 0 || c.MaxTranscriptBytes < 1 || c.MaxTranscriptBytes > 1<<20 || c.MaxDeltaBytes < 1 || c.MaxDeltaBytes > 1<<20 || c.MaxAttemptsPerTurn < 1 || c.MaxAttemptsPerTurn > 16 {
		return fmt.Errorf("invalid speculation limits")
	}
	return nil
}

// Key is a capability identifying immutable input, never a generation ID.
type SpeculationKey struct {
	ID       string `json:"speculation_id"`
	TurnID   string `json:"turn_id"`
	Revision uint64 `json:"revision"`
}
type SpeculationUpdate struct {
	SpeculationKey
	State           SpeculationState `json:"state"`
	Stage           string           `json:"stage"`
	Reason          string           `json:"reason,omitempty"`
	GenerationID    string           `json:"generation_id,omitempty"`
	DurationMS      int64            `json:"duration_ms"`
	SavedMS         int64            `json:"saved_ms,omitempty"`
	WastedMS        int64            `json:"wasted_ms,omitempty"`
	STTDurationMS   int64            `json:"stt_duration_ms,omitempty"`
	LLMFirstDeltaMS int64            `json:"llm_first_delta_ms,omitempty"`
}
type speculationRuntime struct {
	stt         *limited.Provider
	llm         llm.Provider
	config      SpeculationConfig
	current     *speculativeWork
	busy        bool // includes cancelled workers until they have actually exited
	lastAttempt time.Time
	attemptTurn string
	attempts    int
	workers     sync.WaitGroup
}

// All mutable fields are protected by Session.mu. No transport, tools, history
// or TTS capability is given to the speculative worker: it can only buffer text.
type speculativeWork struct {
	key                     SpeculationKey
	state                   SpeculationState
	ctx                     context.Context
	cancel                  context.CancelFunc
	timer                   *time.Timer
	changed                 chan struct{}
	done                    chan struct{}
	started, committedAt    time.Time
	sttDuration, firstDelta time.Duration
	committed, ready        bool
	result                  *stt.Result
	deltas                  []string
	bytes                   int
	terminal                error
	generation              string
}

func (s *Session) ConfigureSpeculation(p *limited.Provider, l llm.Provider, c SpeculationConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input == nil || s.speculation != nil || p == nil || l == nil {
		return fmt.Errorf("invalid speculation configuration")
	}
	s.speculation = &speculationRuntime{stt: p, llm: l, config: c}
	return nil
}
func (s *Session) speculationEventLocked(w *speculativeWork, event, stage, reason string) {
	u := &SpeculationUpdate{SpeculationKey: w.key, State: w.state, Stage: stage, Reason: reason,
		GenerationID: w.generation, DurationMS: time.Since(w.started).Milliseconds(),
		STTDurationMS: w.sttDuration.Milliseconds(), LLMFirstDeltaMS: w.firstDelta.Milliseconds()}
	if event == "promoted" {
		u.SavedMS = w.committedAt.Sub(w.started).Milliseconds()
	}
	if event == "invalidated" || event == "cancelled" || event == "fallback" {
		u.WastedMS = u.DurationMS
	}
	// Telemetry is best effort; losing optimization metrics must not end a session.
	// Reserve space for endpoint/interruption decisions on the shared queue.
	if len(s.input.updates) >= cap(s.input.updates)-16 {
		return
	}
	select {
	case s.input.updates <- InputUpdate{EventType: "speculation." + event, Speculation: u, Generation: w.generation}:
	default:
	}
}
func (w *speculativeWork) notifyLocked() { close(w.changed); w.changed = make(chan struct{}) }
func (s *Session) validSpeculationLocked(w *speculativeWork) bool {
	if w.state == SpeculationPromoted {
		return w.ctx.Err() == nil && s.ctx.Err() == nil && w.generation == s.generationID && s.generationCtx != nil && s.generationCtx.Err() == nil
	}
	return s.speculation.current == w && w.ctx.Err() == nil && s.ctx.Err() == nil &&
		(w.committed || (s.input.turnID == w.key.TurnID && s.input.epoch == w.key.Revision))
}
func (s *Session) endSpeculationLocked(w *speculativeWork, state SpeculationState, event, reason string) {
	if w.state == SpeculationInvalidated || w.state == SpeculationCancelled {
		return
	}
	w.state = state
	w.cancel()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.result = nil
	w.deltas = nil
	w.bytes = 0
	w.notifyLocked()
	s.speculationEventLocked(w, event, "discard", reason)
}
func (s *Session) invalidateSpeculationLocked(reason string) {
	if s.speculation == nil || s.speculation.current == nil {
		return
	}
	w := s.speculation.current
	// Promoted work belongs to the output generation; speech only pauses that
	// generation until the existing interruption policy resolves it.
	if w.state != SpeculationPromoted {
		if reason == "generation_cancelled" || reason == "input_cancelled" || reason == "input_stopped" {
			s.endSpeculationLocked(w, SpeculationCancelled, "cancelled", reason)
		} else {
			s.endSpeculationLocked(w, SpeculationInvalidated, "invalidated", reason)
		}
	}
}

// Called only by the existing endpoint loop after VAD end + minimum delay.
func (s *Session) startSpeculationLocked() {
	r := s.speculation
	if r == nil || !r.config.Enabled || s.ctx.Err() != nil || !s.input.hasSpeech || s.inputAudioBuffer.Len() == 0 {
		return
	}
	if (s.input.state != TurnWaiting && s.input.state != TurnPossibleEnd) || s.input.end.IsZero() || time.Now().Before(s.input.end.Add(s.input.config.MinDelay)) {
		return
	}
	if r.current != nil && r.current.key.TurnID == s.input.turnID && r.current.key.Revision == s.input.epoch {
		return
	}
	w := &speculativeWork{key: SpeculationKey{newID("spec"), s.input.turnID, s.input.epoch}, state: SpeculationCandidate, started: time.Now()}
	if r.attemptTurn != s.input.turnID {
		r.attemptTurn = s.input.turnID
		r.attempts = 0
	}
	if r.busy || time.Since(r.lastAttempt) < r.config.Cooldown || r.attempts >= r.config.MaxAttemptsPerTurn {
		s.speculationEventLocked(w, "fallback", "candidate", "admission_limit")
		return
	}
	// A short overlapping acknowledgement is handled by the existing policy
	// first. Sustained speech may speculate while the interruption is pending.
	if s.interruptionPendingLocked() && s.interruption.samples < 12800 {
		return
	}
	w.ctx, w.cancel = context.WithCancel(s.ctx)
	w.changed = make(chan struct{})
	w.done = make(chan struct{})
	r.current = w
	r.busy = true
	r.lastAttempt = w.started
	r.attempts++
	pcm := append([]byte(nil), s.inputAudioBuffer.Bytes()...)
	w.state = SpeculationRunning
	s.speculationEventLocked(w, "started", "stt", "endpoint_candidate")
	w.timer = time.AfterFunc(r.config.Timeout, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.current == w && w.ctx.Err() == nil && w.state != SpeculationPromoted {
			s.endSpeculationLocked(w, SpeculationCancelled, "fallback", "timeout")
		}
	})
	r.workers.Add(1)
	go s.runSpeculation(w, pcm)
}
func (s *Session) runSpeculation(w *speculativeWork, pcm []byte) {
	r := s.speculation
	defer r.workers.Done()
	defer func() { s.mu.Lock(); r.busy = false; close(w.done); w.notifyLocked(); s.mu.Unlock() }()
	result, err := r.stt.TryTranscribe(w.ctx, stt.Request{Audio: pcm, Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
	pcm = nil
	s.mu.Lock()
	if !s.validSpeculationLocked(w) {
		s.mu.Unlock()
		return
	}
	w.sttDuration = time.Since(w.started)
	if err != nil || result == nil || len(result.Text) > r.config.MaxTranscriptBytes {
		reason := "stt_failed"
		if err == limited.ErrBusy {
			reason = "global_capacity"
		}
		if result != nil && len(result.Text) > r.config.MaxTranscriptBytes {
			reason = "transcript_limit"
		}
		s.endSpeculationLocked(w, SpeculationCancelled, "fallback", reason)
		s.mu.Unlock()
		return
	}
	copyResult := *result
	w.result = &copyResult
	s.speculationEventLocked(w, "ready", "stt", "")
	if result.Text == "" {
		w.ready = true
		w.terminal = io.EOF
		if !w.committed {
			w.state = SpeculationReady
		}
		w.notifyLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	llmStart := time.Now()
	stream, err := r.llm.Generate(w.ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: result.Text}}})
	if stream != nil {
		defer stream.Close()
	}
	s.mu.Lock()
	if !s.validSpeculationLocked(w) {
		s.mu.Unlock()
		return
	}
	if err != nil || stream == nil {
		s.endSpeculationLocked(w, SpeculationCancelled, "fallback", "llm_failed")
		s.mu.Unlock()
		return
	}
	w.ready = true
	if !w.committed {
		w.state = SpeculationReady
	}
	w.notifyLocked()
	s.mu.Unlock()
	first := true
	for {
		delta, err := stream.Recv()
		s.mu.Lock()
		if !s.validSpeculationLocked(w) {
			s.mu.Unlock()
			return
		}
		if err != nil {
			if err != io.EOF && w.state != SpeculationPromoted {
				s.endSpeculationLocked(w, SpeculationCancelled, "fallback", "llm_stream_failed")
			} else {
				w.terminal = err
				w.notifyLocked()
			}
			s.mu.Unlock()
			return
		}
		if delta.Text == "" {
			s.mu.Unlock()
			continue
		}
		if first {
			first = false
			w.firstDelta = time.Since(llmStart)
			s.speculationEventLocked(w, "ready", "llm", "")
		}
		for w.bytes+len(delta.Text) > r.config.MaxDeltaBytes || len(w.deltas) >= 1024 {
			if w.state == SpeculationPromoted && len(delta.Text) > r.config.MaxDeltaBytes {
				w.terminal = fmt.Errorf("LLM delta exceeds buffer limit")
				w.notifyLocked()
				s.mu.Unlock()
				return
			}
			if w.state != SpeculationPromoted || len(delta.Text) > r.config.MaxDeltaBytes {
				s.endSpeculationLocked(w, SpeculationCancelled, "fallback", "delta_limit")
				s.mu.Unlock()
				return
			}
			changed := w.changed
			s.mu.Unlock()
			select {
			case <-w.ctx.Done():
				return
			case <-changed:
			}
			s.mu.Lock()
			if !s.validSpeculationLocked(w) {
				s.mu.Unlock()
				return
			}
		}
		w.deltas = append(w.deltas, delta.Text)
		w.bytes += len(delta.Text)
		w.notifyLocked()
		s.mu.Unlock()
	}
}

// Promotion is the TTS-ready handoff. Its pipeline and replay/live text stream
// exist only after the commit barrier; synthesis can use the existing chunker.
// Future internal PCM artifacts must carry the same Key and a separate byte cap.
type Promotion struct {
	Key          SpeculationKey
	GenerationID string
	Context      context.Context
	Pipeline     *speech.Pipeline
	Transcript   stt.Result
	Stream       llm.Stream
}

// PromoteSpeculation adopts in-flight work up to its original timeout, avoiding
// a second heavy Whisper process. Every wake rechecks identity and cancellation.
func (s *Session) PromoteSpeculation(ctx context.Context, key SpeculationKey) *Promotion {
	for {
		s.mu.Lock()
		if s.speculation == nil || ctx.Err() != nil {
			s.mu.Unlock()
			return nil
		}
		w := s.speculation.current
		if w == nil || w.key != key || !w.committed || w.state == SpeculationPromoted {
			s.mu.Unlock()
			return nil
		}
		if !s.validSpeculationLocked(w) {
			done := w.done
			s.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
			}
			return nil
		}
		// Check the clock under the promotion lock as well as using a timer:
		// scheduler delay must never let expired work cross the barrier.
		if !time.Now().Before(w.started.Add(s.speculation.config.Timeout)) {
			s.endSpeculationLocked(w, SpeculationCancelled, "fallback", "timeout")
			s.mu.Unlock()
			continue
		}
		if !w.ready {
			changed := w.changed
			s.mu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
				return nil
			}
			continue
		}
		var id string
		gctx := ctx
		var pipeline *speech.Pipeline
		if w.result.Text != "" {
			id, gctx, pipeline = s.startGenerationLocked()
		}
		w.state = SpeculationPromoted
		w.generation = id
		w.timer.Stop()
		context.AfterFunc(gctx, w.cancel)
		s.speculationEventLocked(w, "promoted", "generation", "")
		p := &Promotion{key, id, gctx, pipeline, *w.result, &speculativeStream{s, w, gctx}}
		s.mu.Unlock()
		return p
	}
}

type speculativeStream struct {
	session *Session
	work    *speculativeWork
	ctx     context.Context
}

func (r *speculativeStream) Recv() (llm.Delta, error) {
	s, w := r.session, r.work
	for {
		s.mu.Lock()
		if r.ctx.Err() != nil || !s.validSpeculationLocked(w) || s.generationID != w.generation {
			s.mu.Unlock()
			return llm.Delta{}, context.Canceled
		}
		if len(w.deltas) > 0 {
			text := w.deltas[0]
			w.deltas[0] = ""
			w.deltas = w.deltas[1:]
			w.bytes -= len(text)
			w.notifyLocked()
			s.mu.Unlock()
			return llm.Delta{Text: text}, nil
		}
		if w.terminal != nil {
			err := w.terminal
			s.mu.Unlock()
			return llm.Delta{}, err
		}
		changed := w.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-r.ctx.Done():
			return llm.Delta{}, r.ctx.Err()
		case <-w.ctx.Done():
			return llm.Delta{}, w.ctx.Err()
		}
	}
}
func (r *speculativeStream) Close() error {
	r.session.mu.Lock()
	defer r.session.mu.Unlock()
	r.work.cancel()
	r.work.deltas = nil
	r.work.bytes = 0
	r.work.result = nil
	r.work.notifyLocked()
	return nil
}
