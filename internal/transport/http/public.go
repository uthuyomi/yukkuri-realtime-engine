package httptransport

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

func (s *Server) capabilities() protocol.Capabilities {
	info := stt.Describe(s.sttProvider)
	concurrency := info.Concurrency
	if concurrency < 1 {
		concurrency = protocol.MaxSTT
	}
	feature := func(ok bool, modes ...string) protocol.Capability {
		return protocol.Capability{Version: "1", Available: ok, Modes: modes}
	}
	transcription := feature(info.Available, "commit", "final-only")
	if info.Backend != "" {
		transcription.Runtime = &protocol.TranscriptionRuntime{Backend: info.Backend, RequestedDevice: info.RequestedDevice, SelectedDevice: info.SelectedDevice, Model: info.Model, Persistent: info.Persistent, State: info.State, FallbackFrom: info.FallbackFrom, FallbackReason: info.FallbackReason}
	}
	return protocol.Capabilities{ProtocolVersion: protocol.Version,
		Endpoints: map[string]string{"health": "GET /health", "speech": "POST /v1/audio/speech", "realtime": "WS /v1/realtime", "transcription": "WS /v1/transcription", "capabilities": "GET /v1/capabilities"},
		Features: map[string]protocol.Capability{
			"tts": feature(s.engine.HasTTS("")), "transcription": transcription,
			"conversation": feature(s.llmProvider != nil, "text", "audio-if-tts"), "realtime_audio": feature(s.engine.HasTTS("")),
			"realtime_input": feature(s.turnProvider != nil && info.Available),
			"interruption":   feature(s.turnProvider != nil), "backchannel": feature(s.turnProvider != nil && s.backchannelProvider != nil),
			"speculation":        feature(s.turnProvider != nil && info.Available && s.llmProvider != nil && s.speculationConfig.Enabled),
			"audio_flow_control": {Version: "credit-v1", Available: true, Modes: []string{"credit-v1", "legacy-bounded"}},
		}, InputFormats: []protocol.AudioFormat{{Encoding: "pcm_s16le", SampleRate: 16000, Channels: 1}},
		OutputFormats: []protocol.AudioFormat{{Encoding: "pcm_s16le", Channels: 1}, {Encoding: "wav", Channels: 1}},
		Limits:        map[string]int64{"json_bytes": protocol.MaxJSONBytes, "input_binary_bytes": protocol.MaxBinaryBytes, "output_binary_bytes": protocol.MaxOutputBinaryBytes, "http_body_bytes": protocol.MaxHTTPBytes, "text_bytes": protocol.MaxTextBytes, "transcript_bytes": protocol.MaxTranscriptBytes, "legacy_turn_seconds": protocol.MaxTurnSeconds, "realtime_turn_ms": s.endpointConfig.MaxTurnDuration.Milliseconds(), "conversation_items": int64(s.conversationConfig.MaxItems), "conversation_bytes": int64(s.conversationConfig.MaxBytes), "conversation_item_bytes": int64(s.conversationConfig.MaxItemBytes), "audio_queue_ms": 30000, "legacy_audio_window_ms": 2000, "concurrent_stt": int64(concurrency), "stt_admitted_requests": int64(info.QueueCapacity), "sessions": protocol.MaxSessions, "speculation_transcript_bytes": int64(s.speculationConfig.MaxTranscriptBytes), "speculation_delta_bytes": int64(s.speculationConfig.MaxDeltaBytes), "speculation_attempts_per_turn": int64(s.speculationConfig.MaxAttemptsPerTurn), "speculation_timeout_ms": s.speculationConfig.Timeout.Milliseconds(), "provider_timeout_ms": protocol.ProviderTimeout.Milliseconds(), "write_timeout_ms": protocol.WriteTimeout.Milliseconds(), "cleanup_timeout_ms": protocol.CleanupTimeout.Milliseconds(), "credit_wait_timeout_ms": 30000},
	}
}
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.capabilities())
}

