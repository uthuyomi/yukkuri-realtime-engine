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
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

const realtimeAudioChunkSize = 16 * 1024

func (s *Server) handleInputAudioBinary(
	session *realtime.Session,
	writer *realtimeWriter,
	payload []byte,
) error {
	if len(payload) == 0 {
		return nil
	}

	if !session.AppendInputAudio(
		payload,
	) {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"input_audio_not_started",
			"binary audio received before input_audio.start",
		)
	}

	log.Printf(
		"input audio chunk: session=%s bytes=%d",
		session.ID(),
		len(payload),
	)

	return nil
}

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
		messageType, payload, err :=
			conn.Read(session.Context())
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

		if messageType == websocket.MessageBinary {
			if err := s.handleInputAudioBinary(
				session,
				writer,
				payload,
			); err != nil {
				log.Printf(
					"input audio failed: session=%s: %v",
					session.ID(),
					err,
				)
			}

			continue
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
	log.Printf(
		"realtime event received: type=%s generation=%s",
		event.Type,
		event.Generation,
	)
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

	case "response.text.delta":
		return s.handleTextDelta(
			session,
			writer,
			event,
		)

	case "response.text.done":
		return s.handleTextDone(
			session,
			writer,
		)

	case "playback.progress":
		return s.handlePlaybackProgress(
			session,
			event,
		)

	case "input_audio.start":
		var data realtime.InputAudioFormatData

		if err := json.Unmarshal(
			event.Data,
			&data,
		); err != nil {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"invalid_input_audio",
				"invalid input_audio.start data",
			)
		}

		if data.SampleRate <= 0 {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"invalid_input_audio",
				"invalid input sample rate",
			)
		}

		if data.Channels != 1 {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"invalid_input_audio",
				"only mono input is currently supported",
			)
		}

		if data.Encoding != "pcm_s16le" {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"invalid_input_audio",
				"unsupported input audio encoding",
			)
		}

		session.StartInputAudio(
			data,
		)

		return nil

	case "input_audio.commit":
		audioData, format, ok :=
			session.CommitInputAudio()

		if !ok {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"input_audio_not_started",
				"input_audio.commit received before input_audio.start",
			)
		}

		if len(audioData) == 0 {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"empty_input_audio",
				"input audio buffer is empty",
			)
		}

		log.Printf(
			"input audio committed: session=%s sample_rate=%d channels=%d encoding=%s bytes=%d",
			session.ID(),
			format.SampleRate,
			format.Channels,
			format.Encoding,
			len(audioData),
		)

		if s.sttProvider == nil {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"stt_not_configured",
				"STT provider is not configured",
			)
		}

		go s.transcribeInputAudio(
			session,
			writer,
			audioData,
			format,
		)

		return nil

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

func (s *Server) transcribeInputAudio(
	session *realtime.Session,
	writer *realtimeWriter,
	audioData []byte,
	format realtime.InputAudioFormatData,
) {
	ctx := session.Context()

	result, err := s.sttProvider.Transcribe(
		ctx,
		stt.Request{
			Audio: audioData,
			Format: stt.AudioFormat{
				SampleRate: format.SampleRate,
				Channels:   format.Channels,
				Encoding:   format.Encoding,
			},
		},
	)
	if err != nil {
		if errors.Is(
			err,
			context.Canceled,
		) {
			return
		}

		log.Printf(
			"STT failed: session=%s: %v",
			session.ID(),
			err,
		)

		_ = sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"stt_failed",
			err.Error(),
		)

		return
	}

	if result == nil {
		_ = sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"stt_failed",
			"STT provider returned no result",
		)

		return
	}

	log.Printf(
		"STT transcript: session=%s language=%s text=%q",
		session.ID(),
		result.Language,
		result.Text,
	)

	response, err := realtime.NewEvent(
		"input_audio.transcript.final",
		session.ID(),
		"",
		map[string]any{
			"text":     result.Text,
			"language": result.Language,
		},
	)
	if err != nil {
		log.Printf(
			"create STT transcript event failed: %v",
			err,
		)

		return
	}

	if err := writer.Event(
		session.Context(),
		response,
	); err != nil {
		log.Printf(
			"send STT transcript failed: session=%s: %v",
			session.ID(),
			err,
		)

		return
	}

	if result.Text == "" {
		return
	}

	s.startLLMGeneration(
		session,
		writer,
		result.Text,
	)
}

