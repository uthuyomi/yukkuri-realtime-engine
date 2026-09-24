package httptransport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

func TestHealthRegression(t *testing.T) {
	s := New(Config{}, engine.New())
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

type testClassifier struct{ kind backchannel.Kind }

func (testClassifier) Name() string { return "test" }
func (p testClassifier) Classify(context.Context, backchannel.Observation) (backchannel.Result, error) {
	return backchannel.Result{Kind: p.kind, Reason: "test"}, nil
}
func sendScoped(t *testing.T, c *websocket.Conn, ctx context.Context, kind, id string, data any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": kind, "generation_id": id, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}
func TestTwoPhaseInterruptionProtocol(t *testing.T) {
	for _, kind := range []backchannel.Kind{backchannel.Backchannel, backchannel.FalseInterruption, backchannel.Interruption, backchannel.Ambiguous} {
		t.Run(string(kind), func(t *testing.T) {
			s := New(Config{}, engine.New())
			st := &testSTT{calls: make(chan stt.Request, 1)}
			s.SetSTTProvider(st)
			d := &testDetector{entered: make(chan struct{}, 1), release: make(chan struct{})}
			close(d.release)
			if err := s.SetTurnDetector(d, realtime.EndpointConfig{MinDelay: 30 * time.Millisecond, MaxDelay: 100 * time.Millisecond, MaxTurnDuration: time.Second}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetBackchannelProvider(testClassifier{kind}, realtime.InterruptionConfig{DecisionWindow: 400 * time.Millisecond}); err != nil {
				t.Fatal(err)
			}
			c, ctx := connectTest(t, s)
			sendEvent(t, c, ctx, "generation.create", nil)
			gen := readType(t, c, ctx, "generation.created").Generation
			sendEvent(t, c, ctx, "input_audio.start", realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", Mode: "realtime"})
			sendScoped(t, c, ctx, "input_audio.speech_start", gen, realtime.InterruptionRequestData{InterruptionID: "p1"})
			readType(t, c, ctx, "interruption.suspected")
			if err := c.Write(ctx, websocket.MessageBinary, make([]byte, 3200)); err != nil {
				t.Fatal(err)
			}
			if kind == backchannel.FalseInterruption {
				sendEvent(t, c, ctx, "input_audio.vad_misfire", nil)
			} else {
				sendEvent(t, c, ctx, "input_audio.speech_end", nil)
			}
			if kind == backchannel.Interruption || kind == backchannel.Ambiguous {
				readType(t, c, ctx, "interruption.confirmed")
				readType(t, c, ctx, "generation.cancelled")
				select {
				case <-st.calls:
				case <-ctx.Done():
					t.Fatal("true interruption did not reach STT")
				}
			} else {
				e := readType(t, c, ctx, "interruption.recovered")
				if e.Generation != gen {
					t.Fatal("wrong generation resumed")
				}
				select {
				case <-st.calls:
					t.Fatal("recovered input reached STT")
				case <-time.After(100 * time.Millisecond):
				}
				// The original text generation is still writable, proving its context survived.
				sendEvent(t, c, ctx, "response.text.done", nil)
				readType(t, c, ctx, "generation.done")
			}
		})
	}
}
