// Package turndetection defines the replaceable audio turn detection boundary.
package turndetection

import (
	"context"
	"time"
)

// Request owns an immutable PCM signed 16-bit little endian, 16 kHz mono snapshot.
// Audio contains only the current turn, limited to the provider's context window.
type Request struct{ Audio []byte }

type Result struct {
	Complete    bool
	Probability float64
}

// Implementations must support concurrent sessions and return promptly on context
// cancellation. No provider may turn inference failures into positive predictions.
type Provider interface {
	Name() string
	AudioWindow() time.Duration
	Detect(context.Context, Request) (Result, error)
}
