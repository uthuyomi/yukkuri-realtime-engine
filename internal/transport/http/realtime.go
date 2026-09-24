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
	if session.RealtimeInputActive() {
		if err := session.AppendRealtimeAudio(payload); err != nil {
			return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_input_audio", err.Error())
		}
		return nil
	}
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
	conn.SetReadLimit(64 * 1024)

	session, err := realtime.NewSessionWithConversation(r.Context(), s.conversationConfig)
	if err != nil {
		log.Printf("configure conversation: %v", err)
		return
	}
	defer session.Close()

	writer := newRealtimeWriter(conn)
	if s.turnProvider != nil {
		if err := session.ConfigureInput(s.turnProvider, s.endpointConfig); err != nil {
			log.Printf("configure input: %v", err)
			return
		}
		if err := session.ConfigureInterruption(s.backchannelProvider, s.interruptionConfig); err != nil {
			log.Printf("configure interruption: %v", err)
			return
		}
		if s.speculativeSTT != nil && s.llmProvider != nil {
			if err := session.ConfigureSpeculation(s.speculativeSTT, s.llmProvider, s.speculationConfig); err != nil {
				log.Printf("configure speculation: %v", err)
				return
			}
		}
		inputDone := make(chan struct{})
		go func() { defer close(inputDone); s.consumeInputUpdates(session, writer) }()
		defer func() { session.Close(); <-inputDone }()
	}

	log.Printf(
		"realtime session connected: %s",
		session.ID(),
	)

	created, err := realtime.NewEvent(
		"session.created",
		session.ID(),
		"",
		map[string]any{
			"transport":               "websocket",
			"conversation_id":         session.ConversationID(),
			"interruption_timeout_ms": s.interruptionConfig.DecisionWindow.Milliseconds(),
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

	conversationDone := make(chan struct{})
	go func() { defer close(conversationDone); s.consumeConversationUpdates(session, writer) }()
	defer func() { session.Close(); <-conversationDone }()

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

	case "input_text.commit":
		return s.handleInputText(session, writer, event)

	case "generation.create":
		return s.handleGenerationCreate(
			session,
			writer,
			event,
		)

	case "generation.cancel":
		generationID := session.CancelGenerationID(event.Generation)
		if generationID == "" && event.Generation != "" {
			return nil
		}

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
		if event.Generation != "" && event.Generation != session.CurrentGeneration() {
			return nil
		}
		return s.handleTextDone(
			session,
			writer,
		)

	case "playback.progress":
		return s.handlePlaybackProgress(
			session,
			event,
		)
	case "interruption.suspected", "playback.paused", "playback.overflow", "interruption.failed":
		var data realtime.InterruptionRequestData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return sendRealtimeError(session.Context(), writer, session.ID(), event.Generation, "invalid_interruption", err.Error())
		}
		var err error
		switch event.Type {
		case "interruption.suspected":
			err = session.BeginInterruption(event.Generation, data.InterruptionID)
		case "playback.paused":
			err = session.PlaybackPaused(event.Generation, data.InterruptionID, data.PlayedSeconds, data.PlayedSourceFrames)
		case "playback.overflow":
			session.PlaybackFailed(event.Generation, "", "playback_overflow")
		case "interruption.failed":
			session.PlaybackFailed(event.Generation, data.InterruptionID, "client_decision_timeout")
		}
		if err != nil {
			return sendRealtimeError(session.Context(), writer, session.ID(), event.Generation, "invalid_interruption", err.Error())
		}
		return nil

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

		if data.SampleRate != 16000 {
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

		if data.Mode == "realtime" {
			if err := session.StartRealtimeInput(data); err != nil {
				return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_input_audio", err.Error())
			}
			return nil
		}
		if data.Mode != "" || session.RealtimeInputActive() {
			return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_input_audio", "stop realtime input before changing modes")
		}
		session.StartInputAudio(
			data,
		)

		return nil

	case "input_audio.speech_start", "input_audio.speech_end", "input_audio.vad_misfire":
		var err error
		if event.Type == "input_audio.speech_start" {
			var data realtime.InterruptionRequestData
			if len(event.Data) > 0 {
				if err := json.Unmarshal(event.Data, &data); err != nil {
					return err
				}
			}
			err = session.SpeechStartWithInterruption(event.Generation, data.InterruptionID)
		} else if event.Type == "input_audio.vad_misfire" {
			err = session.VADMisfire()
		} else {
			err = session.SpeechEnd()
		}
		if err != nil {
			return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_vad_event", err.Error())
		}
		return nil
	case "input_audio.cancel", "input_audio.stop":
		session.CancelInput(event.Type == "input_audio.stop")
		return nil
	case "input_audio.commit":
		if session.RealtimeInputActive() {
			return sendRealtimeError(session.Context(), writer, session.ID(), "", "server_endpointing", "realtime turns are committed by the server")
		}
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
			session.NewInputResponseContext(),
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
	ctx context.Context,
	session *realtime.Session,
	writer *realtimeWriter,
	audioData []byte,
	format realtime.InputAudioFormatData,
) {
	if ctx.Err() != nil {
		return
	}
	if s.sttProvider == nil {
		_ = sendRealtimeError(ctx, writer, session.ID(), "", "stt_not_configured", "STT provider is not configured")
		return
	}

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

		log.Printf("STT failed: session=%s", session.ID())

		_ = sendRealtimeError(
			ctx,
			writer,
			session.ID(),
			"",
			"stt_failed",
			"STT transcription failed",
		)

		return
	}

	if ctx.Err() != nil {
		return
	}
	if result == nil {
		_ = sendRealtimeError(
			ctx,
			writer,
			session.ID(),
			"",
			"stt_failed",
			"STT provider returned no result",
		)

		return
	}

	log.Printf(
		"STT completed: session=%s language=%s bytes=%d",
		session.ID(),
		result.Language,
		len(result.Text),
	)

	response, err := realtime.NewEvent(
		"input_audio.transcript.final",
		session.ID(),
		"",
		map[string]any{
			"text":     result.Text,
			"language": result.Language,
			"turn_id":  session.InputTurnID(ctx),
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
		ctx,
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
		ctx,
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

	if data.Output != "" && data.Output != "audio" && data.Output != "text" {
		return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_output", "output must be audio or text")
	}
	generationID, generationCtx, pipeline :=
		session.StartGenerationMode(data.Output == "text")

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

	if data.Output != "text" {
		go s.runSpeechPipeline(
			session,
			writer,
			generationCtx,
			generationID,
			pipeline,
			data.Voice,
			data.Speed,
		)

	}
	return nil
}

func (s *Server) handleTextDelta(
	session *realtime.Session,
	writer *realtimeWriter,
	event realtime.Event,
) error {
	id := session.CurrentGeneration()
	if event.Generation != "" && event.Generation != id {
		return nil
	}
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

	if err := session.AppendAssistantText(id, data.Text, false); err != nil {
		return err
	}
	if err := session.AppendAssistantText(id, data.Text, true); err != nil {
		return err
	}
	if session.GenerationTextOnly(id) {
		return nil
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

	id := session.CurrentGeneration()
	session.MarkAssistantTextDone(id)
	if session.GenerationTextOnly(id) {
		return s.finishTextGeneration(session, writer, id)
	}
	return pipeline.Close()
}

func (s *Server) handlePlaybackProgress(session *realtime.Session, event realtime.Event) error {
	var data realtime.PlaybackProgressData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	return session.RecordPlayback(event.Generation, data.PlayedSeconds, data.PlayedSourceFrames)
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
	completed := false
	defer func() {
		if !completed && ctx.Err() == nil {
			s.cancelFailedGeneration(session, writer, generationID)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return

		case chunk, ok := <-pipeline.Output():
			if !ok {
				completed = true
				if session.MarkGenerationDone(generationID) {
					event, err := realtime.NewEvent("generation.done", session.ID(), generationID, nil)
					if err == nil {
						err = writer.Event(ctx, event)
					}
					if err != nil {
						log.Printf("generation done event: %v", err)
					}
				}
				return
			}

			log.Printf(
				"speech chunk: generation=%s sequence=%d bytes=%d",
				generationID,
				chunk.Sequence,
				len(chunk.Text),
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
		"AquesTalk input: generation=%s sequence=%d bytes=%d",
		generationID,
		chunk.Sequence,
		len([]byte(chunk.Text)),
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

	timeline := session.TimelineForGeneration(generationID)

	if err := session.RegisterSpeechChunk(generationID, chunk, pcm.SampleRate, pcm.Channels, generatedFrames); err != nil {
		return err
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
				"sample_rate":     pcm.SampleRate,
				"channels":        pcm.Channels,
				"bits_per_sample": pcm.Bits,
			},
		)
		if err != nil {
			return err
		}

		if err := writer.AudioDelta(ctx, delta, audioData); err != nil {
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

			session.RecordAudioSent(generationID, sentFrames)
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

func (s *Server) startLLMGeneration(inputCtx context.Context, session *realtime.Session, writer *realtimeWriter, userText string, textMode ...bool) {
	textOnly := len(textMode) > 0 && textMode[0]
	id, ctx, pipeline, err := session.StartConversationResponse(inputCtx, userText, textOnly)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			_ = sendRealtimeError(inputCtx, writer, session.ID(), "", "conversation_rejected", err.Error())
		}
		return
	}
	if id == "" {
		return
	}
	if s.llmProvider == nil {
		s.cancelFailedGeneration(session, writer, id)
		_ = sendRealtimeError(session.Context(), writer, session.ID(), id, "llm_not_configured", "LLM provider is not configured")
		return
	}
	created, err := realtime.NewEvent("generation.created", session.ID(), id, map[string]any{"source": map[bool]string{true: "text", false: "voice"}[textOnly], "provider": s.llmProvider.Name(), "output": map[bool]string{true: "text", false: "audio"}[textOnly]})
	if err != nil || writer.Event(ctx, created) != nil {
		s.cancelFailedGeneration(session, writer, id)
		return
	}
	if !textOnly {
		go s.runSpeechPipeline(session, writer, ctx, id, pipeline, "", 0)
	}
	go s.runLLMGeneration(session, writer, ctx, id, pipeline)
}

func (s *Server) runLLMGeneration(session *realtime.Session, writer *realtimeWriter, ctx context.Context, generationID string, pipeline *speech.Pipeline) {
	request, ok := session.GenerationRequest(generationID)
	if !ok {
		return
	}
	log.Printf("LLM generation started: generation=%s context_items=%d", generationID, len(request.Messages))
	stream, err := s.llmProvider.Generate(ctx, request)
	if err != nil || stream == nil {
		if stream != nil {
			_ = stream.Close()
		}
		if ctx.Err() == nil {
			_ = sendRealtimeError(ctx, writer, session.ID(), generationID, "llm_failed", "LLM request failed")
		}
		s.cancelFailedGeneration(session, writer, generationID)
		return
	}
	s.consumeLLMStream(session, writer, ctx, generationID, pipeline, stream)
}

func (s *Server) consumeLLMStream(session *realtime.Session, writer *realtimeWriter, ctx context.Context, generationID string, pipeline *speech.Pipeline, stream llm.Stream) {
	completed := false
	defer func() {
		if !completed && ctx.Err() == nil {
			s.cancelFailedGeneration(session, writer, generationID)
		}
	}()
	defer stream.Close()
	textOnly := session.GenerationTextOnly(generationID)
	for {
		delta, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if !session.MarkAssistantTextDone(generationID) {
				return
			}
			done, eventErr := realtime.NewEvent("response.text.done", session.ID(), generationID, nil)
			if eventErr != nil || writer.Event(ctx, done) != nil {
				return
			}
			if textOnly {
				if s.finishTextGeneration(session, writer, generationID) != nil {
					return
				}
			} else {
				if pipeline.Close() != nil {
					return
				}
			}
			completed = true
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				_ = sendRealtimeError(ctx, writer, session.ID(), generationID, "llm_stream_failed", "LLM stream failed")
			}
			return
		}
		if delta.Text == "" {
			continue
		}
		if err = session.AppendAssistantText(generationID, delta.Text, false); err != nil {
			return
		}
		event, eventErr := realtime.NewEvent("response.text.delta", session.ID(), generationID, realtime.TextDeltaData{Text: delta.Text})
		if eventErr != nil || writer.Event(ctx, event) != nil {
			return
		}
		if err = session.AppendAssistantText(generationID, delta.Text, true); err != nil {
			return
		}
		if !textOnly {
			if err = pipeline.Push(delta.Text); err != nil {
				return
			}
		}
	}
}
