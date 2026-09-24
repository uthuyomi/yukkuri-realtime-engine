package realtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
)

type detectorFunc func(context.Context, turndetection.Request) (turndetection.Result, error)

func (detectorFunc) Name() string               { return "test" }
func (detectorFunc) AudioWindow() time.Duration { return time.Second }
func (d detectorFunc) Detect(c context.Context, r turndetection.Request) (turndetection.Result, error) {
	return d(c, r)
}
func inputSession(t *testing.T, d detectorFunc) *Session {
	t.Helper()
	s := NewSession(context.Background())
	if err := s.ConfigureInput(d, EndpointConfig{30 * time.Millisecond, 200 * time.Millisecond, time.Second}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.StartRealtimeInput(InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", Mode: "realtime"}); err != nil {
		t.Fatal(err)
	}
	return s
}
func say(t *testing.T, s *Session) {
	t.Helper()
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRealtimeAudio(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := s.SpeechEnd(); err != nil {
		t.Fatal(err)
	}
}
func nextCommit(t *testing.T, s *Session) InputUpdate {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case u := <-s.InputUpdates():
			if u.State == TurnComplete {
				return u
			}
		case <-timer.C:
			t.Fatal("no commit")
			return InputUpdate{}
		}
	}
}
func TestEndpointDecisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		complete bool
		err      error
		reason   string
	}{
		{"complete", true, nil, "detector_complete"},
		{"continuation", false, nil, "max_delay"},
		{"failure", false, errors.New("unavailable"), "max_delay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := make(chan time.Time, 1)
			s := inputSession(t, func(ctx context.Context, r turndetection.Request) (turndetection.Result, error) {
				calls <- time.Now()
				return turndetection.Result{Complete: tc.complete}, tc.err
			})
			start := time.Now()
			say(t, s)
			u := nextCommit(t, s)
			if u.Reason != tc.reason {
				t.Fatalf("reason: %s", u.Reason)
			}
			if (<-calls).Sub(start) < 25*time.Millisecond {
				t.Fatal("detected before minimum delay")
			}
			if tc.reason == "max_delay" && time.Since(start) < 190*time.Millisecond {
				t.Fatal("fallback before maximum")
			}
			if len(u.Audio) != 1024 || u.Context.Err() != nil {
				t.Fatal("invalid committed audio/context")
			}
			if err := s.AppendRealtimeAudio([]byte{1, 2}); err != nil {
				t.Fatal(err)
			}
			if u.Audio[0] != 0 {
				t.Fatal("committed storage reused")
			}
			if err := s.SpeechStart(); err != nil {
				t.Fatal(err)
			}
			if u.Context.Err() == nil {
				t.Fatal("old STT context not cancelled")
			}
		})
	}
}
func TestResumeRejectsLateDetectorAndPreservesAudio(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	s := inputSession(t, func(ctx context.Context, r turndetection.Request) (turndetection.Result, error) {
		close(entered)
		<-release
		close(returned)
		return turndetection.Result{Complete: true}, nil
	})
	say(t, s)
	<-entered
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-returned
	if err := s.AppendRealtimeAudio(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(220 * time.Millisecond)
	if s.TurnState() != TurnSpeaking {
		t.Fatal("stale result committed resumed speech")
	}
	s.mu.Lock()
	n := s.inputAudioBuffer.Len()
	s.mu.Unlock()
	if n != 2048 {
		t.Fatalf("continuation audio lost: %d", n)
	}
}
func TestMaxDeadlineCancelsSlowInference(t *testing.T) {
	done := make(chan struct{})
	s := inputSession(t, func(ctx context.Context, r turndetection.Request) (turndetection.Result, error) {
		<-ctx.Done()
		close(done)
		return turndetection.Result{}, ctx.Err()
	})
	say(t, s)
	if nextCommit(t, s).Reason != "max_delay" {
		t.Fatal("expected fallback")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("inference was not cancelled")
	}
}
func TestInputCancelAndGenerationCancelAreSeparate(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	s := inputSession(t, func(ctx context.Context, r turndetection.Request) (turndetection.Result, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return turndetection.Result{}, ctx.Err()
	})
	_, genCtx, _ := s.StartGeneration()
	say(t, s)
	<-started
	s.CancelGeneration()
	if genCtx.Err() == nil || s.TurnState() != TurnWaiting {
		t.Fatal("generation cancellation affected input")
	}
	s.CancelInput(false)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("input inference not cancelled")
	}
	if s.TurnState() != TurnListening {
		t.Fatal("expected listening")
	}
	s.Close()
	select {
	case <-s.input.done:
	default:
		t.Fatal("runtime leaked")
	}
}
func TestInputBufferBoundsAndValidation(t *testing.T) {
	s := inputSession(t, func(context.Context, turndetection.Request) (turndetection.Result, error) {
		return turndetection.Result{}, nil
	})
	for i := 0; i < 100; i++ {
		if err := s.AppendRealtimeAudio(make([]byte, 1024)); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	n := s.inputAudioBuffer.Len()
	s.mu.Unlock()
	if n > 16000 {
		t.Fatal("unbounded idle silence")
	}
	if s.AppendRealtimeAudio([]byte{1}) == nil {
		t.Fatal("accepted odd PCM")
	}
	if s.AppendRealtimeAudio(make([]byte, 16386)) == nil {
		t.Fatal("accepted oversized PCM")
	}
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	var err error
	for i := 0; i < 40; i++ {
		err = s.AppendRealtimeAudio(make([]byte, 1024))
		if err != nil {
			break
		}
	}
	if err == nil || s.TurnState() != TurnIdle {
		t.Fatal("turn limit not enforced")
	}
}
func TestSupersededTranscriptCannotStartGeneration(t *testing.T) {
	s := NewSession(context.Background())
	defer s.Close()
	ctx := s.NewInputResponseContext()
	s.CancelGeneration()
	if id, _, _ := s.StartGenerationForInput(ctx); id != "" {
		t.Fatal("stale transcript started generation")
	}
}

func TestStopBeforeMinimumAndRestart(t *testing.T) {
	calls := make(chan struct{}, 1)
	s := inputSession(t, func(context.Context, turndetection.Request) (turndetection.Result, error) {
		calls <- struct{}{}
		return turndetection.Result{Complete: true}, nil
	})
	say(t, s)
	s.CancelInput(true)
	time.Sleep(60 * time.Millisecond)
	select {
	case <-calls:
		t.Fatal("detected stopped input")
	default:
	}
	if s.TurnState() != TurnIdle {
		t.Fatal("not idle")
	}
	if err := s.StartRealtimeInput(InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", Mode: "realtime"}); err != nil {
		t.Fatal(err)
	}
	say(t, s)
	if nextCommit(t, s).Reason != "detector_complete" {
		t.Fatal("restart did not detect")
	}
}

func TestMisfirePreservesExistingTurn(t *testing.T) {
	s := inputSession(t, func(context.Context, turndetection.Request) (turndetection.Result, error) {
		return turndetection.Result{Complete: true}, nil
	})
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	if err := s.VADMisfire(); err != nil {
		t.Fatal(err)
	}
	if s.TurnState() != TurnListening {
		t.Fatal("isolated noise started turn")
	}
	say(t, s)
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	if err := s.VADMisfire(); err != nil {
		t.Fatal(err)
	}
	if len(nextCommit(t, s).Audio) != 1024 {
		t.Fatal("misfire discarded prior valid speech")
	}
}

func TestCloseDuringInference(t *testing.T) {
	entered := make(chan struct{})
	done := make(chan struct{})
	s := inputSession(t, func(ctx context.Context, _ turndetection.Request) (turndetection.Result, error) {
		close(entered)
		<-ctx.Done()
		close(done)
		return turndetection.Result{}, ctx.Err()
	})
	say(t, s)
	<-entered
	s.Close()
	select {
	case <-done:
	default:
		t.Fatal("close did not join inference")
	}
}
