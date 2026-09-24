package httptransport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

type testSTT struct {
	calls chan stt.Request
	text  string
}

func (*testSTT) Name() string { return "test" }
func (p *testSTT) Transcribe(ctx context.Context, r stt.Request) (*stt.Result, error) {
	p.calls <- r
	return &stt.Result{Text: p.text}, nil
}

type testLLM struct{ calls chan llm.Request }

func (*testLLM) Name() string { return "test" }
func (p *testLLM) Generate(ctx context.Context, r llm.Request) (llm.Stream, error) {
	p.calls <- r
	return emptyLLMStream{}, nil
}

type emptyLLMStream struct{}

func (emptyLLMStream) Recv() (llm.Delta, error) { return llm.Delta{}, io.EOF }
func (emptyLLMStream) Close() error             { return nil }

type testDetector struct {
	entered chan struct{}
	release chan struct{}
}

func (*testDetector) Name() string               { return "test" }
func (*testDetector) AudioWindow() time.Duration { return time.Second }
func (p *testDetector) Detect(ctx context.Context, r turndetection.Request) (turndetection.Result, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
		return turndetection.Result{Complete: true}, nil
	case <-ctx.Done():
		return turndetection.Result{}, ctx.Err()
	}
}

type testTTS struct{}

func (testTTS) Name() string { return "test" }
func (testTTS) Synthesize(context.Context, tts.Request) (*tts.Stream, error) {
	b := make([]byte, 46)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 38)
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 8000)
	binary.LittleEndian.PutUint32(b[28:], 16000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], 2)
	return &tts.Stream{Format: tts.AudioFormat{Codec: "wav", SampleRate: 8000, Channels: 1}, Audio: io.NopCloser(bytes.NewReader(b))}, nil
}
func connectTest(t *testing.T, s *Server) (*websocket.Conn, context.Context) {
	t.Helper()
	srv := httptest.NewServer(s.server.Handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	readType(t, c, ctx, "session.created")
	return c, ctx
}
func sendEvent(t *testing.T, c *websocket.Conn, ctx context.Context, kind string, data any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": kind, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}
func readType(t *testing.T, c *websocket.Conn, ctx context.Context, kind string) realtime.Event {
	t.Helper()
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var e realtime.Event
		if err = json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "error" {
			t.Fatalf("server error: %s", e.Data)
		}
		if e.Type == kind {
			return e
		}
	}
}
func TestRealtimeEndpointAndBargeIn(t *testing.T) {
	s := New(Config{}, engine.New())
	// Preserve the non-speculative endpoint regression; speculation has its own
	// wire-level commit-barrier and cancellation tests.
	cfg := realtime.DefaultSpeculationConfig()
	cfg.Enabled = false
	if err := s.SetSpeculationConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := &testSTT{calls: make(chan stt.Request, 2), text: "こんにちは"}
	s.SetSTTProvider(st)
	lm := &testLLM{calls: make(chan llm.Request, 1)}
	s.SetLLMProvider(lm)
	d := &testDetector{entered: make(chan struct{}, 1), release: make(chan struct{})}
	if err := s.SetTurnDetector(d, realtime.EndpointConfig{MinDelay: 30 * time.Millisecond, MaxDelay: 2 * time.Second, MaxTurnDuration: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	c, ctx := connectTest(t, s)
	sendEvent(t, c, ctx, "generation.create", nil)
	readType(t, c, ctx, "generation.created")
	sendEvent(t, c, ctx, "input_audio.start", realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le", Mode: "realtime"})
	sendEvent(t, c, ctx, "input_audio.speech_start", nil)
	if err := c.Write(ctx, websocket.MessageBinary, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.calls:
		t.Fatal("STT during speech")
	default:
	}
	sendEvent(t, c, ctx, "input_audio.speech_end", nil)
	select {
	case <-d.entered:
	case <-ctx.Done():
		t.Fatal("detector not connected")
	}
	// A blocked turn detector must not block the generation cancellation ack.
	sendEvent(t, c, ctx, "generation.cancel", nil)
	readType(t, c, ctx, "generation.cancelled")
	select {
	case <-st.calls:
		t.Fatal("STT before detector completed")
	default:
	}
	close(d.release)
	select {
	case req := <-st.calls:
		if len(req.Audio) != 1024 {
			t.Fatal("incorrect STT audio")
		}
	case <-ctx.Done():
		t.Fatal("no STT after commit")
	}
	select {
	case req := <-lm.calls:
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "こんにちは" {
			t.Fatal("incorrect LLM input")
		}
	case <-ctx.Done():
		t.Fatal("no LLM after committed STT")
	}
}
func TestLegacyInputAndTextAndTTS(t *testing.T) {
	e := engine.New()
	if err := e.RegisterTTS(testTTS{}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{}, e)
	st := &testSTT{calls: make(chan stt.Request, 1)}
	s.SetSTTProvider(st)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"text":"test"}`))
	s.server.Handler.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.Len() != 46 || w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatal("TTS endpoint regression", w.Code)
	}
	c, ctx := connectTest(t, s)
	sendEvent(t, c, ctx, "generation.create", nil)
	readType(t, c, ctx, "generation.created")
	sendEvent(t, c, ctx, "response.text.delta", realtime.TextDeltaData{Text: "test"})
	sendEvent(t, c, ctx, "response.text.done", nil)
	readType(t, c, ctx, "response.audio.chunk.done")
	sendEvent(t, c, ctx, "input_audio.start", realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
	if err := c.Write(ctx, websocket.MessageBinary, []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	sendEvent(t, c, ctx, "input_audio.commit", nil)
	readType(t, c, ctx, "input_audio.transcript.final")
	select {
	case <-st.calls:
	default:
		t.Fatal("legacy STT not called")
	}
}
