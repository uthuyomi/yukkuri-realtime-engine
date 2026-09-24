// Package limited shares admission between ordinary and speculative STT.
package limited

import (
	"context"
	"errors"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

var ErrBusy = errors.New("STT capacity occupied")

type Provider struct {
	provider stt.Provider
	slots    chan struct{}
}

func New(p stt.Provider, concurrency int) *Provider {
	if p == nil || concurrency < 1 {
		panic("invalid STT admission configuration")
	}
	return &Provider{provider: p, slots: make(chan struct{}, concurrency)}
}
func (p *Provider) Name() string { return p.provider.Name() }
func (p *Provider) Transcribe(ctx context.Context, r stt.Request) (*stt.Result, error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.slots }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.provider.Transcribe(ctx, r)
}

// TryTranscribe never queues speculative work behind useful work.
func (p *Provider) TryTranscribe(ctx context.Context, r stt.Request) (*stt.Result, error) {
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
