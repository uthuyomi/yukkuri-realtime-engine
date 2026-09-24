package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
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
	Address string
}

func New(config Config, e *engine.Engine) *Server {
	mux := http.NewServeMux()

	s := &Server{
		engine:              e,
		backchannelProvider: &multisignal.Policy{AllowAcousticRecovery: true},
		interruptionConfig:  realtime.DefaultInterruptionConfig(),
		speculationConfig:   realtime.DefaultSpeculationConfig(),
	}

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /v1/audio/speech", s.handleSpeech)
	mux.HandleFunc("GET /v1/realtime", s.handleRealtime)

	s.server = &http.Server{
		Addr:              config.Address,
		Handler:           mux,
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
	s.speculativeSTT = limited.New(provider, 2)
	s.sttProvider = s.speculativeSTT
}

func (s *Server) SetSpeculationConfig(c realtime.SpeculationConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.speculationConfig = c
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
	return s.server.Shutdown(ctx)
}

type speechRequest struct {
	Provider string  `json:"provider"`
	Text     string  `json:"text"`
	Voice    string  `json:"voice"`
	Speed    float64 `json:"speed"`
}

type errorResponse struct {
	Error string `json:"error"`
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
		io.LimitReader(r.Body, 1<<20),
	)
	decoder.DisallowUnknownFields()

	var request speechRequest

	if err := decoder.Decode(&request); err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			fmt.Sprintf("invalid request: %v", err),
		)
		return
	}

	if request.Text == "" {
		writeError(
			w,
			http.StatusBadRequest,
			"text is required",
		)
		return
	}

	stream, err := s.engine.Synthesize(
		r.Context(),
		request.Provider,
		tts.Request{
			Text:  request.Text,
			Voice: request.Voice,
			Speed: request.Speed,
		},
	)
	if err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			err.Error(),
		)
		return
	}
	defer stream.Audio.Close()

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
		log.Printf("failed to stream speech response: %v", err)
	}
}

func writeError(
	w http.ResponseWriter,
	status int,
	message string,
) {
	writeJSON(w, status, errorResponse{
		Error: message,
	})
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
