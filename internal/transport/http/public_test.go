package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

func connectPublic(t *testing.T, s *Server, path string) (*websocket.Conn, context.Context, protocol.Event) {
	t.Helper()
	srv := httptest.NewServer(s.server.Handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c, ctx, publicEvent(t, c, ctx, "session.created")
}
func publicEvent(t *testing.T, c *websocket.Conn, ctx context.Context, kind string) protocol.Event {
	t.Helper()
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var e protocol.Event
		if json.Unmarshal(b, &e) != nil {
			t.Fatal(string(b))
		}
		if e.EventID == "" || e.SessionID == "" || e.Timestamp.IsZero() || len(e.Data) == 0 {
			t.Fatal("invalid envelope", string(b))
		}
		if e.Type == kind {
			return e
		}
		if e.Type == "error" {
			t.Fatal("unexpected error", string(e.Data))
		}
	}
}
func publicError(t *testing.T, c *websocket.Conn, ctx context.Context, code string) protocol.Event {
	t.Helper()
	e := publicEvent(t, c, ctx, "error")
	var p protocol.PublicError
	json.Unmarshal(e.Data, &p)
	if p.Code != code || p.Message == "" {
		t.Fatal(string(e.Data))
	}
	return e
}
func inputConfig() realtime.InputAudioFormatData {
	return realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}
}
func sendID(t *testing.T, c *websocket.Conn, ctx context.Context, kind, id string, data any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"type": kind, "event_id": id, "data": data})
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func TestPublicVersionCapabilitiesAndCorrelation(t *testing.T) {
	for _, path := range []string{"/v1/realtime", "/v1/transcription"} {
		t.Run(path, func(t *testing.T) {
			c, ctx, created := connectPublic(t, New(Config{}, engine.New()), path)
			var data struct {
				Version      string                `json:"protocol_version"`
				Capabilities protocol.Capabilities `json:"capabilities"`
			}
			json.Unmarshal(created.Data, &data)
			if data.Version != "1" || data.Capabilities.Features["transcription"].Available || data.Capabilities.Features["audio_flow_control"].Version != "credit-v1" {
				t.Fatal(string(created.Data))
			}
			sendID(t, c, ctx, "ping", "ping_1", nil)
			pong := publicEvent(t, c, ctx, "pong")
			if pong.RelatedEventID != "ping_1" || pong.EventID == created.EventID {
				t.Fatal(pong)
			}
			sendID(t, c, ctx, "session.configure", "version_1", map[string]string{"protocol_version": "2"})
			publicError(t, c, ctx, "unsupported_capability")
			sendID(t, c, ctx, "session.configure", "version_2", map[string]string{"protocol_version": "1"})
			if e := publicEvent(t, c, ctx, "session.configured"); e.RelatedEventID != "version_2" {
				t.Fatal(e)
			}
		})
	}
}
func TestPublicMalformedUnknownStateAndLimits(t *testing.T) {
	for _, tc := range []struct{ name, payload, code string }{
		{"malformed", "{", "invalid_request"}, {"unknown", `{"type":"future.event","event_id":"bad_1"}`, "invalid_request"},
		{"state", `{"type":"response.text.done"}`, "invalid_state"},
		{"format", `{"type":"input_audio.start","data":{"sample_rate":8000,"channels":1,"encoding":"pcm_s16le"}}`, "unsupported_format"},
		{"credit", `{"type":"playback.configure","data":{"flow_control":"future"}}`, "unsupported_capability"},
		{"large_json", strings.Repeat(" ", protocol.MaxJSONBytes+1), "payload_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ctx, _ := connectPublic(t, New(Config{}, engine.New()), "/v1/realtime")
			if err := c.Write(ctx, websocket.MessageText, []byte(tc.payload)); err != nil {
				t.Fatal(err)
			}
			e := publicError(t, c, ctx, tc.code)
			if tc.name == "unknown" && e.RelatedEventID != "bad_1" {
				t.Fatal(e)
			}
			if tc.name != "large_json" {
				sendEvent(t, c, ctx, "ping", nil)
				publicEvent(t, c, ctx, "pong")
			}
		})
	}
	for _, path := range []string{"/v1/realtime", "/v1/transcription"} {
		t.Run("binary"+path, func(t *testing.T) {
			c, ctx, _ := connectPublic(t, New(Config{}, engine.New()), path)
			if err := c.Write(ctx, websocket.MessageBinary, make([]byte, protocol.MaxBinaryBytes+1)); err != nil {
				t.Fatal(err)
			}
			publicError(t, c, ctx, "payload_too_large")
		})
	}
}

