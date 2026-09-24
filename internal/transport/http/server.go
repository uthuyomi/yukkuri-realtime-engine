package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/conversation"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel/multisignal"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt/limited"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

type Server struct {
	rootContext         context.Context
	rootCancel          context.CancelFunc
	connections         chan struct{}
	allowedOrigins      []string
	localOrigins        bool
	engine              *engine.Engine
	sttProvider         stt.Provider
	speculativeSTT      *limited.Provider
	speculationConfig   realtime.SpeculationConfig
	llmProvider         llm.Provider
	turnProvider        turndetection.Provider
	endpointConfig      realtime.EndpointConfig
	backchannelProvider backchannel.Provider
	interruptionConfig  realtime.InterruptionConfig
	server              *http.Server
	conversationConfig  conversation.Config
}

// Configure before ListenAndServe; existing text/TTS clients do not need this.
func (s *Server) SetTurnDetector(p turndetection.Provider, c realtime.EndpointConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("turn detector is nil")
	}
	s.turnProvider, s.endpointConfig = p, c
	return nil
}

type Config struct {
	Address             string
	AllowedOrigins      []string
	DisableLocalOrigins bool
}

func New(config Config, e *engine.Engine) *Server {
	mux := http.NewServeMux()
	rootContext, rootCancel := context.WithCancel(context.Background())

	s := &Server{
		engine:      e,
		rootContext: rootContext, rootCancel: rootCancel, connections: make(chan struct{}, protocol.MaxSessions), allowedOrigins: append([]string(nil), config.AllowedOrigins...), localOrigins: !config.DisableLocalOrigins,
		endpointConfig:      realtime.DefaultEndpointConfig(),
		backchannelProvider: &multisignal.Policy{AllowAcousticRecovery: true},
		interruptionConfig:  realtime.DefaultInterruptionConfig(),
		speculationConfig:   realtime.DefaultSpeculationConfig(),
		conversationConfig:  conversation.DefaultConfig(),
	}

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /v1/capabilities", s.handleCapabilities)
	mux.HandleFunc("GET /v1/transcription", s.handleTranscription)
	mux.HandleFunc("POST /v1/audio/speech", s.handleSpeech)
	mux.HandleFunc("GET /v1/realtime", s.handleRealtime)

	s.server = &http.Server{
		Addr:              config.Address,
		Handler:           s.publicMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s
}

func (s *Server) SetBackchannelProvider(p backchannel.Provider, c realtime.InterruptionConfig) error {
	if p == nil || c.DecisionWindow < 300*time.Millisecond || c.DecisionWindow > 5*time.Second {
		return fmt.Errorf("invalid interruption configuration")
	}
	s.backchannelProvider = p
	s.interruptionConfig = c
	return nil
}

func (s *Server) SetSTTProvider(
	provider stt.Provider,
) {
	if provider == nil {
		s.sttProvider = nil
		s.speculativeSTT = nil
		return
	}
	// One shared process budget for legacy, committed and speculative requests.
	concurrency := protocol.MaxSTT
	if n := stt.Describe(provider).Concurrency; n > 0 && n < concurrency {
		concurrency = n
	}
	s.speculativeSTT = limited.New(provider, concurrency)
	s.sttProvider = s.speculativeSTT
}

func (s *Server) SetSpeculationConfig(c realtime.SpeculationConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.speculationConfig = c
	return nil
}

// Trusted server configuration only; public clients cannot supply system roles.
func (s *Server) SetConversationConfig(c conversation.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.conversationConfig = c
	return nil
}

func (s *Server) SetLLMProvider(
	provider llm.Provider,
) {
	s.llmProvider = provider
}

func (s *Server) ListenAndServe() error {
	log.Printf("HTTP server listening on %s", s.server.Addr)

	err := s.server.ListenAndServe()

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.rootCancel()
	err := s.server.Shutdown(ctx)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for len(s.connections) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return err
}

type speechRequest struct {
	Provider string  `json:"provider"`
	Text     string  `json:"text"`
	Voice    string  `json:"voice"`
	Speed    float64 `json:"speed"`
}

func (s *Server) handleHealth(
	w http.ResponseWriter,
	r *http.Request,
) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func (s *Server) handleSpeech(
	w http.ResponseWriter,
	r *http.Request,
) {
	defer r.Body.Close()

	decoder := json.NewDecoder(
		http.MaxBytesReader(w, r.Body, protocol.MaxHTTPBytes),
	)
	decoder.DisallowUnknownFields()

	var request speechRequest

	if err := decoder.Decode(&request); err != nil {
		code := "invalid_request"
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			code = "payload_too_large"
		}
		status := 400
		if code == "payload_too_large" {
			status = 413
		}
		writePublicError(w, status, protocol.Error(code))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writePublicError(w, 413, protocol.Error("payload_too_large"))
		} else {
			writePublicError(w, 400, protocol.Error("invalid_request"))
		}
		return
	}
	if strings.TrimSpace(request.Text) == "" || request.Speed < 0 {
		writePublicError(w, 400, protocol.Error("invalid_request"))
		return
	}
	if len(request.Text) > protocol.MaxTextBytes {
		writePublicError(w, 413, protocol.Error("payload_too_large"))
		return
	}
	if !s.engine.HasTTS(request.Provider) {
		writePublicError(w, 503, protocol.Error("provider_unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), protocol.ProviderTimeout)
	defer cancel()

	stream, err := s.engine.Synthesize(
		ctx,
		request.Provider,
		tts.Request{
			Text:  request.Text,
			Voice: request.Voice,
			Speed: request.Speed,
		},
	)
	if stream != nil && stream.Audio != nil {
		defer stream.Audio.Close()
	}
	if err != nil || stream == nil || stream.Audio == nil || ctx.Err() != nil {
		log.Printf("TTS failed: request=%s", w.Header().Get("X-Request-ID"))
		code := "generation_failed"
		status := 502
		if ctx.Err() == context.DeadlineExceeded {
			code = "timeout"
			status = 504
		}
		writePublicError(w, status, protocol.Error(code))
		return
	}

	switch stream.Format.Codec {
	case "wav":
		w.Header().Set("Content-Type", "audio/wav")

	default:
		w.Header().Set(
			"Content-Type",
			"application/octet-stream",
		)
	}

	w.Header().Set(
		"X-Audio-Sample-Rate",
		fmt.Sprintf("%d", stream.Format.SampleRate),
	)

	w.Header().Set(
		"X-Audio-Channels",
		fmt.Sprintf("%d", stream.Format.Channels),
	)

	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, stream.Audio); err != nil {
		log.Printf("speech stream failed: request=%s error_type=%T", w.Header().Get("X-Request-ID"), err)
	}
}

func writeJSON(
	w http.ResponseWriter,
	status int,
	value any,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("failed to encode JSON response: %v", err)
	}
}