func (s *Server) handleGenerationCreate(
	session *realtime.Session,
	writer *realtimeWriter,
	event realtime.Event,
) error {
	var data realtime.GenerationCreateData

	if len(event.Data) > 0 {
		if err := json.Unmarshal(
			event.Data,
			&data,
		); err != nil {
			return sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				"",
				"invalid_generation",
				"invalid generation.create data",
			)
		}
	}

	generationID, generationCtx, pipeline :=
		session.StartGeneration()

	created, err := realtime.NewEvent(
		"generation.created",
		session.ID(),
		generationID,
		map[string]any{
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

	go s.runSpeechPipeline(
		session,
		writer,
		generationCtx,
		generationID,
		pipeline,
		data.Voice,
		data.Speed,
	)

	return nil
}

func (s *Server) handleTextDelta(
	session *realtime.Session,
	writer *realtimeWriter,
	event realtime.Event,
) error {
	var data realtime.TextDeltaData

	if err := json.Unmarshal(
		event.Data,
		&data,
	); err != nil {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			session.CurrentGeneration(),
			"invalid_text_delta",
			"invalid response.text.delta data",
		)
	}

	if data.Text == "" {
		return nil
	}

	pipeline := session.Pipeline()

	if pipeline == nil {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"generation_not_active",
			"no active generation",
		)
	}

	return pipeline.Push(data.Text)
}

func (s *Server) handleTextDone(
	session *realtime.Session,
	writer *realtimeWriter,
) error {
	pipeline := session.Pipeline()

	if pipeline == nil {
		return sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"generation_not_active",
			"no active generation",
		)
	}

	return pipeline.Close()
}

func (s *Server) handlePlaybackProgress(
	session *realtime.Session,
	event realtime.Event,
) error {
	if event.Generation == "" {
		return nil
	}

	timeline := session.Timeline()

	if timeline == nil {
		return nil
	}

	snapshot := timeline.Snapshot()

	// Ignore delayed progress events from an older generation.
	if snapshot.GenerationID != event.Generation {
		return nil
	}

	var data realtime.PlaybackProgressData

	if err := json.Unmarshal(
		event.Data,
		&data,
	); err != nil {
		return fmt.Errorf(
			"decode playback.progress: %w",
			err,
		)
	}

	if data.PlayedSeconds < 0 {
		return fmt.Errorf(
			"invalid played_seconds: %f",
			data.PlayedSeconds,
		)
	}

	if snapshot.SampleRate <= 0 {
		return nil
	}

	playedFrames :=
		int64(
			data.PlayedSeconds *
				float64(
					snapshot.SampleRate,
				),
		)

	timeline.SetPlayed(
		playedFrames,
	)

	updated :=
		timeline.Snapshot()

	log.Printf(
		"playback progress: generation=%s played=%s sent=%s generated=%s buffered=%s",
		event.Generation,
		updated.PlayedDuration(),
		updated.SentDuration(),
		updated.GeneratedDuration(),
		updated.BufferedDuration(),
	)

	return nil
}