func (s *Server) allowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, allowed := range s.allowedOrigins {
		if strings.TrimSpace(allowed) == origin {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return s.localOrigins && (host == "localhost" || (ip != nil && ip.IsLoopback()))
}
func (s *Server) publicMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", protocol.NewID("req"))
		w.Header().Add("Vary", "Origin")
		if !s.allowedOrigin(r) {
			writePublicError(w, 403, protocol.Error("origin_rejected"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, X-Audio-Sample-Rate, X-Audio-Channels")
		}
		routes := map[string]string{"/health": "GET", "/v1/capabilities": "GET", "/v1/audio/speech": "POST", "/v1/realtime": "GET", "/v1/transcription": "GET"}
		method, ok := routes[r.URL.Path]
		if !ok {
			writePublicError(w, 404, protocol.Error("invalid_request"))
			return
		}
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", method)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(204)
			return
		}
		if r.Method != method {
			w.Header().Set("Allow", method)
			writePublicError(w, 405, protocol.Error("invalid_request"))
			return
		}
		log.Printf("HTTP request: request=%s method=%s route=%s", w.Header().Get("X-Request-ID"), r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
func writePublicError(w http.ResponseWriter, status int, e *protocol.PublicError) {
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		id = protocol.NewID("req")
		w.Header().Set("X-Request-ID", id)
	}
	log.Printf("HTTP error: request=%s code=%s status=%d", id, e.Code, status)
	// Keep the old error string as a safe compatibility alias.
	writeJSON(w, status, map[string]any{"error": e.Message, "code": e.Code, "message": e.Message, "recoverable": e.Recoverable, "request_id": id})
}

// Read a complete logical WS message through a bounded reader. Fragmentation
// cannot bypass limits; the +1 byte permits a structured error before close.
func readPublicMessage(ctx context.Context, c *websocket.Conn) (websocket.MessageType, []byte, error) {
	kind, r, err := c.Reader(ctx)
	if err != nil {
		return kind, nil, err
	}
	limit := protocol.MaxJSONBytes
	if kind == websocket.MessageBinary {
		limit = protocol.MaxBinaryBytes
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if err != nil {
		return kind, nil, err
	}
	if len(b) > limit {
		return kind, nil, protocol.Error("payload_too_large")
	}
	return kind, b, nil
}
func (s *Server) acceptPublic(w http.ResponseWriter, r *http.Request) (*websocket.Conn, context.Context, func(), bool) {
	select {
	case s.connections <- struct{}{}:
	default:
		writePublicError(w, 429, protocol.Error("resource_limit"))
		return nil, nil, nil, false
	}
	if s.rootContext.Err() != nil {
		<-s.connections
		writePublicError(w, 503, protocol.Error("provider_unavailable"))
		return nil, nil, nil, false
	}
	// Origin is checked once, by the shared HTTP/WS middleware, including scheme.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		<-s.connections
		return nil, nil, nil, false
	}
	c.SetReadLimit(int64(max(protocol.MaxJSONBytes, protocol.MaxBinaryBytes) + 1))
	ctx, cancel := context.WithCancel(s.rootContext)
	return c, ctx, func() { cancel(); c.CloseNow(); <-s.connections }, true
}
func emitError(ctx context.Context, w *realtimeWriter, session, generation string, e *protocol.PublicError) error {
	event, err := protocol.NewEvent("error", session, generation, e)
	if err != nil {
		return err
	}
	return w.Event(ctx, event)
}
func configurePublic(ctx context.Context, w *realtimeWriter, id string, e protocol.Event, requireCredit func() error) error {
	var c protocol.SessionConfig
	if json.Unmarshal(e.Data, &c) != nil {
		return protocol.Error("invalid_request")
	}
	if c.ProtocolVersion != protocol.Version || (c.AudioFlowControl != "" && c.AudioFlowControl != "credit-v1") {
		return protocol.Error("unsupported_capability")
	}
	if c.AudioFlowControl != "" {
		if requireCredit == nil {
			return protocol.Error("unsupported_capability")
		}
		if err := requireCredit(); err != nil {
			return protocol.Error("invalid_state")
		}
	}
	result, _ := protocol.NewEvent("session.configured", id, "", c)
	return w.Event(ctx, result)
}
func closeSocket(c *websocket.Conn) {
	closeSocketCode(c, websocket.StatusNormalClosure, "session closed")
}
func closeSocketCode(c *websocket.Conn, code websocket.StatusCode, reason string) {
	// coder/websocket's close handshake is bounded internally; cap our wait too.
	timer := time.AfterFunc(protocol.CleanupTimeout, func() { c.CloseNow() })
	defer timer.Stop()
	_ = c.Close(code, reason)
}
