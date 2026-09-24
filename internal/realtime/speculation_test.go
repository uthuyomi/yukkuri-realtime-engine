package realtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt/limited"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
)

type speculativeSTTFunc func(context.Context, stt.Request) (*stt.Result, error)

func (speculativeSTTFunc) Name() string { return "test" }
func (f speculativeSTTFunc) Transcribe(c context.Context, r stt.Request) (*stt.Result, error) {
	return f(c, r)
}

type speculativeLLMFunc func(context.Context, llm.Request) (llm.Stream, error)

func (speculativeLLMFunc) Name() string { return "test" }
func (f speculativeLLMFunc) Generate(c context.Context, r llm.Request) (llm.Stream, error) {
	return f(c, r)
}

type sliceStream struct{ texts []string }

func (r *sliceStream) Recv() (llm.Delta, error) {
	if len(r.texts) == 0 {
		return llm.Delta{}, io.EOF
	}
	text := r.texts[0]
	r.texts = r.texts[1:]
	return llm.Delta{Text: text}, nil
}
func (*sliceStream) Close() error { return nil }
func goodSpecLLM() speculativeLLMFunc {
	return func(context.Context, llm.Request) (llm.Stream, error) {
		return &sliceStream{texts: []string{"buffered answer"}}, nil
	}
}
func goodSpecSTT() speculativeSTTFunc {
	return func(context.Context, stt.Request) (*stt.Result, error) {
		return &stt.Result{Text: "real transcript"}, nil
	}
}
func specSession(t *testing.T, p speculativeSTTFunc, l speculativeLLMFunc, c SpeculationConfig) (*Session, chan struct{}) {
	t.Helper()
	gate := make(chan struct{})
	s := NewSession(context.Background())
	err := s.ConfigureInput(detectorFunc(func(ctx context.Context, _ turndetection.Request) (turndetection.Result, error) {
		select {
		case <-gate:
			return turndetection.Result{Complete: true}, nil
		case <-ctx.Done():
			return turndetection.Result{}, ctx.Err()
		}
	}), EndpointConfig{10 * time.Millisecond, 3 * time.Second, 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureSpeculation(limited.New(p, 1), l, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err = s.StartRealtimeInput(InputAudioFormatData{Mode: "realtime", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}); err != nil {
		t.Fatal(err)
	}
	return s, gate
}
func specConfig() SpeculationConfig { c := DefaultSpeculationConfig(); c.Cooldown = 0; return c }
func specEvent(t *testing.T, s *Session, event, stage string) *SpeculationUpdate {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case u := <-s.InputUpdates():
			if u.Speculation != nil && u.EventType == "speculation."+event && (stage == "" || stage == u.Speculation.Stage) {
				return u.Speculation
			}
		case <-timer.C:
			t.Fatalf("missing speculation %s/%s", event, stage)
			return nil
		}
	}
}
func specWork(s *Session) *speculativeWork {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speculation.current
}
func waitSpec(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}
}

func TestSpeculationCandidateReuseBarrierAndExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	s, gate := specSession(t, func(c context.Context, r stt.Request) (*stt.Result, error) {
		calls.Add(1)
		if len(r.Audio) != 1024 {
			t.Errorf("snapshot=%d", len(r.Audio))
		}
		return goodSpecSTT()(c, r)
	}, goodSpecLLM(), specConfig())
	say(t, s)
	ev := specEvent(t, s, "ready", "llm")
	w := specWork(s)
	waitSpec(t, w.done)
	if s.CurrentGeneration() != "" || s.Pipeline() != nil {
		t.Fatal("output created before commit")
	}
	if s.PromoteSpeculation(s.Context(), ev.SpeculationKey) != nil {
		t.Fatal("precommit promotion")
	}
	// Silence appended after the canonical endpoint snapshot does not revise speech.
	if err := s.AppendRealtimeAudio(make([]byte, 320)); err != nil {
		t.Fatal(err)
	}
	_ = s.SpeechEnd()
	s.mu.Lock()
	s.startSpeculationLocked()
	s.mu.Unlock()
	close(gate)
	u := nextCommit(t, s)
	if u.SpeculationKey == nil {
		t.Fatal("valid work not adopted")
	}
	p := s.PromoteSpeculation(u.Context, *u.SpeculationKey)
	if p == nil || p.Transcript.Text != "real transcript" {
		t.Fatal("missing reuse")
	}
	defer p.Stream.Close()
	d, err := p.Stream.Recv()
	if err != nil || d.Text != "buffered answer" {
		t.Fatal(d, err)
	}
	if _, err = p.Stream.Recv(); err != io.EOF {
		t.Fatal(err)
	}
	if s.PromoteSpeculation(u.Context, *u.SpeculationKey) != nil || calls.Load() != 1 {
		t.Fatal("duplicate promotion/work")
	}
}

