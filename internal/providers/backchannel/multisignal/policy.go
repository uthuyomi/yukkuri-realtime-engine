// Package multisignal implements a transparent, conservative heuristic policy,
// not a trained Japanese backchannel model. Short corrections remain a known
// ambiguity; deployments may disable acoustic-only recovery via configuration.
package multisignal

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
)

type Policy struct{ AllowAcousticRecovery bool }

func (*Policy) Name() string { return "multisignal-ja-v1" }
func (p *Policy) Classify(ctx context.Context, o backchannel.Observation) (backchannel.Result, error) {
	if err := ctx.Err(); err != nil {
		return backchannel.Result{}, err
	}
	r := func(k backchannel.Kind, s string) (backchannel.Result, error) {
		return backchannel.Result{Kind: k, Reason: s}, nil
	}
	if !o.OutputActive {
		return r(backchannel.Interruption, "no_recoverable_output")
	}
	if o.Misfire {
		return r(backchannel.FalseInterruption, "vad_misfire")
	}
	if o.Resumed {
		return r(backchannel.Interruption, "speech_resumed")
	}
	if o.SpeechDuration >= 800*time.Millisecond {
		return r(backchannel.Interruption, "sustained_speech")
	}
	if o.Speaking || o.SilenceDuration < 250*time.Millisecond {
		return r(backchannel.Ambiguous, "observing_speech")
	}
	if o.Semantic != nil {
		text := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return r
		}, o.Semantic.Text)
		candidate := false
		switch text {
		case "うん", "うんうん", "はい", "ええ", "へえ", "なるほど", "あー", "ふーん":
			candidate = true
		}
		if text != "" && !candidate {
			return r(backchannel.Interruption, "semantic_content")
		}
		if candidate && o.SpeechDuration >= 80*time.Millisecond && o.SpeechDuration <= 750*time.Millisecond && o.EndComplete && (o.Semantic.Confidence == nil || *o.Semantic.Confidence >= 0.8) {
			return r(backchannel.Backchannel, "semantic_and_turn_evidence")
		}
		return r(backchannel.Ambiguous, "insufficient_semantic_evidence")
	}
	// Require overlap with retained output, a single ended segment, meaningful
	// unclipped audio, and a confident EOT signal. Duration alone never recovers.
	if p.AllowAcousticRecovery && o.BufferedAudio > 0 && o.SpeechDuration >= 120*time.Millisecond && o.SpeechDuration <= 550*time.Millisecond && o.Samples >= 1920 && o.RMS >= 0.006 && o.RMS <= 0.25 && o.ClippedFraction < 0.01 && o.EndComplete && o.EndProbability != nil && *o.EndProbability >= 0.85 {
		return r(backchannel.Backchannel, "acoustic_turn_candidate")
	}
	return r(backchannel.Ambiguous, "insufficient_evidence")
}
