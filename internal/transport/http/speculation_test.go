package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

type specSTT func(context.Context, stt.Request) (*stt.Result, error)

func (specSTT) Name() string                                                       { return "test" }
func (f specSTT) Transcribe(c context.Context, r stt.Request) (*stt.Result, error) { return f(c, r) }

type specLLM func(context.Context, llm.Request) (llm.Stream, error)

func (specLLM) Name() string                                                    { return "test" }
func (f specLLM) Generate(c context.Context, r llm.Request) (llm.Stream, error) { return f(c, r) }

type answerStream struct{ sent bool }

func (s *answerStream) Recv() (llm.Delta, error) {
	if s.sent {
		return llm.Delta{}, io.EOF
	}
	s.sent = true
	return llm.Delta{Text: "private answer."}, nil
}
func (*answerStream) Close() error { return nil }

type countingTTS struct{ calls atomic.Int32 }

func (*countingTTS) Name() string { return "test" }
func (p *countingTTS) Synthesize(c context.Context, r tts.Request) (*tts.Stream, error) {
	p.calls.Add(1)
	return (testTTS{}).Synthesize(c, r)
}
func specServer(t *testing.T, p stt.Provider, l llm.Provider) (*Server, *testDetector, *countingTTS) {
	t.Helper()
	e := engine.New()
	voice := &countingTTS{}
	if err := e.RegisterTTS(voice); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, e)
	s.SetSTTProvider(p)
	s.SetLLMProvider(l)
	d := &testDetector{entered: make(chan struct{}, 1), release: make(chan struct{})}
	if err := s.SetTurnDetector(d, realtime.EndpointConfig{MinDelay: 10 * time.Millisecond, MaxDelay: 2 * time.Second, MaxTurnDuration: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	return s, d, voice
}
func specSpeak(t *testing.T, c *websocket.Conn, ctx context.Context) {
	t.Helper()
	sendEvent(t, c, ctx, "input_audio.start", realtime.InputAudioFormatData{Mode: "realtime", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
	sendEvent(t, c, ctx, "input_audio.speech_start", nil)
	if err := c.Write(ctx, websocket.MessageBinary, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	sendEvent(t, c, ctx, "input_audio.speech_end", nil)
}
func readSpecEvent(t *testing.T, c *websocket.Conn, ctx context.Context, want, stage string) {
	t.Helper()
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ != websocket.MessageText {
			t.Fatal("PCM leaked before commit")
		}
		var e realtime.Event
		if err = json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(e.Type, "response.") || e.Type == "input_audio.transcript.final" || e.Type == "generation.created" || e.Type == "error" {
			t.Fatalf("official output before commit: %s", e.Type)
		}
		if strings.Contains(string(b), "private") {
			t.Fatal("speculative text leaked in metadata")
		}
		if e.Type == want {
			var u realtime.SpeculationUpdate
			if err = json.Unmarshal(e.Data, &u); err != nil {
				t.Fatal(err)
			}
			if stage == "" || stage == u.Stage {
				return
			}
		}
	}
}
func TestSpeculationWireBarrierAndReuse(t *testing.T) {
	var sttCalls, llmCalls atomic.Int32
	s, d, voice := specServer(t, specSTT(func(context.Context, stt.Request) (*stt.Result, error) {
		sttCalls.Add(1)
		return &stt.Result{Text: "private transcript"}, nil
	}), specLLM(func(context.Context, llm.Request) (llm.Stream, error) { llmCalls.Add(1); return &answerStream{}, nil }))
	c, ctx := connectTest(t, s)
	specSpeak(t, c, ctx)
	sendEvent(t, c, ctx, "input_audio.speech_end", nil)
	readSpecEvent(t, c, ctx, "speculation.ready", "llm")
	if voice.calls.Load() != 0 {
		t.Fatal("speculative TTS executed")
	}
	close(d.release)
	seen := map[string]int{}
	var generation string
	var text strings.Builder
	pcm := false
	for seen["generation.done"] == 0 || seen["response.text.done"] == 0 || seen["speculation.promoted"] == 0 {
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.MessageBinary {
			pcm = true
			continue
		}
		var e realtime.Event
		if err = json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		seen[e.Type]++
		if e.Type == "error" {
			t.Fatalf("%s", e.Data)
		}
		if e.Type == "generation.created" {
			generation = e.Generation
		}
		if strings.HasPrefix(e.Type, "response.") && (generation == "" || generation != e.Generation) {
			t.Fatal("missing/stale official generation")
		}
		if e.Type == "response.text.delta" {
			var delta realtime.TextDeltaData
			_ = json.Unmarshal(e.Data, &delta)
			text.WriteString(delta.Text)
		}
	}
	if seen["input_audio.transcript.final"] != 1 || seen["generation.created"] != 1 || text.String() != "private answer." || !pcm || voice.calls.Load() != 1 || sttCalls.Load() != 1 || llmCalls.Load() != 1 {
		t.Fatalf("reuse/output mismatch: %v STT=%d LLM=%d TTS=%d text=%q PCM=%v", seen, sttCalls.Load(), llmCalls.Load(), voice.calls.Load(), text.String(), pcm)
	}
}
func TestSpeculationWireFallback(t *testing.T) {
	for _, failure := range []string{"stt", "llm", "overflow", "timeout_after_commit"} {
		t.Run(failure, func(t *testing.T) {
			var sttCalls, llmCalls atomic.Int32
			s, d, _ := specServer(t, specSTT(func(ctx context.Context, _ stt.Request) (*stt.Result, error) {
				n := sttCalls.Add(1)
				if n == 1 {
					switch failure {
					case "stt":
						return nil, errors.New("speculative failure")
					case "timeout_after_commit":
						<-ctx.Done()
						return nil, ctx.Err()
					}
				}
				return &stt.Result{Text: "private transcript"}, nil
			}), specLLM(func(context.Context, llm.Request) (llm.Stream, error) {
				n := llmCalls.Add(1)
				if n == 1 && failure == "llm" {
					return nil, errors.New("speculative failure")
				}
				return &answerStream{}, nil
			}))
			cfg := realtime.DefaultSpeculationConfig()
			if failure == "overflow" {
				cfg.MaxDeltaBytes = 2
			}
			if failure == "timeout_after_commit" {
				cfg.Timeout = 80 * time.Millisecond
			}
			if err := s.SetSpeculationConfig(cfg); err != nil {
				t.Fatal(err)
			}
			c, ctx := connectTest(t, s)
			specSpeak(t, c, ctx)
			if failure == "timeout_after_commit" {
				readSpecEvent(t, c, ctx, "speculation.started", "")
				close(d.release)
			} else {
				readSpecEvent(t, c, ctx, "speculation.fallback", "")
				close(d.release)
			}
			readType(t, c, ctx, "input_audio.transcript.final")
			readType(t, c, ctx, "generation.created")
			readType(t, c, ctx, "response.text.done")
			if sttCalls.Load() != 2 {
				t.Fatalf("normal STT fallback calls=%d", sttCalls.Load())
			}
		})
	}
}
func TestSpeculationWireDisconnectCancelsLLM(t *testing.T) {
	cancelled := make(chan struct{})
	started := make(chan struct{})
	s, _, _ := specServer(t, specSTT(func(context.Context, stt.Request) (*stt.Result, error) {
		return &stt.Result{Text: "private transcript"}, nil
	}), specLLM(func(ctx context.Context, _ llm.Request) (llm.Stream, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}))
	c, ctx := connectTest(t, s)
	specSpeak(t, c, ctx)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("LLM not started")
	}
	c.CloseNow()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("LLM survived disconnect")
	}
}

type delayedErrorStream struct {
	ctx     context.Context
	release <-chan struct{}
}

func (r delayedErrorStream) Recv() (llm.Delta, error) {
	select {
	case <-r.release:
		return llm.Delta{}, errors.New("stream failed")
	case <-r.ctx.Done():
		return llm.Delta{}, r.ctx.Err()
	}
}
func (delayedErrorStream) Close() error { return nil }
func TestPromotedStreamFailureBeforeOutputRetriesNormalLLM(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	s, d, _ := specServer(t, specSTT(func(context.Context, stt.Request) (*stt.Result, error) {
		return &stt.Result{Text: "private transcript"}, nil
	}), specLLM(func(ctx context.Context, _ llm.Request) (llm.Stream, error) {
		if calls.Add(1) == 1 {
			return delayedErrorStream{ctx, release}, nil
		}
		return &answerStream{}, nil
	}))
	c, ctx := connectTest(t, s)
	specSpeak(t, c, ctx)
	readSpecEvent(t, c, ctx, "speculation.ready", "stt")
	close(d.release)
	readType(t, c, ctx, "generation.created")
	close(release)
	readType(t, c, ctx, "speculation.fallback")
	readType(t, c, ctx, "response.text.done")
	if calls.Load() != 2 {
		t.Fatal("normal LLM retry missing")
	}
}
