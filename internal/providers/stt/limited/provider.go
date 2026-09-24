// Package limited shares admission between ordinary and speculative STT.
package limited

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

var ErrBusy = errors.New("STT capacity occupied")

type Provider struct {
	provider stt.Provider
	slots    chan struct{}
	queue    chan struct{}
}

func New(p stt.Provider, concurrency int) *Provider {
	if p == nil || concurrency < 1 {
		panic("invalid STT admission configuration")
	}
	capacity := stt.Describe(p).QueueCapacity
	if capacity < concurrency {
		capacity = 64
	}
	return &Provider{provider: p, slots: make(chan struct{}, concurrency), queue: make(chan struct{}, capacity)}
}
func (p *Provider) Name() string { return p.provider.Name() }
func (p *Provider) RuntimeInfo() stt.RuntimeInfo {
	info := stt.Describe(p.provider)
	info.Concurrency = cap(p.slots)
	info.QueueCapacity = cap(p.queue)
	return info
}
func (p *Provider) Transcribe(ctx context.Context, r stt.Request) (*stt.Result, error) {
	select {
	case p.queue <- struct{}{}:
	default:
		return nil, stt.ErrCapacity
	}
	defer func() { <-p.queue }()
	start := time.Now()
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.slots }()
	log.Printf("STT admission: queue_wait_ms=%.2f", float64(time.Since(start).Microseconds())/1000)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.provider.Transcribe(ctx, r)
}

// TryTranscribe never queues speculative work behind useful work.
func (p *Provider) TryTranscribe(ctx context.Context, r stt.Request) (*stt.Result, error) {
	select {
	case p.queue <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	defer func() { <-p.queue }()
	select {
	case p.slots <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	defer func() { <-p.slots }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.provider.Transcribe(ctx, r)
}