func TestTranscriptionOnlyFinalAndValidation(t *testing.T) {
	s := New(Config{}, engine.New())
	requests := make(chan stt.Request, 1)
	s.SetSTTProvider(&testSTT{calls: requests, text: "recognized"})
	c, ctx, _ := connectPublic(t, s, "/v1/transcription")
	sendEvent(t, c, ctx, "input_audio.start", map[string]any{"sample_rate": 8000, "channels": 1, "encoding": "pcm_s16le"})
	publicError(t, c, ctx, "unsupported_format")
	sendEvent(t, c, ctx, "input_audio.start", inputConfig())
	publicEvent(t, c, ctx, "input_audio.started")
	sendEvent(t, c, ctx, "input_audio.commit", nil)
	publicError(t, c, ctx, "invalid_request")
	if err := c.Write(ctx, websocket.MessageBinary, []byte{1}); err != nil {
		t.Fatal(err)
	}
	publicError(t, c, ctx, "invalid_request")
	pcm := []byte{1, 0, 2, 0}
	if err := c.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		t.Fatal(err)
	}
	sendID(t, c, ctx, "input_audio.commit", "commit_1", nil)
	publicEvent(t, c, ctx, "input_audio.committed")
	final := publicEvent(t, c, ctx, "input_audio.transcript.final")
	if final.RelatedEventID != "commit_1" || !strings.Contains(string(final.Data), "recognized") {
		t.Fatal(final)
	}
	req := <-requests
	if !bytes.Equal(req.Audio, pcm) || req.Format.SampleRate != 16000 {
		t.Fatal(req)
	}
	sendEvent(t, c, ctx, "generation.create", nil)
	publicError(t, c, ctx, "unsupported_capability")
}
func TestTranscriptionCancellationAndCleanup(t *testing.T) {
	for _, action := range []string{"input_audio.cancel", "disconnect", "session.close"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{})
			stopped := make(chan struct{})
			s := New(Config{}, engine.New())
			s.SetSTTProvider(specSTT(func(ctx context.Context, _ stt.Request) (*stt.Result, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return nil, ctx.Err()
			}))
			c, ctx, _ := connectPublic(t, s, "/v1/transcription")
			sendEvent(t, c, ctx, "input_audio.start", inputConfig())
			publicEvent(t, c, ctx, "input_audio.started")
			c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
			sendEvent(t, c, ctx, "input_audio.commit", nil)
			publicEvent(t, c, ctx, "input_audio.committed")
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("STT not started")
			}
			if action == "disconnect" {
				c.CloseNow()
			} else {
				sendID(t, c, ctx, action, "close_1", nil)
				kind := "input_audio.cancelled"
				if action == "session.close" {
					kind = "session.closed"
				}
				e := publicEvent(t, c, ctx, kind)
				if e.RelatedEventID != "close_1" {
					t.Fatal(e)
				}
				if action == "session.close" && !strings.Contains(string(e.Data), `"cleanup_complete":true`) {
					t.Fatal(string(e.Data))
				}
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("STT worker survived cancellation")
			}
		})
	}
}

const privateFailure = `private-secret sk-do-not-expose C:\proprietary\model.bin raw-provider-response`

type failingTTS struct{}

