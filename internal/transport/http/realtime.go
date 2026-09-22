package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

const realtimeAudioChunkSize = 16 * 1024

func (s *Server) handleRealtime(
	w http.ResponseWriter,
	r *http.Request,
) {
	conn, err := websocket.Accept(
		w,
		r,
		&websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		},
	)
	if err != nil {
		log.Printf(
			"realtime websocket accept failed: %v",
			err,
		)
		return
	}
	defer conn.CloseNow()

	session := realtime.NewSession(r.Context())
	defer session.Close()

	writer := newRealtimeWriter(conn)

	log.Printf(
		"realtime session connected: %s",
		session.ID(),
	)

	created, err := realtime.NewEvent(
		"session.created",
		session.ID(),
		"",
		map[string]any{
			"transport": "websocket",
		},
	)
	if err != nil {
		return
	}

	if err := writer.Event(
		session.Context(),
		created,
	); err != nil {
		return
	}

	for {
		_, payload, err := conn.Read(session.Context())
		if err != nil {
			var closeErr websocket.CloseError

			if errors.As(err, &closeErr) {
				log.Printf(
					"realtime session closed: %s code=%d",
					session.ID(),
					closeErr.Code,
				)
			} else {
				log.Printf(
					"realtime read failed: %s: %v",
					session.ID(),
					err,
				)
			}

			return
		}

		var event realtime.Event

		if err := json.Unmarshal(payload, &event); err != nil {
			_ = sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"invalid_event",
				"invalid realtime event JSON",
			)
			continue
		}

		if err := s.processRealtimeEvent(
			session,
			writer,
			event,
		); err != nil {
			log.Printf(
				"realtime event failed: %s: %v",
				session.ID(),
				err,
			)
		}
	}
}

func (s *Server) processRealtimeEvent(
	session *realtime.Session,
	writer *realtimeWriter,
	event realtime.Event,
) error {
	switch event.Type {

	case "ping":
		response, err := realtime.NewEvent(
			"pong",
			session.ID(),
			"",
			nil,
		)
		if err != nil {
			return err
		}

		return writer.Event(
			session.Context(),
			response,
		)

	case "generation.create":
		return s.handleGenerationCreate(
			session,
			writer,
			event,
		)

	case "generation.cancel":
		generationID := session.CancelGeneration()

		response, err := realtime.NewEvent(
			"generation.cancelled",
			session.ID(),
			generationID,
			nil,
		)
		if err != nil {
			return err
		}

		return writer.Event(
			session.Context(),
			response,
		)

	default:
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			event.Generation,
			"unknown_event",
			"unknown realtime event type: "+event.Type,
		)
	}
}

func (s *Server) handleGenerationCreate(
	session *realtime.Session,
	writer *realtimeWriter,
	event realtime.Event,
) error {
	var data realtime.GenerationCreateData

	if len(event.Data) == 0 {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"invalid_generation",
			"generation.create requires data",
		)
	}

	if err := json.Unmarshal(event.Data, &data); err != nil {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"invalid_generation",
			"invalid generation.create data",
		)
	}

	if data.Text == "" {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"invalid_generation",
			"text is required",
		)
	}

	generationID, generationCtx := session.StartGeneration()

	created, err := realtime.NewEvent(
		"generation.created",
		session.ID(),
		generationID,
		map[string]any{
			"text":  data.Text,
			"voice": data.Voice,
			"speed": data.Speed,
		},
	)
	if err != nil {
		return err
	}

	if err := writer.Event(
		session.Context(),
		created,
	); err != nil {
		return err
	}

	go s.runSpeechGeneration(
		session,
		writer,
		generationCtx,
		generationID,
		data,
	)

	return nil
}

func (s *Server) runSpeechGeneration(
	session *realtime.Session,
	writer *realtimeWriter,
	ctx context.Context,
	generationID string,
	data realtime.GenerationCreateData,
) {
	stream, err := s.engine.Synthesize(
		ctx,
		"",
		tts.Request{
			Text:  data.Text,
			Voice: data.Voice,
			Speed: data.Speed,
		},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}

		_ = sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			generationID,
			"synthesis_failed",
			err.Error(),
		)
		return
	}
	defer stream.Audio.Close()

	started, err := realtime.NewEvent(
		"response.audio.started",
		session.ID(),
		generationID,
		map[string]any{
			"codec":       stream.Format.Codec,
			"sample_rate": stream.Format.SampleRate,
			"channels":    stream.Format.Channels,
		},
	)
	if err != nil {
		return
	}

	if err := writer.Event(ctx, started); err != nil {
		return
	}

	buffer := make([]byte, realtimeAudioChunkSize)

	var totalBytes int64
	var sequence int

	for {
		if err := ctx.Err(); err != nil {
			return
		}

		n, readErr := stream.Audio.Read(buffer)

		if n > 0 {
			chunkEvent, err := realtime.NewEvent(
				"response.audio.delta",
				session.ID(),
				generationID,
				map[string]any{
					"sequence": sequence,
					"bytes":    n,
				},
			)
			if err != nil {
				return
			}

			if err := writer.Event(ctx, chunkEvent); err != nil {
				return
			}

			if err := writer.Binary(
				ctx,
				buffer[:n],
			); err != nil {
				return
			}

			totalBytes += int64(n)
			sequence++
		}

		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			_ = sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				generationID,
				"audio_read_failed",
				readErr.Error(),
			)
			return
		}
	}

	done, err := realtime.NewEvent(
		"response.audio.done",
		session.ID(),
		generationID,
		map[string]any{
			"bytes":  totalBytes,
			"chunks": sequence,
		},
	)
	if err != nil {
		return
	}

	if err := writer.Event(ctx, done); err != nil {
		return
	}

	log.Printf(
		"realtime speech completed: session=%s generation=%s bytes=%d chunks=%d",
		session.ID(),
		generationID,
		totalBytes,
		sequence,
	)
}

func sendRealtimeError(
	ctx context.Context,
	writer *realtimeWriter,
	sessionID string,
	generationID string,
	code string,
	message string,
) error {
	event, err := realtime.NewEvent(
		"error",
		sessionID,
		generationID,
		map[string]string{
			"code":    code,
			"message": message,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"create realtime error event: %w",
			err,
		)
	}

	return writer.Event(ctx, event)
}