func TestSpeculationRunningCommitAdoptsSameWork(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	s, gate := specSession(t, func(ctx context.Context, _ stt.Request) (*stt.Result, error) {
		calls.Add(1)
		select {
		case <-release:
			return &stt.Result{Text: "ready later"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, goodSpecLLM(), specConfig())
	say(t, s)
	specEvent(t, s, "started", "")
	close(gate)
	u := nextCommit(t, s)
	result := make(chan *Promotion, 1)
	go func() { result <- s.PromoteSpeculation(u.Context, *u.SpeculationKey) }()
	select {
	case <-result:
		t.Fatal("promoted without transcript")
	default:
	}
	close(release)
	select {
	case p := <-result:
		if p == nil {
			t.Fatal("did not adopt")
		}
		p.Stream.Close()
	case <-time.After(time.Second):
		t.Fatal("promotion blocked")
	}
	if calls.Load() != 1 {
		t.Fatal("restarted STT")
	}
}

func TestSpeculationResumeRejectsLateResultAndRevision(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var calls atomic.Int32
	s, gate := specSession(t, func(context.Context, stt.Request) (*stt.Result, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return &stt.Result{Text: "late"}, nil
	}, goodSpecLLM(), specConfig())
	say(t, s)
	waitSpec(t, entered)
	old := specWork(s)
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	if old.ctx.Err() == nil {
		t.Fatal("resume did not cancel")
	}
	close(release)
	waitSpec(t, old.done)
	if s.PromoteSpeculation(s.Context(), old.key) != nil {
		t.Fatal("stale result promoted")
	}
	if err := s.AppendRealtimeAudio(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	_ = s.SpeechEnd()
	ev := specEvent(t, s, "ready", "llm")
	if ev.Revision == old.key.Revision || ev.TurnID != old.key.TurnID {
		t.Fatal("revision/turn identity")
	}
	close(gate)
	u := nextCommit(t, s)
	if len(u.Audio) != 2048 || u.SpeculationKey == nil || u.SpeculationKey.ID == old.key.ID {
		t.Fatal("input lost or stale snapshot")
	}
	if p := s.PromoteSpeculation(u.Context, *u.SpeculationKey); p == nil {
		t.Fatal("new revision rejected")
	} else {
		p.Stream.Close()
	}
}

func TestSpeculationCancellation(t *testing.T) {
	for _, action := range []string{"cancel", "stop", "disconnect", "generation", "replace", "misfire"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{})
			s, _ := specSession(t, func(ctx context.Context, _ stt.Request) (*stt.Result, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}, goodSpecLLM(), specConfig())
			say(t, s)
			waitSpec(t, entered)
			w := specWork(s)
			switch action {
			case "cancel":
				s.CancelInput(false)
			case "stop":
				s.CancelInput(true)
			case "disconnect":
				s.Close()
			case "generation":
				s.CancelGeneration()
			case "replace":
				s.StartGeneration()
			case "misfire":
				_ = s.SpeechStart()
				_ = s.VADMisfire()
			}
			waitSpec(t, w.done)
			if w.ctx.Err() == nil || s.PromoteSpeculation(s.Context(), w.key) != nil {
				t.Fatal("cancelled result reused")
			}
		})
	}
}

func TestSpeculationFailureLimitsAndTimeout(t *testing.T) {
	for _, kind := range []string{"stt", "llm", "transcript", "delta", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			c := specConfig()
			p := goodSpecSTT()
			l := goodSpecLLM()
			reason := ""
			switch kind {
			case "stt":
				p = func(context.Context, stt.Request) (*stt.Result, error) { return nil, errors.New("failed") }
				reason = "stt_failed"
			case "llm":
				l = func(context.Context, llm.Request) (llm.Stream, error) { return nil, errors.New("failed") }
				reason = "llm_failed"
			case "transcript":
				c.MaxTranscriptBytes = 2
				reason = "transcript_limit"
			case "delta":
				c.MaxDeltaBytes = 2
				reason = "delta_limit"
			case "timeout":
				c.Timeout = 25 * time.Millisecond
				p = func(ctx context.Context, _ stt.Request) (*stt.Result, error) { <-ctx.Done(); return nil, ctx.Err() }
				reason = "timeout"
			}
			s, gate := specSession(t, p, l, c)
			say(t, s)
			e := specEvent(t, s, "fallback", "")
			if e.Reason != reason {
				t.Fatal(e.Reason)
			}
			waitSpec(t, specWork(s).done)
			close(gate)
			u := nextCommit(t, s)
			if u.SpeculationKey != nil || s.Context().Err() != nil || len(u.Audio) == 0 {
				t.Fatal("failure broke normal input")
			}
		})
	}
}