func (failingTTS) Name() string { return "test" }
func (failingTTS) Synthesize(context.Context, tts.Request) (*tts.Stream, error) {
	return nil, errors.New(privateFailure)
}
func TestPublicProviderErrorPrivacy(t *testing.T) {
	s := New(Config{}, engine.New())
	s.SetSTTProvider(specSTT(func(context.Context, stt.Request) (*stt.Result, error) { return nil, errors.New(privateFailure) }))
	c, ctx, _ := connectPublic(t, s, "/v1/transcription")
	sendEvent(t, c, ctx, "input_audio.start", inputConfig())
	publicEvent(t, c, ctx, "input_audio.started")
	c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
	sendID(t, c, ctx, "input_audio.commit", "secret_error", nil)
	e := publicError(t, c, ctx, "transcription_failed")
	if strings.Contains(string(e.Data), "private") || e.RelatedEventID != "secret_error" {
		t.Fatal(e)
	}
	eng := engine.New()
	eng.RegisterTTS(failingTTS{})
	s = New(Config{}, eng)
	c, ctx, _ = connectPublic(t, s, "/v1/realtime")
	sendEvent(t, c, ctx, "generation.create", nil)
	publicEvent(t, c, ctx, "generation.created")
	sendEvent(t, c, ctx, "response.text.delta", realtime.TextDeltaData{Text: "hello"})
	sendEvent(t, c, ctx, "response.text.done", nil)
	e = publicError(t, c, ctx, "generation_failed")
	if strings.Contains(string(e.Data), "private") {
		t.Fatal(e)
	}
}
func TestHTTPPublicSurfaceErrorsIDsAndOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, origin, body string
		provider           tts.Provider
		status             int
		code               string
	}{
		{"success", "http://localhost:8123", `{"text":"hello"}`, testTTS{}, 200, ""},
		{"native", "", `{"text":"hello"}`, testTTS{}, 200, ""},
		{"rejected_origin", "https://untrusted.example", `{"text":"hello"}`, testTTS{}, 403, "origin_rejected"},
		{"null_origin", "null", `{"text":"hello"}`, testTTS{}, 403, "origin_rejected"},
		{"malformed", "", `{`, testTTS{}, 400, "invalid_request"},
		{"extra_json", "", `{"text":"x"} {}`, testTTS{}, 400, "invalid_request"},
		{"unavailable", "", `{"text":"x"}`, nil, 503, "provider_unavailable"},
		{"provider_failure", "", `{"text":"x"}`, failingTTS{}, 502, "generation_failed"},
		{"large", "", strings.Repeat(" ", protocol.MaxHTTPBytes+1), testTTS{}, 413, "payload_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := engine.New()
			if tc.provider != nil {
				eng.RegisterTTS(tc.provider)
			}
			s := New(Config{}, eng)
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(tc.body))
			r.Header.Set("Origin", tc.origin)
			s.server.Handler.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("X-Request-ID") == "" || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.code != "" {
				var e map[string]any
				json.Unmarshal(w.Body.Bytes(), &e)
				if e["code"] != tc.code || e["request_id"] != w.Header().Get("X-Request-ID") {
					t.Fatal(e)
				}
			}
			if w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("credential CORS")
			}
		})
	}
	s := New(Config{}, engine.New())
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/v1/capabilities", nil))
	var c protocol.Capabilities
	if json.Unmarshal(w.Body.Bytes(), &c) != nil || c.ProtocolVersion != "1" || c.Features["tts"].Available || c.Limits["json_bytes"] != protocol.MaxJSONBytes {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("OPTIONS", "/v1/audio/speech", nil)
	r.Header.Set("Origin", "http://localhost:8000")
	s.server.Handler.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:8000" {
		t.Fatal(w.Code, w.Header())
	}
}
func TestWebSocketOriginPolicy(t *testing.T) {
	s := New(Config{AllowedOrigins: []string{"https://trusted.example"}}, engine.New())
	srv := httptest.NewServer(s.server.Handler)
	defer srv.Close()
	for _, origin := range []string{"", "http://127.0.0.1:8000", "https://trusted.example", "https://evil.example", "null"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		c, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/realtime", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{origin}}})
		allowed := origin != "https://evil.example" && origin != "null"
		if allowed {
			if err != nil {
				t.Fatal(origin, err)
			}
			publicEvent(t, c, ctx, "session.created")
			c.CloseNow()
		} else {
			if err == nil || response.StatusCode != 403 {
				t.Fatal(origin, response, err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		cancel()
	}
}
func TestRealtimeGracefulCloseCancelsCreditWait(t *testing.T) {
	eng := engine.New()
	eng.RegisterTTS(longAudioTTS{})
	s := New(Config{}, eng)
	c, ctx, _ := connectPublic(t, s, "/v1/realtime")
	sendEvent(t, c, ctx, "session.configure", protocol.SessionConfig{ProtocolVersion: "1", AudioFlowControl: "credit-v1"})
	publicEvent(t, c, ctx, "session.configured")
	sendEvent(t, c, ctx, "generation.create", nil)
	publicEvent(t, c, ctx, "generation.created")
	sendEvent(t, c, ctx, "response.text.delta", realtime.TextDeltaData{Text: "pending audio"})
	sendEvent(t, c, ctx, "response.text.done", nil)
	publicEvent(t, c, ctx, "response.audio.chunk.started")
	sendID(t, c, ctx, "session.close", "close_credit", nil)
	e := publicEvent(t, c, ctx, "session.closed")
	if !strings.Contains(string(e.Data), `"cleanup_complete":true`) || e.RelatedEventID != "close_credit" {
		t.Fatal(e)
	}
}

func TestTranscriptionEmptyResultAndInputBudget(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty_final", true: "turn_limit"}[large], func(t *testing.T) {
			s := New(Config{}, engine.New())
			calls := make(chan stt.Request, 1)
			s.SetSTTProvider(&testSTT{calls: calls})
			c, ctx, _ := connectPublic(t, s, "/v1/transcription")
			sendEvent(t, c, ctx, "input_audio.start", inputConfig())
			publicEvent(t, c, ctx, "input_audio.started")
			if large {
				for n := 0; n < protocol.MaxInputBytes; n += 64000 {
					if err := c.Write(ctx, websocket.MessageBinary, make([]byte, 64000)); err != nil {
						t.Fatal(err)
					}
				}
				c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
				publicError(t, c, ctx, "payload_too_large")
				sendEvent(t, c, ctx, "input_audio.commit", nil)
				publicError(t, c, ctx, "invalid_state")
				select {
				case <-calls:
					t.Fatal("oversized turn reached STT")
				default:
				}
			} else {
				c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
				sendEvent(t, c, ctx, "input_audio.commit", nil)
				e := publicEvent(t, c, ctx, "input_audio.transcript.final")
				var data map[string]string
				json.Unmarshal(e.Data, &data)
				if data["text"] != "" {
					t.Fatal(data)
				}
			}
		})
	}
}

