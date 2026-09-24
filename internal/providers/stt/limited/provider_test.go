package limited

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

type blockingSTT struct {
	entered chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (*blockingSTT) Name() string { return "test" }
func (p *blockingSTT) Transcribe(ctx context.Context, _ stt.Request) (*stt.Result, error) {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	if n > p.peak.Load() {
		p.peak.Store(n)
	}
	p.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestSharedAdmissionCancellationAndNoSpeculativeQueue(t *testing.T) {
	raw := &blockingSTT{entered: make(chan struct{}, 2)}
	p := New(raw, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = p.Transcribe(ctx, stt.Request{}) }()
	<-raw.entered
	if _, err := p.TryTranscribe(context.Background(), stt.Request{}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := p.Transcribe(waitCtx, stt.Request{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cancel()
	<-done
	nextCtx, nextCancel := context.WithCancel(context.Background())
	nextDone := make(chan struct{})
	go func() { defer close(nextDone); _, _ = p.TryTranscribe(nextCtx, stt.Request{}) }()
	<-raw.entered
	nextCancel()
	<-nextDone
	if raw.peak.Load() != 1 || raw.active.Load() != 0 {
		t.Fatal("process budget or cleanup failed")
	}
}

func TestAdmissionQueueIsBounded(t *testing.T) {
	raw := &blockingSTT{entered: make(chan struct{}, 1)}
	p := New(raw, 1)
	// Fill the bounded queue without scheduling dozens of blocked goroutines.
	for range cap(p.queue) {
		p.queue <- struct{}{}
	}
	if _, err := p.Transcribe(context.Background(), stt.Request{}); !errors.Is(err, stt.ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := p.TryTranscribe(context.Background(), stt.Request{}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if raw.active.Load() != 0 {
		t.Fatal("admission started inference")
	}
}
