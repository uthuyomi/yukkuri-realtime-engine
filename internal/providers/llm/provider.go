package llm

import "context"

type Message struct {
	Role    string
	Content string
}

type Request struct {
	Messages []Message
}

type Delta struct {
	Text string
}

type Stream interface {
	Recv() (Delta, error)
	Close() error
}

type Provider interface {
	Name() string

	Generate(
		ctx context.Context,
		req Request,
	) (Stream, error)
}