func TestTranscriptionSharesSTTAdmissionAndServerShutdown(t *testing.T) {
	entered := make(chan struct{}, 3)
	s := New(Config{}, engine.New())
	s.SetSTTProvider(specSTT(func(ctx context.Context, _ stt.Request) (*stt.Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	for i := 0; i < 3; i++ {
		c, ctx, _ := connectPublic(t, s, "/v1/transcription")
		sendEvent(t, c, ctx, "input_audio.start", inputConfig())
		publicEvent(t, c, ctx, "input_audio.started")
		c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
		sendEvent(t, c, ctx, "input_audio.commit", nil)
		publicEvent(t, c, ctx, "input_audio.committed")
		if i < 2 {
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("worker not admitted")
			}
		}
	}
	select {
	case <-entered:
		t.Fatal("exceeded shared STT budget")
	case <-time.After(30 * time.Millisecond):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.connections) != 0 {
		t.Fatal("websocket sessions survived shutdown")
	}
}

func TestPublicTextToAudioAndStaleCancel(t *testing.T) {
	eng := engine.New()
	eng.RegisterTTS(testTTS{})
	s := New(Config{}, eng)
	s.SetLLMProvider(specLLM(func(context.Context, llm.Request) (llm.Stream, error) { return &answerStream{}, nil }))
	c, ctx, _ := connectPublic(t, s, "/v1/realtime")
	sendID(t, c, ctx, "input_text.commit", "text_audio", map[string]string{"text": "hello", "output": "audio"})
	created := publicEvent(t, c, ctx, "generation.created")
	if created.RelatedEventID != "text_audio" {
		t.Fatal(created)
	}
	readAudioFrames(t, c, ctx, created.Generation, 0, 1)
	publicEvent(t, c, ctx, "generation.done")
	sendEvent(t, c, ctx, "generation.create", map[string]string{"output": "text"})
	next := publicEvent(t, c, ctx, "generation.created")
	sendScoped(t, c, ctx, "generation.cancel", created.Generation, nil)
	sendScoped(t, c, ctx, "response.text.delta", next.Generation, realtime.TextDeltaData{Text: "still active"})
	sendScoped(t, c, ctx, "response.text.done", next.Generation, nil)
	if done := publicEvent(t, c, ctx, "generation.done"); done.Generation != next.Generation {
		t.Fatal(done)
	}
}

func TestGracefulCloseBoundsNoncompliantProvider(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	s := New(Config{}, engine.New())
	s.SetSTTProvider(specSTT(func(context.Context, stt.Request) (*stt.Result, error) {
		close(started)
		<-release
		return &stt.Result{Text: "late"}, nil
	}))
	c, ctx, _ := connectPublic(t, s, "/v1/transcription")
	sendEvent(t, c, ctx, "input_audio.start", inputConfig())
	publicEvent(t, c, ctx, "input_audio.started")
	c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
	sendEvent(t, c, ctx, "input_audio.commit", nil)
	publicEvent(t, c, ctx, "input_audio.committed")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	sendEvent(t, c, ctx, "session.close", nil)
	e := publicEvent(t, c, ctx, "session.closed")
	if !strings.Contains(string(e.Data), `"cleanup_complete":false`) {
		t.Fatal("unbounded or inaccurate cleanup", string(e.Data))
	}
}

func TestPublicConnectionAndTranscriptLimits(t *testing.T) {
	s := New(Config{}, engine.New())
	for i := 0; i < protocol.MaxSessions; i++ {
		s.connections <- struct{}{}
	}
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/v1/realtime", nil))
	if w.Code != 429 || !strings.Contains(w.Body.String(), `"code":"resource_limit"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	s = New(Config{}, engine.New())
	s.SetSTTProvider(specSTT(func(context.Context, stt.Request) (*stt.Result, error) {
		return &stt.Result{Text: strings.Repeat("x", protocol.MaxTranscriptBytes+1)}, nil
	}))
	c, ctx, _ := connectPublic(t, s, "/v1/transcription")
	sendEvent(t, c, ctx, "input_audio.start", inputConfig())
	publicEvent(t, c, ctx, "input_audio.started")
	c.Write(ctx, websocket.MessageBinary, []byte{0, 0})
	sendEvent(t, c, ctx, "input_audio.commit", nil)
	publicError(t, c, ctx, "resource_limit")
}
