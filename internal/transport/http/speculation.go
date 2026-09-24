package httptransport

import (
	"errors"
	"io"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

// Only a Session-issued Promotion has crossed both endpoint and interruption
// barriers. This is the first point where speculative content can reach a writer.
func (s *Server) runPromotedInput(session *realtime.Session, writer *realtimeWriter, p *realtime.Promotion) {
	defer p.Stream.Close()
	if p.Context.Err() != nil {
		return
	}
	transcript, err := realtime.NewEvent("input_audio.transcript.final", session.ID(), "", map[string]string{"text": p.Transcript.Text, "language": p.Transcript.Language})
	if err != nil || writer.Event(p.Context, transcript) != nil {
		if p.GenerationID != "" {
			s.cancelFailedGeneration(session, writer, p.GenerationID)
		}
		return
	}
	if p.Transcript.Text == "" {
		return
	}
	created, err := realtime.NewEvent("generation.created", session.ID(), p.GenerationID, map[string]any{"source": "voice", "provider": s.llmProvider.Name(), "speculation_id": p.Key.ID})
	if err != nil || writer.Event(p.Context, created) != nil {
		s.cancelFailedGeneration(session, writer, p.GenerationID)
		return
	}
	go s.runSpeechPipeline(session, writer, p.Context, p.GenerationID, p.Pipeline, "", 0)
	s.consumeLLMStream(session, writer, p.Context, p.GenerationID, p.Pipeline, &promotedStream{server: s, session: session, writer: writer, promotion: p, stream: p.Stream})
}

// If the adopted stream fails before emitting any text, retry once on the normal
// LLM path using the already committed transcript. After a delta is published,
// the existing official-generation failure policy avoids duplicating an answer.
type promotedStream struct {
	server           *Server
	session          *realtime.Session
	writer           *realtimeWriter
	promotion        *realtime.Promotion
	stream           llm.Stream
	emitted, retried bool
}

func (r *promotedStream) Recv() (llm.Delta, error) {
	delta, err := r.stream.Recv()
	if err != nil && !errors.Is(err, io.EOF) && !r.emitted && !r.retried && r.promotion.Context.Err() == nil {
		r.retried = true
		_ = r.stream.Close()
		event, eventErr := realtime.NewEvent("speculation.fallback", r.session.ID(), r.promotion.GenerationID, realtime.SpeculationUpdate{SpeculationKey: r.promotion.Key, State: realtime.SpeculationPromoted, Stage: "llm", Reason: "stream_failed_before_output", GenerationID: r.promotion.GenerationID})
		if eventErr == nil {
			_ = r.writer.Event(r.promotion.Context, event)
		}
		stream, generateErr := r.server.llmProvider.Generate(r.promotion.Context, llm.Request{Messages: []llm.Message{{Role: "user", Content: r.promotion.Transcript.Text}}})
		if generateErr != nil {
			return llm.Delta{}, generateErr
		}
		if stream == nil {
			return llm.Delta{}, errors.New("LLM provider returned no stream")
		}
		r.stream = stream
		return r.Recv()
	}
	if delta.Text != "" {
		r.emitted = true
	}
	return delta, err
}
func (r *promotedStream) Close() error { return r.stream.Close() }
