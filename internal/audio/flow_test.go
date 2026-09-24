package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func flowTest(t *testing.T) *FlowController {
	t.Helper()
	f, err := NewFlowController(8000)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func waitBlocked(t *testing.T, f *FlowController) {
	t.Helper()
	deadline := time.After(time.Second)
	for f.Snapshot().WaitCount == 0 {
		select {
		case <-deadline:
			t.Fatal("sender did not wait")
		case <-time.After(time.Millisecond):
		}
	}
}
func TestCreditWindowBlocksResumesAndDoesNotMintDuplicates(t *testing.T) {
	f := flowTest(t)
	f.RequireCredit()
	done := make(chan int64, 1)
	go func() { n, _ := f.Reserve(context.Background(), 9000); done <- n }()
	waitBlocked(t, f)
	if err := f.Update(Credit{Capacity: 800}); err != nil {
		t.Fatal(err)
	}
	if n := <-done; n != 800 {
		t.Fatal(n)
	}
	if err := f.Update(Credit{Capacity: 800}); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot().Available != 0 {
		t.Fatal("duplicate minted credit")
	}
	if err := f.Update(Credit{Capacity: 800, Received: 800, Played: 100, Buffered: 700}); err != nil {
		t.Fatal(err)
	}
	if n, err := f.Reserve(context.Background(), 800); err != nil || n != 100 {
		t.Fatal(n, err)
	}
	if err := f.Update(Credit{Capacity: 1600, Received: 700, Played: 50, Buffered: 650}); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot().Available != 0 {
		t.Fatal("stale credit changed window")
	}
	f.Acknowledge(900)
	if f.Snapshot().Played != 100 {
		t.Fatal("legacy progress minted explicit credit")
	}
	if f.Snapshot().WaitDuration <= 0 {
		t.Fatal("wait metric missing")
	}
}
func TestCreditCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "timeout"}[timeout], func(t *testing.T) {
			f := flowTest(t)
			f.RequireCredit()
			if timeout {
				f.timeout = 20 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := f.Reserve(ctx, 1); result <- err }()
			waitBlocked(t, f)
			if !timeout {
				cancel()
			}
			select {
			case err := <-result:
				if err == nil || (!timeout && !errors.Is(err, context.Canceled)) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("leaked waiter")
			}
		})
	}
}
func TestLegacyFallbackIsBoundedAndProgressReplenishes(t *testing.T) {
	f := flowTest(t)
	n, err := f.Reserve(context.Background(), 999999)
	if err != nil || n != 16000 {
		t.Fatal(n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan int64, 1)
	go func() { n, _ := f.Reserve(ctx, 4000); result <- n }()
	waitBlocked(t, f)
	f.Acknowledge(2000)
	if n := <-result; n != 2000 {
		t.Fatal(n)
	}
	if f.Snapshot().Reserved-f.Snapshot().Played != 16000 {
		t.Fatal(f.Snapshot())
	}
}
func TestCreditValidation(t *testing.T) {
	for _, c := range []Credit{{Capacity: 0}, {Capacity: 240001}, {Capacity: 800, Received: 1, Buffered: 1}, {Capacity: 800, Played: -1}, {Capacity: 800, Played: 1}, {Capacity: 800, Buffered: 1}} {
		if err := flowTest(t).Update(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	for _, rate := range []int{0, 999, 192001} {
		if _, err := NewFlowController(rate); err == nil {
			t.Fatal(rate)
		}
	}
}

func TestTimelineSourceFramesMonotonicThroughPause(t *testing.T) {
	p := NewPlaybackTimeline("g")
	p.SetFormat(8000, 1)
	p.AddGenerated(8000)
	p.AddSent(8000)
	p.SetPlayed(4000)
	p.SetPaused(true)
	p.SetPlayed(1000)
	if s := p.Snapshot(); s.PlayedFrames != 4000 || s.PausePlayedFrames != 4000 || s.PlayedDuration() != 500*time.Millisecond {
		t.Fatal(s)
	}
	p.SetPaused(false)
	p.SetPlayed(9999)
	if p.Snapshot().PlayedFrames != 8000 {
		t.Fatal(p.Snapshot())
	}
}