func TestSpeculationInterruptionBarrier(t *testing.T) {
	for _, kind := range []backchannel.Kind{backchannel.Backchannel, backchannel.FalseInterruption, backchannel.Interruption} {
		t.Run(string(kind), func(t *testing.T) {
			s, gate := specSession(t, goodSpecSTT(), goodSpecLLM(), specConfig())
			if err := s.ConfigureInterruption(classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
				return backchannel.Result{Kind: backchannel.Ambiguous}, nil
			}), InterruptionConfig{time.Second}); err != nil {
				t.Fatal(err)
			}
			id, oldctx, _ := s.StartGeneration()
			startSuspect(t, s, id)
			// Sustained input permits speculation while classification remains pending.
			_ = s.AppendRealtimeAudio(voice(8000))
			_ = s.AppendRealtimeAudio(voice(6000))
			_ = s.SpeechEnd()
			ev := specEvent(t, s, "ready", "llm")
			close(gate)
			// Force the endpoint signal through the same locked barrier to avoid relying
			// on classifier scheduling for this identity test.
			s.mu.Lock()
			s.commitTurnLocked("detector_complete")
			s.mu.Unlock()
			if s.PromoteSpeculation(s.Context(), ev.SpeculationKey) != nil {
				t.Fatal("pending interruption promoted")
			}
			s.mu.Lock()
			if kind == backchannel.Interruption {
				s.confirmInterruptionLocked("test", "")
			} else {
				s.recoverInterruptionLocked(kind, "test")
			}
			s.mu.Unlock()
			if kind == backchannel.Interruption {
				u := nextCommit(t, s)
				if oldctx.Err() == nil || u.SpeculationKey == nil {
					t.Fatal("true interruption lost work")
				}
				p := s.PromoteSpeculation(u.Context, *u.SpeculationKey)
				if p == nil {
					t.Fatal("true interruption not promoted")
				}
				p.Stream.Close()
			} else {
				if oldctx.Err() != nil || s.PromoteSpeculation(s.Context(), ev.SpeculationKey) != nil {
					t.Fatal("recovery promoted or cancelled output")
				}
			}
		})
	}
}

type liveSpecStream struct {
	ctx    context.Context
	deltas chan string
}

func (r *liveSpecStream) Recv() (llm.Delta, error) {
	select {
	case text, ok := <-r.deltas:
		if !ok {
			return llm.Delta{}, io.EOF
		}
		return llm.Delta{Text: text}, nil
	case <-r.ctx.Done():
		return llm.Delta{}, r.ctx.Err()
	}
}
func (*liveSpecStream) Close() error { return nil }
func TestSpeculationLiveStreamGenerationCancellationAndConcurrency(t *testing.T) {
	deltas := make(chan string, 2)
	deltas <- "first"
	var calls atomic.Int32
	s, gate := specSession(t, goodSpecSTT(), func(ctx context.Context, _ llm.Request) (llm.Stream, error) {
		calls.Add(1)
		return &liveSpecStream{ctx, deltas}, nil
	}, specConfig())
	say(t, s)
	specEvent(t, s, "ready", "llm")
	close(gate)
	u := nextCommit(t, s)
	p := s.PromoteSpeculation(u.Context, *u.SpeculationKey)
	if p == nil {
		t.Fatal("no promotion")
	}
	defer p.Stream.Close()
	d, _ := p.Stream.Recv()
	if d.Text != "first" {
		t.Fatal(d)
	}
	deltas <- "ongoing"
	d, _ = p.Stream.Recv()
	if d.Text != "ongoing" || calls.Load() != 1 {
		t.Fatal("stream restarted")
	}
	// Speech onset must retain the promoted output for possible pause/recovery.
	_ = s.SpeechStart()
	if p.Context.Err() != nil {
		t.Fatal("speech prematurely cancelled output")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.CancelGenerationID("stale"); s.TimelineForGeneration(p.GenerationID) }()
	}
	wg.Wait()
	if p.Context.Err() != nil {
		t.Fatal("stale generation cancellation")
	}
	s.CancelGenerationID(p.GenerationID)
	waitSpec(t, specWork(s).done)
	if _, err := p.Stream.Recv(); err == nil {
		t.Fatal("stale stream emitted")
	}
}

