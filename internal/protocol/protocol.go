// Package protocol contains the public v1 wire contract, not runtime states.
package protocol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const Version = "1"
const (
	MaxJSONBytes         = 64 * 1024
	MaxBinaryBytes       = 64 * 1024
	MaxOutputBinaryBytes = 16 * 1024
	MaxHTTPBytes         = 1 * 1024 * 1024
	MaxTextBytes         = 32 * 1024
	MaxTranscriptBytes   = 16 * 1024
	MaxTurnSeconds       = 120
	MaxInputBytes        = MaxTurnSeconds * 16000 * 2
	MaxIDBytes           = 128
	MaxSessions          = 64
	MaxSTT               = 2
	MaxWorkers           = 16
	WriteTimeout         = 10 * time.Second
	CleanupTimeout       = 2 * time.Second
	ProviderTimeout      = 2 * time.Minute
)

type Event struct {
	Type           string          `json:"type"`
	SessionID      string          `json:"session_id,omitempty"`
	EventID        string          `json:"event_id,omitempty"`
	RelatedEventID string          `json:"related_event_id,omitempty"`
	Generation     string          `json:"generation_id,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
	Data           json.RawMessage `json:"data"`
}

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random ID source unavailable")
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
func NewEvent(kind, session, generation string, data any) (Event, error) {
	raw := json.RawMessage(`{}`)
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Event{}, err
		}
		raw = b
	}
	return Event{Type: kind, SessionID: session, Generation: generation, EventID: NewID("evt"), Timestamp: time.Now().UTC(), Data: raw}, nil
}

type PublicError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
	LegacyCode  string `json:"legacy_code,omitempty"`
}

func (e *PublicError) Error() string { return e.Code }

var Messages = map[string]string{
	"invalid_request":        "Invalid request or event data.",
	"invalid_state":          "This operation is not valid in the current state.",
	"unsupported_format":     "Use mono PCM s16le at 16000 Hz for input.",
	"unsupported_capability": "The requested protocol or capability is not supported.",
	"payload_too_large":      "The payload exceeds the published limit.",
	"resource_limit":         "A configured resource limit has been reached.",
	"provider_unavailable":   "The requested service is not configured.",
	"transcription_failed":   "Transcription failed.",
	"generation_failed":      "Response generation failed.",
	"audio_flow_error":       "Invalid audio flow control state or credit.",
	"timeout":                "The operation timed out.",
	"internal_error":         "The operation could not be completed.",
	"origin_rejected":        "The request origin is not allowed.",
}

func Error(code string) *PublicError {
	message, ok := Messages[code]
	if !ok {
		code = "internal_error"
		message = Messages[code]
	}
	return &PublicError{Code: code, Message: message, Recoverable: true}
}

// Legacy aliases remain available in legacy_code; raw errors never become wire messages.
func LegacyError(code string) *PublicError {
	category := code
	switch code {
	case "invalid_event", "invalid_generation", "invalid_output", "invalid_text_delta", "invalid_text_input", "unknown_event", "empty_input_audio":
		category = "invalid_request"
	case "invalid_input_audio", "invalid_interruption", "invalid_vad_event", "input_conflict", "input_audio_not_started", "server_endpointing", "generation_not_active":
		category = "invalid_state"
	case "stt_not_configured", "llm_not_configured", "tts_not_configured":
		category = "provider_unavailable"
	case "stt_failed":
		category = "transcription_failed"
	case "llm_failed", "llm_stream_failed", "synthesis_failed":
		category = "generation_failed"
	case "text_backpressure", "conversation_rejected":
		category = "resource_limit"
	}
	e := Error(category)
	if category != code {
		e.LegacyCode = code
	}
	return e
}
func FromError(err error, fallback string) *PublicError {
	var e *PublicError
	if errors.As(err, &e) {
		safe := Error(e.Code)
		safe.Recoverable = e.Recoverable
		if e.LegacyCode != "" {
			alias := LegacyError(e.LegacyCode)
			if alias.Code == safe.Code {
				safe.LegacyCode = alias.LegacyCode
			}
		}
		return safe
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Error("timeout")
	}
	return Error(fallback)
}

type AudioFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate,omitempty"`
	Channels   int    `json:"channels"`
}

func ValidateInput(rate, channels int, encoding string) error {
	if rate != 16000 || channels != 1 || encoding != "pcm_s16le" {
		return Error("unsupported_format")
	}
	return nil
}

type Capability struct {
	Version   string   `json:"version"`
	Available bool     `json:"available"`
	Modes     []string `json:"modes,omitempty"`
}
type Capabilities struct {
	ProtocolVersion string                `json:"protocol_version"`
	Endpoints       map[string]string     `json:"endpoints"`
	Features        map[string]Capability `json:"features"`
	InputFormats    []AudioFormat         `json:"input_audio_formats"`
	OutputFormats   []AudioFormat         `json:"output_audio_formats"`
	Limits          map[string]int64      `json:"limits"`
}

type SessionConfig struct {
	ProtocolVersion  string `json:"protocol_version"`
	AudioFlowControl string `json:"audio_flow_control,omitempty"`
}

// InputAudioFormatData is shared by realtime manual input and transcription.
type InputAudioFormatData struct {
	Mode       string `json:"mode,omitempty"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Encoding   string `json:"encoding"`
}
