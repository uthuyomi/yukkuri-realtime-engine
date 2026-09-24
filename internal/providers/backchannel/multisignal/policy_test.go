package multisignal

import (
	"context"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"testing"
	"time"
)

func TestMultipleSignalsRequired(t *testing.T) {
	prob := 0.95
	base := backchannel.Observation{OutputActive: true, BufferedAudio: time.Second, SpeechDuration: 300 * time.Millisecond, SilenceDuration: 300 * time.Millisecond, Samples: 4800, RMS: 0.03, EndComplete: true, EndProbability: &prob}
	p := &Policy{AllowAcousticRecovery: true}
	for _, tc := range []struct {
		name   string
		modify func(*backchannel.Observation)
		want   backchannel.Kind
	}{
		{"candidate", func(*backchannel.Observation) {}, backchannel.Backchannel},
		{"duration_alone", func(o *backchannel.Observation) { o.EndProbability = nil }, backchannel.Ambiguous},
		{"noise", func(o *backchannel.Observation) { o.RMS = 0 }, backchannel.Ambiguous},
		{"clipping", func(o *backchannel.Observation) { o.ClippedFraction = 0.5 }, backchannel.Ambiguous},
		{"not_complete", func(o *backchannel.Observation) { o.EndComplete = false }, backchannel.Ambiguous},
		{"no_output", func(o *backchannel.Observation) { o.OutputActive = false }, backchannel.Interruption},
		{"resumed", func(o *backchannel.Observation) { o.Resumed = true }, backchannel.Interruption},
		{"correction", func(o *backchannel.Observation) { o.Semantic = &backchannel.SemanticEvidence{Text: "いや東京の"} }, backchannel.Interruption},
		{"semantic_backchannel", func(o *backchannel.Observation) { o.Semantic = &backchannel.SemanticEvidence{Text: "うん。"} }, backchannel.Backchannel},
		{"still_speaking", func(o *backchannel.Observation) { o.Speaking = true }, backchannel.Ambiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.modify(&o)
			r, err := p.Classify(context.Background(), o)
			if err != nil || r.Kind != tc.want {
				t.Fatal(r, err)
			}
		})
	}
	p.AllowAcousticRecovery = false
	if r, _ := p.Classify(context.Background(), base); r.Kind != backchannel.Ambiguous {
		t.Fatal("disabled acoustic recovery still classifies")
	}
}
