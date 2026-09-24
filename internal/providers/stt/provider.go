package stt

import (
	"context"
)

type AudioFormat struct {
	SampleRate int
	Channels   int
	Encoding   string
}

type Request struct {
	Audio  []byte
	Format AudioFormat
}

type Result struct {
	Text       string
	Confidence float64
	Language   string
}

type Provider interface {
	Name() string

	Transcribe(
		ctx context.Context,
		req Request,
	) (*Result, error)
}