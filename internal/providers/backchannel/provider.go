// Package backchannel defines interruption classification independently of turn
// endpointing. An EOT probability is not a backchannel probability.
package backchannel

import (
	"context"
	"time"
)

type Kind string

const (
	Ambiguous         Kind = "ambiguous"
	Backchannel       Kind = "backchannel"
	FalseInterruption Kind = "false_interruption"
	Interruption      Kind = "interruption"
)

// SemanticEvidence is optional server-side STT evidence. Confidence is nil when
// the STT provider does not supply it. The runtime never invents STT confidence.
type SemanticEvidence struct {
	Text       string
	Confidence *float64
}

type Observation struct {
	SpeechDuration  time.Duration
	SilenceDuration time.Duration
	OutputActive    bool
	BufferedAudio   time.Duration
	Speaking        bool
	Resumed         bool
	Misfire         bool
	RMS             float64
	ClippedFraction float64
	Samples         int
	EndProbability  *float64
	EndComplete     bool
	Semantic        *SemanticEvidence
}
type Result struct {
	Kind   Kind
	Reason string
}

// Providers must honor cancellation, be safe across sessions, and keep their
// own work bounded. Slow/failed classifiers are handled by the runtime deadline.
type Provider interface {
	Name() string
	Classify(context.Context, Observation) (Result, error)
}
