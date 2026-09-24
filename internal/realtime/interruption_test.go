package realtime

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel/multisignal"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
)

type classifierFunc func(context.Context, backchannel.Observation) (backchannel.Result, error)

func (classifierFunc) Name() string { return "test" }
func (f classifierFunc) Classify(c context.Context, o backchannel.Observation) (backchannel.Result, error) {
	return f(c, o)
}
func interruptionSession(t *testing.T, p backchannel.Provider) (*Session, string, context.Context) {
	t.Helper()
	s := inputSession(t, func(context.Context, turndetection.Request) (turndetection.Result, error) {
		return turndetection.Result{Complete: true, Probability: 0.95}, nil
	})
	if err := s.ConfigureInterruption(p, InterruptionConfig{600 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	id, ctx, _ := s.StartGeneration()
	tl := s.Timeline()
	tl.SetFormat(16000, 1)
	tl.AddGenerated(32000)
	tl.AddSent(32000)
	tl.SetPlayed(8000)
	return s, id, ctx
}
func voice(samples int) []byte {
	b := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(1000))
	}
	return b
}
func startSuspect(t *testing.T, s *Session, id string) {
	t.Helper()
	if err := s.SpeechStartWithInterruption(id, "p1"); err != nil {
		t.Fatal(err)
	}
}
func shortSpeech(t *testing.T, s *Session, id string) {
	t.Helper()
	startSuspect(t, s, id)
	if err := s.AppendRealtimeAudio(voice(4800)); err != nil {
		t.Fatal(err)
	}
	if err := s.SpeechEnd(); err != nil {
		t.Fatal(err)
	}
}
func decision(t *testing.T, s *Session, kind string) InterruptionUpdate {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case u := <-s.InputUpdates():
			if u.EventType == kind {
				return *u.Interruption
			}
		case <-timer.C:
			t.Fatal("missing decision", kind)
			return InterruptionUpdate{}
		}
	}
}
func fixedClassifier(k backchannel.Kind) classifierFunc {
	return func(context.Context, backchannel.Observation) (backchannel.Result, error) {
		return backchannel.Result{Kind: k, Reason: "test"}, nil
	}
}