func TestSpeculationCooldownAndSingleWorker(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var calls atomic.Int32
	c := specConfig()
	c.Cooldown = time.Second
	s, _ := specSession(t, func(context.Context, stt.Request) (*stt.Result, error) {
		calls.Add(1)
		close(entered)
		<-release
		return &stt.Result{Text: strings.Repeat("a", 10)}, nil
	}, goodSpecLLM(), c)
	say(t, s)
	waitSpec(t, entered)
	w := specWork(s)
	_ = s.SpeechStart()
	_ = s.SpeechEnd()
	specEvent(t, s, "fallback", "")
	if calls.Load() != 1 {
		t.Fatal("concurrent speculative process")
	}
	close(release)
	waitSpec(t, w.done)
	_ = s.SpeechStart()
	_ = s.SpeechEnd()
	specEvent(t, s, "fallback", "")
	if calls.Load() != 1 {
		t.Fatal("cooldown bypassed")
	}
}

func TestSpeculationContinuationInvalidatesCandidate(t *testing.T) {
	s := NewSession(context.Background())
	t.Cleanup(s.Close)
	gate := make(chan struct{})
	if err := s.ConfigureInput(detectorFunc(func(ctx context.Context, _ turndetection.Request) (turndetection.Result, error) {
		select {
		case <-gate:
			return turndetection.Result{Complete: false}, nil
		case <-ctx.Done():
			return turndetection.Result{}, ctx.Err()
		}
	}), EndpointConfig{10 * time.Millisecond, 150 * time.Millisecond, time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureSpeculation(limited.New(goodSpecSTT(), 1), goodSpecLLM(), specConfig()); err != nil {
		t.Fatal(err)
	}
	_ = s.StartRealtimeInput(InputAudioFormatData{Mode: "realtime", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
	say(t, s)
	specEvent(t, s, "ready", "llm")
	close(gate)
	e := specEvent(t, s, "invalidated", "")
	if e.Reason != "continuation_likely" {
		t.Fatal(e.Reason)
	}
	u := nextCommit(t, s)
	if u.SpeculationKey != nil || u.Reason != "max_delay" {
		t.Fatal("continuation speculation reused")
	}
}

func TestSpeculationPromotionChecksDeadlineUnderLock(t *testing.T) {
	s, gate := specSession(t, goodSpecSTT(), goodSpecLLM(), specConfig())
	say(t, s)
	specEvent(t, s, "ready", "llm")
	w := specWork(s)
	waitSpec(t, w.done)
	close(gate)
	u := nextCommit(t, s)
	s.mu.Lock()
	w.timer.Stop()
	w.started = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if s.PromoteSpeculation(u.Context, *u.SpeculationKey) != nil {
		t.Fatal("expired work promoted before timer callback")
	}
}

func TestSpeculationAttemptBudgetAndEmptyTranscript(t *testing.T) {
	c := specConfig()
	c.MaxAttemptsPerTurn = 1
	s, gate := specSession(t, goodSpecSTT(), goodSpecLLM(), c)
	say(t, s)
	specEvent(t, s, "ready", "llm")
	waitSpec(t, specWork(s).done)
	_ = s.SpeechStart()
	_ = s.SpeechEnd()
	specEvent(t, s, "fallback", "")
	close(gate)
	if u := nextCommit(t, s); u.SpeculationKey != nil {
		t.Fatal("per-turn attempt budget bypassed")
	}
	empty, emptyGate := specSession(t, func(context.Context, stt.Request) (*stt.Result, error) { return &stt.Result{}, nil }, func(context.Context, llm.Request) (llm.Stream, error) {
		t.Error("LLM started for empty transcript")
		return nil, errors.New("unexpected")
	}, specConfig())
	say(t, empty)
	specEvent(t, empty, "ready", "stt")
	close(emptyGate)
	u := nextCommit(t, empty)
	p := empty.PromoteSpeculation(u.Context, *u.SpeculationKey)
	if p == nil || p.GenerationID != "" || empty.CurrentGeneration() != "" {
		t.Fatal("empty STT created generation")
	}
	p.Stream.Close()
}
