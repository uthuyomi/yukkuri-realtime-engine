package speech

import (
	"context"
	"errors"
	"sync"
)

var ErrPipelineClosed = errors.New(
	"speech pipeline is closed",
)

type Chunk struct {
	Sequence   int
	Text       string
	SourceText string
}

type Pipeline struct {
	ctx    context.Context
	cancel context.CancelFunc

	chunker    *Chunker
	normalizer *Normalizer

	mu       sync.Mutex
	sequence int
	closed   bool

	output chan Chunk
}

func NewPipeline(
	parent context.Context,
	config ChunkerConfig,
) *Pipeline {
	ctx, cancel := context.WithCancel(parent)

	return &Pipeline{
		ctx:        ctx,
		cancel:     cancel,
		chunker:    NewChunker(config),
		normalizer: NewNormalizer(),
		output:     make(chan Chunk, 16),
	}
}

func (p *Pipeline) Output() <-chan Chunk {
	return p.output
}

func (p *Pipeline) Push(delta string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrPipelineClosed
	}

	chunks := p.chunker.Push(delta)

	return p.emitChunks(chunks)
}

func (p *Pipeline) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrPipelineClosed
	}

	chunks := p.chunker.Flush()

	return p.emitChunks(chunks)
}

func (p *Pipeline) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	chunks := p.chunker.Flush()

	if err := p.emitChunks(chunks); err != nil {
		p.closed = true
		p.cancel()
		close(p.output)

		return err
	}

	p.closed = true
	close(p.output)

	return nil
}

func (p *Pipeline) Cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	p.closed = true
	p.cancel()
	close(p.output)
}

func (p *Pipeline) emitChunks(
	chunks []string,
) error {
	for _, text := range chunks {
		normalized, err :=
			p.normalizer.Normalize(text)

		if err != nil {
			return err
		}

		if normalized == "" {
			continue
		}

		if err := p.emit(normalized, text); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pipeline) emit(text, source string) error {
	chunk := Chunk{
		Sequence:   p.sequence,
		Text:       text,
		SourceText: source,
	}

	select {
	case <-p.ctx.Done():
		return p.ctx.Err()

	case p.output <- chunk:
		p.sequence++

		return nil
	}
}