func TestShortBackchannelKeepsGenerationAndSuppressesTurn(t *testing.T) {
	s, id, ctx := interruptionSession(t, &multisignal.Policy{AllowAcousticRecovery: true})
	shortSpeech(t, s, id)
	if ctx.Err() != nil {
		t.Fatal("speech start cancelled output")
	}
	u := decision(t, s, "interruption.recovered")
	if u.Decision != backchannel.Backchannel || u.InterruptionID != "p1" || u.TurnID == "" {
		t.Fatal(u)
	}
	if s.CurrentGeneration() != id || ctx.Err() != nil || s.TurnState() != TurnListening {
		t.Fatal("generation was not recovered")
	}
	if snap := s.Timeline().Snapshot(); snap.PlayedFrames != 8000 || snap.Paused {
		t.Fatal("pause advanced playback", snap)
	}
	// Recovery has no committed audio, hence no path to STT/LLM.
	for {
		select {
		case u := <-s.InputUpdates():
			if len(u.Audio) > 0 {
				t.Fatal("backchannel committed")
			}
		default:
			return
		}
	}
}
func TestTrueInterruptionCancelsThenCommits(t *testing.T) {
	s, id, ctx := interruptionSession(t, fixedClassifier(backchannel.Interruption))
	shortSpeech(t, s, id)
	decision(t, s, "interruption.confirmed")
	if ctx.Err() == nil {
		t.Fatal("output not cancelled")
	}
	if len(nextCommit(t, s).Audio) != 9600 {
		t.Fatal("user audio lost")
	}
}
func TestFalseInterruptionRecovery(t *testing.T) {
	s, id, ctx := interruptionSession(t, fixedClassifier(backchannel.Ambiguous))
	startSuspect(t, s, id)
	if err := s.PlaybackPaused(id, "p1", 0.75); err != nil {
		t.Fatal(err)
	}
	if err := s.VADMisfire(); err != nil {
		t.Fatal(err)
	}
	if err := s.SpeechEnd(); err != nil {
		t.Fatal(err)
	} // late end must not restart endpointing
	u := decision(t, s, "interruption.recovered")
	if u.Decision != backchannel.FalseInterruption || ctx.Err() != nil {
		t.Fatal(u)
	}
	if got := s.Timeline().Snapshot(); got.PlayedFrames != 12000 || got.PausePlayedFrames != 12000 || got.BufferedFrames() != 20000 {
		t.Fatal(got)
	}
}
func TestAmbiguousTimeoutAndClassifierFailure(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		p            backchannel.Provider
	}{
		{"timeout", "decision_timeout", fixedClassifier(backchannel.Ambiguous)},
		{"failure", "classifier_failed", classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
			return backchannel.Result{}, errors.New("unavailable")
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id, ctx := interruptionSession(t, tc.p)
			shortSpeech(t, s, id)
			u := decision(t, s, "interruption.confirmed")
			if u.Reason != tc.reason || ctx.Err() == nil {
				t.Fatal(u)
			}
			nextCommit(t, s)
		})
	}
}
func TestCompletionWhilePausedRemainsRecoverable(t *testing.T) {
	s, id, ctx := interruptionSession(t, fixedClassifier(backchannel.Backchannel))
	shortSpeech(t, s, id)
	if !s.MarkGenerationDone(id) {
		t.Fatal("completion rejected")
	}
	decision(t, s, "interruption.recovered")
	if ctx.Err() != nil || s.CurrentGeneration() != id {
		t.Fatal("completed generation cancelled")
	}
}
func TestRepeatedSpeechAndResumeConfirms(t *testing.T) {
	s, id, ctx := interruptionSession(t, fixedClassifier(backchannel.Backchannel))
	shortSpeech(t, s, id)
	if err := s.SpeechEnd(); err != nil {
		t.Fatal(err)
	}
	if err := s.SpeechStartWithInterruption(id, "p2"); err != nil {
		t.Fatal(err)
	}
	u := decision(t, s, "interruption.confirmed")
	if u.InterruptionID != "p2" || u.Reason != "speech_resumed" || ctx.Err() == nil {
		t.Fatal(u)
	}
	if s.TurnState() != TurnSpeaking {
		t.Fatal("resumed input was committed early")
	}
}
func TestNewGenerationRejectsOldClassifier(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	s, id, _ := interruptionSession(t, classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
		close(entered)
		<-release
		close(returned)
		return backchannel.Result{Kind: backchannel.Backchannel}, nil
	}))
	shortSpeech(t, s, id)
	<-entered
	newID, newCtx, _ := s.StartGeneration()
	close(release)
	<-returned
	if err := s.PlaybackPaused(id, "p1", 1); err != nil {
		t.Fatal(err)
	}
	s.PlaybackFailed(id, "p1", "stale")
	if s.CancelGenerationID(id) != "" || s.CurrentGeneration() != newID || newCtx.Err() != nil {
		t.Fatal("stale operation affected new generation")
	}
	if s.InterruptionState() != InterruptionIdle {
		t.Fatal("old classifier recovered new generation")
	}
}
func TestDisconnectCancelsAndJoinsClassifier(t *testing.T) {
	entered := make(chan struct{})
	done := make(chan struct{})
	s, id, _ := interruptionSession(t, classifierFunc(func(ctx context.Context, _ backchannel.Observation) (backchannel.Result, error) {
		close(entered)
		<-ctx.Done()
		close(done)
		return backchannel.Result{}, ctx.Err()
	}))
	shortSpeech(t, s, id)
	<-entered
	s.Close()
	select {
	case <-done:
	default:
		t.Fatal("classifier leaked")
	}
	if s.InterruptionState() != InterruptionIdle {
		t.Fatal("pending decision survived disconnect")
	}
}
func TestPauseOverflowPromotesCancellation(t *testing.T) {
	s, id, ctx := interruptionSession(t, fixedClassifier(backchannel.Ambiguous))
	startSuspect(t, s, id)
	s.PlaybackFailed(id, "p1", "playback_overflow")
	if decision(t, s, "interruption.confirmed").Reason != "playback_overflow" || ctx.Err() == nil {
		t.Fatal("overflow did not cancel")
	}
}
func TestConcurrentPlaybackCompletionAndCancellation(t *testing.T) {
	s, id, _ := interruptionSession(t, fixedClassifier(backchannel.Ambiguous))
	startSuspect(t, s, id)
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				s.MarkGenerationDone(id)
				_ = s.PlaybackPaused(id, "p1", 0.6)
				s.Timeline().Snapshot()
			}
		}()
	}
	s.CancelGenerationID(id)
	wg.Wait()
	if s.CurrentGeneration() != "" {
		t.Fatal("cancelled generation resurrected")
	}
}

func TestSpeechResumesDuringClassification(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s, id, ctx := interruptionSession(t, classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
		close(entered)
		<-release
		return backchannel.Result{Kind: backchannel.Backchannel}, nil
	}))
	shortSpeech(t, s, id)
	<-entered
	if err := s.SpeechStartWithInterruption(id, "p2"); err != nil {
		t.Fatal(err)
	}
	close(release)
	u := decision(t, s, "interruption.confirmed")
	if u.Reason != "speech_resumed" || ctx.Err() == nil {
		t.Fatal(u)
	}
	s.Close() // joins even the stale classifier
}

func TestOutputAppearingDuringExistingTurnCannotDiscardItsPrefix(t *testing.T) {
	s, _, _ := interruptionSession(t, fixedClassifier(backchannel.Backchannel))
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRealtimeAudio(voice(4800)); err != nil {
		t.Fatal(err)
	}
	id, ctx, _ := s.StartGeneration()
	if err := s.BeginInterruption(id, "late"); err != nil {
		t.Fatal(err)
	}
	if u := decision(t, s, "interruption.confirmed"); u.Reason != "speech_resumed" || ctx.Err() == nil {
		t.Fatal(u)
	}
	if err := s.SpeechEnd(); err != nil {
		t.Fatal(err)
	}
	if len(nextCommit(t, s).Audio) != 9600 {
		t.Fatal("earlier input prefix was discarded")
	}
}