func (s *Server) runSpeechPipeline(
	session *realtime.Session,
	writer *realtimeWriter,
	ctx context.Context,
	generationID string,
	pipeline *speech.Pipeline,
	voice string,
	speed float64,
) {
	for {
		select {
		case <-ctx.Done():
			return

		case chunk, ok := <-pipeline.Output():
			if !ok {
				return
			}

			log.Printf(
				"speech chunk: generation=%s sequence=%d text=%q",
				generationID,
				chunk.Sequence,
				chunk.Text,
			)

			if err := s.synthesizeSpeechChunk(
				session,
				writer,
				ctx,
				generationID,
				chunk,
				voice,
				speed,
			); err != nil {
				if errors.Is(
					err,
					context.Canceled,
				) {
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
		}
	}
}

func (s *Server) synthesizeSpeechChunk(
	session *realtime.Session,
	writer *realtimeWriter,
	ctx context.Context,
	generationID string,
	chunk speech.Chunk,
	voice string,
	speed float64,
) error {
	log.Printf(
		"AquesTalk input: generation=%s sequence=%d bytes=%d text=%q",
		generationID,
		chunk.Sequence,
		len([]byte(chunk.Text)),
		chunk.Text,
	)

	stream, err := s.engine.Synthesize(
		ctx,
		"",
		tts.Request{
			Text:  chunk.Text,
			Voice: voice,
			Speed: speed,
		},
	)
	if err != nil {
		return err
	}
	defer stream.Audio.Close()

	wavData, err := io.ReadAll(stream.Audio)
	if err != nil {
		return fmt.Errorf(
			"read synthesized WAV: %w",
			err,
		)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	pcm, err := audio.DecodeWAV(wavData)
	if err != nil {
		return fmt.Errorf(
			"decode synthesized WAV: %w",
			err,
		)
	}

	bytesPerSample := pcm.Bits / 8

	if bytesPerSample <= 0 {
		return fmt.Errorf(
			"invalid PCM bits per sample: %d",
			pcm.Bits,
		)
	}

	if pcm.Channels <= 0 {
		return fmt.Errorf(
			"invalid PCM channel count: %d",
			pcm.Channels,
		)
	}

	bytesPerFrame :=
		bytesPerSample * pcm.Channels

	if len(pcm.Data)%bytesPerFrame != 0 {
		return fmt.Errorf(
			"PCM data is not frame-aligned: bytes=%d bytes_per_frame=%d",
			len(pcm.Data),
			bytesPerFrame,
		)
	}

	generatedFrames :=
		int64(
			len(pcm.Data) /
				bytesPerFrame,
		)

	timeline := session.Timeline()

	if timeline != nil {
		timeline.SetFormat(
			pcm.SampleRate,
			pcm.Channels,
		)

		timeline.AddGenerated(
			generatedFrames,
		)
	}

	started, err := realtime.NewEvent(
		"response.audio.chunk.started",
		session.ID(),
		generationID,
		map[string]any{
			"sequence":        chunk.Sequence,
			"text":            chunk.Text,
			"codec":           "pcm_s16le",
			"sample_rate":     pcm.SampleRate,
			"channels":        pcm.Channels,
			"bits_per_sample": pcm.Bits,
		},
	)
	if err != nil {
		return err
	}

	if err := writer.Event(
		ctx,
		started,
	); err != nil {
		return err
	}

	var totalBytes int64
	audioSequence := 0

	for offset := 0; offset < len(pcm.Data); {
		if err := ctx.Err(); err != nil {
			return err
		}

		remaining :=
			len(pcm.Data) - offset

		chunkBytes :=
			realtimeAudioChunkSize

		if chunkBytes > remaining {
			chunkBytes = remaining
		}

		if chunkBytes < remaining {
			chunkBytes -=
				chunkBytes % bytesPerFrame
		}

		if chunkBytes <= 0 {
			return fmt.Errorf(
				"invalid PCM chunk size: bytes_per_frame=%d",
				bytesPerFrame,
			)
		}

		end :=
			offset + chunkBytes

		audioData := pcm.Data[offset:end]

		delta, err := realtime.NewEvent(
			"response.audio.delta",
			session.ID(),
			generationID,
			map[string]any{
				"speech_sequence": chunk.Sequence,
				"audio_sequence":  audioSequence,
				"bytes":           len(audioData),
			},
		)
		if err != nil {
			return err
		}

		if err := writer.Event(
			ctx,
			delta,
		); err != nil {
			return err
		}

		if err := writer.Binary(
			ctx,
			audioData,
		); err != nil {
			return err
		}

		if len(audioData)%bytesPerFrame != 0 {
			return fmt.Errorf(
				"PCM chunk is not frame-aligned: bytes=%d bytes_per_frame=%d",
				len(audioData),
				bytesPerFrame,
			)
		}

		if timeline != nil {
			sentFrames :=
				int64(
					len(audioData) /
						bytesPerFrame,
				)

			timeline.AddSent(
				sentFrames,
			)
		}

		totalBytes += int64(len(audioData))
		audioSequence++
		offset = end
	}

	done, err := realtime.NewEvent(
		"response.audio.chunk.done",
		session.ID(),
		generationID,
		map[string]any{
			"sequence": chunk.Sequence,
			"bytes":    totalBytes,
		},
	)
	if err != nil {
		return err
	}

	if timeline != nil {
		snapshot := timeline.Snapshot()

		log.Printf(
			"playback timeline: generation=%s generated=%s sent=%s played=%s",
			generationID,
			snapshot.GeneratedDuration(),
			snapshot.SentDuration(),
			snapshot.PlayedDuration(),
		)
	}

	return writer.Event(
		ctx,
		done,
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

func (s *Server) startLLMGeneration(
	session *realtime.Session,
	writer *realtimeWriter,
	userText string,
) {
	if s.llmProvider == nil {
		_ = sendRealtimeError(
			session.Context(),
			writer,
			session.ID(),
			"",
			"llm_not_configured",
			"LLM provider is not configured",
		)

		return
	}

	generationID, generationCtx, pipeline :=
		session.StartGeneration()

	created, err := realtime.NewEvent(
		"generation.created",
		session.ID(),
		generationID,
		map[string]any{
			"source":   "voice",
			"provider": s.llmProvider.Name(),
		},
	)
	if err != nil {
		return
	}

	if err := writer.Event(
		generationCtx,
		created,
	); err != nil {
		return
	}

	go s.runSpeechPipeline(
		session,
		writer,
		generationCtx,
		generationID,
		pipeline,
		"",
		0,
	)

	go s.runLLMGeneration(
		session,
		writer,
		generationCtx,
		generationID,
		pipeline,
		userText,
	)
}

func (s *Server) runLLMGeneration(
	session *realtime.Session,
	writer *realtimeWriter,
	ctx context.Context,
	generationID string,
	pipeline *speech.Pipeline,
	userText string,
) {
	log.Printf(
		"LLM generation started: generation=%s text=%q",
		generationID,
		userText,
	)

	stream, err := s.llmProvider.Generate(
		ctx,
		llm.Request{
			Messages: []llm.Message{
				{
					Role:    "user",
					Content: userText,
				},
			},
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
			"llm_failed",
			err.Error(),
		)

		return
	}

	defer stream.Close()

	for {
		delta, err := stream.Recv()

		if errors.Is(err, io.EOF) {
			if err := pipeline.Close(); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf(
						"close speech pipeline failed: generation=%s: %v",
						generationID,
						err,
					)
				}
			}

			done, eventErr := realtime.NewEvent(
				"response.text.done",
				session.ID(),
				generationID,
				nil,
			)

			if eventErr == nil {
				_ = writer.Event(
					ctx,
					done,
				)
			}

			log.Printf(
				"LLM generation completed: generation=%s",
				generationID,
			)

			return
		}

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}

			_ = sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				generationID,
				"llm_stream_failed",
				err.Error(),
			)

			return
		}

		if delta.Text == "" {
			continue
		}

		event, eventErr := realtime.NewEvent(
			"response.text.delta",
			session.ID(),
			generationID,
			realtime.TextDeltaData{
				Text: delta.Text,
			},
		)
		if eventErr != nil {
			return
		}

		if err := writer.Event(
			ctx,
			event,
		); err != nil {
			return
		}

		if err := pipeline.Push(
			delta.Text,
		); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}

			_ = sendRealtimeError(
				session.Context(),
				writer,
				session.ID(),
				generationID,
				"speech_pipeline_failed",
				err.Error(),
			)

			return
		}
	}
}
