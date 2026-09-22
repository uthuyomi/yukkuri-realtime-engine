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
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
)

type Server struct {
	engine *engine.Engine
	server *http.Server
}

type Config struct {
	Address string
}

func New(config Config, e *engine.Engine) *Server {
	mux := http.NewServeMux()

	s := &Server{
		engine: e,
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
