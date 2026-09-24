package speech

import (
	"context"
	"testing"
)

func TestSemanticSourceSurvivesSpeechNormalization(t *testing.T) {
	p := NewPipeline(context.Background(), ChunkerConfig{SoftLimit: 30, HardLimit: 60})
	if err := p.Push("札幌：晴れ"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	c := <-p.Output()
	if c.Sequence != 0 || c.SourceText != "札幌：晴れ" || c.Text != "札幌、晴れ" {
		t.Fatal(c)
	}
}
