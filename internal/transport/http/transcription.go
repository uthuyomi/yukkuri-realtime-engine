package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

// Shared provider boundary. No conversation, generation or LLM is required.
func (s *Server) transcribe(ctx context.Context, pcm []byte, format protocol.InputAudioFormatData) (*stt.Result, error) {
	if s.sttProvider == nil {
		return nil, protocol.Error("provider_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, protocol.ProviderTimeout)
	defer cancel()
	result, err := s.sttProvider.Transcribe(ctx, stt.Request{Audio: pcm, Format: stt.AudioFormat{SampleRate: format.SampleRate, Channels: format.Channels, Encoding: format.Encoding}})
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, protocol.Error("timeout")
		}
		return nil, context.Canceled
	}
	if err != nil || result == nil {
		return nil, protocol.Error("transcription_failed")
	}
	if len(result.Text) > protocol.MaxTranscriptBytes {
		return nil, protocol.Error("resource_limit")
	}
	if !utf8.ValidString(result.Text) {
		return nil, protocol.Error("transcription_failed")
	}
	copy := *result
	if len(copy.Language) > 32 || strings.ContainsAny(copy.Language, `\/: `) {
		copy.Language = ""
	}
	return &copy, nil
}

type transcriptionJob struct {
	ctx    context.Context
	cancel context.CancelFunc
	turn   string
}

func (s *Server) handleTranscription(w http.ResponseWriter, r *http.Request) {
	conn, parent, release, ok := s.acceptPublic(w, r)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	id := protocol.NewID("sess")
	writer := newRealtimeWriter(conn)
	writer.session = id
	var mu sync.Mutex
	var active *transcriptionJob
	var input bytes.Buffer
	var format protocol.InputAudioFormatData
	collecting := false
	cleaned := false
	cleanup := func() bool {
		if cleaned {
			return true
		}
		cleaned = true
		cancel()
		c, stop := context.WithTimeout(context.Background(), protocol.CleanupTimeout)
		defer stop()
		return writer.wait(c)
	}
	defer cleanup()
	send := func(w *realtimeWriter, kind string, data any) error {
		e, _ := protocol.NewEvent(kind, id, "", data)
		return w.Event(ctx, e)
	}
	if send(writer, "session.created", map[string]any{"protocol_version": protocol.Version, "request_id": w.Header().Get("X-Request-ID"), "connection_type": "transcription", "capabilities": s.capabilities()}) != nil {
		return
	}
	for {
		kind, payload, err := readPublicMessage(ctx, conn)
		if err != nil {
			if e, ok := err.(*protocol.PublicError); ok {
				e.Recoverable = false
				_ = emitError(ctx, writer, id, "", e)
				closeSocketCode(conn, websocket.StatusMessageTooBig, "payload limit")
			}
			return
		}
		if kind == websocket.MessageBinary {
			code := ""
			if !collecting {
				code = "invalid_state"
			} else if err := audio.AppendPCM16(&input, payload, protocol.MaxInputBytes); err != nil {
				code = "invalid_request"
				if errors.Is(err, audio.ErrInputLimit) {
					code = "payload_too_large"
					input = bytes.Buffer{}
					collecting = false
				}
			}
			if code != "" {
				_ = emitError(ctx, writer, id, "", protocol.Error(code))
			}
			continue
		}
		event, err := protocol.DecodeClient(payload)
		related := event.EventID
		if !protocol.ValidID(related) {
			related = ""
		}
		requestWriter := writer.withRelated(related)
		if err != nil {
			_ = emitError(ctx, requestWriter, id, "", protocol.FromError(err, "invalid_request"))
			continue
		}
		if event.SessionID != "" && event.SessionID != id {
			_ = emitError(ctx, requestWriter, id, "", protocol.Error("invalid_state"))
			continue
		}
		var publicErr error
		switch event.Type {
		case "ping":
			_ = send(requestWriter, "pong", nil)
		case "session.configure":
			publicErr = configurePublic(ctx, requestWriter, id, event, nil)
		case "session.close":
			complete := cleanup()
			e, _ := protocol.NewEvent("session.closed", id, "", map[string]bool{"cleanup_complete": complete})
			closeCtx, stop := context.WithTimeout(context.Background(), protocol.CleanupTimeout)
			_ = requestWriter.Event(closeCtx, e)
			stop()
			closeSocket(conn)
			return
		case "input_audio.start":
			var config protocol.InputAudioFormatData
			_ = json.Unmarshal(event.Data, &config)
			mu.Lock()
			busy := active != nil
			mu.Unlock()
			if busy {
				publicErr = protocol.Error("resource_limit")
			} else if collecting {
				publicErr = protocol.Error("invalid_state")
			} else if s.sttProvider == nil {
				publicErr = protocol.Error("provider_unavailable")
			} else if config.Mode != "" && config.Mode != "manual" {
				publicErr = protocol.Error("unsupported_capability")
			} else if err := protocol.ValidateInput(config.SampleRate, config.Channels, config.Encoding); err != nil {
				publicErr = err
			} else {
				format = config
				collecting = true
				input = bytes.Buffer{}
				_ = send(requestWriter, "input_audio.started", config)
			}
		case "input_audio.cancel", "input_audio.stop":
			input = bytes.Buffer{}
			collecting = false
			turn := ""
			mu.Lock()
			if active != nil {
				active.cancel()
				turn = active.turn
			}
			mu.Unlock()
			_ = send(requestWriter, "input_audio.cancelled", map[string]string{"turn_id": turn})
		case "input_audio.commit":
			if !collecting {
				publicErr = protocol.Error("invalid_state")
				break
			}
			if input.Len() == 0 {
				publicErr = protocol.Error("invalid_request")
				break
			}
			audioData := input.Bytes()
			input = bytes.Buffer{}
			collecting = false
			jobCtx, jobCancel := context.WithCancel(ctx)
			job := &transcriptionJob{ctx: jobCtx, cancel: jobCancel, turn: protocol.NewID("turn")}
			mu.Lock()
			active = job
			mu.Unlock()
			_ = send(requestWriter, "input_audio.committed", map[string]any{"turn_id": job.turn, "bytes": len(audioData)})
			committedFormat := format
			writer.Go(jobCtx, func() {
				defer jobCancel()
				defer func() {
					mu.Lock()
					if active == job {
						active = nil
					}
					mu.Unlock()
				}()
				result, err := s.transcribe(jobCtx, audioData, committedFormat)
				if jobCtx.Err() != nil {
					return
				}
				if err != nil {
					log.Printf("transcription failed: session=%s turn=%s related_event=%s", id, job.turn, related)
					_ = emitError(jobCtx, requestWriter, id, "", protocol.FromError(err, "transcription_failed"))
					return
				}
				e, _ := protocol.NewEvent("input_audio.transcript.final", id, "", map[string]string{"turn_id": job.turn, "text": result.Text, "language": result.Language})
				_ = requestWriter.Event(jobCtx, e)
			})
		default:
			publicErr = protocol.Error("unsupported_capability")
		}
		if publicErr != nil {
			_ = emitError(ctx, requestWriter, id, "", protocol.FromError(publicErr, "invalid_request"))
		}
	}
}
