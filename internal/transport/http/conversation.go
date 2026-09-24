package httptransport

import (
	"bytes"
	"encoding/json"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"io"
	"strings"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

func (s *Server) consumeConversationUpdates(session *realtime.Session, writer *realtimeWriter) {
	for {
		select {
		case <-session.Context().Done():
			return
		case u := <-session.ConversationUpdates():
			e, err := realtime.NewEvent("conversation.item.updated", session.ID(), u.GenerationID, u)
			if err != nil || writer.Event(session.Context(), e) != nil {
				session.Close()
				return
			}
		}
	}
}
func (s *Server) handleInputText(session *realtime.Session, writer *realtimeWriter, event realtime.Event) error {
	var data struct {
		Text   string `json:"text"`
		Output string `json:"output"`
	}
	d := json.NewDecoder(bytes.NewReader(event.Data))
	d.DisallowUnknownFields()
	if err := d.Decode(&data); err != nil {
		return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_text_input", "text and optional output are required")
	}
	if d.Decode(new(any)) != io.EOF || strings.TrimSpace(data.Text) == "" || (data.Output != "" && data.Output != "text" && data.Output != "audio") {
		return sendRealtimeError(session.Context(), writer, session.ID(), "", "invalid_text_input", "invalid text or output")
	}
	if s.llmProvider == nil || (data.Output == "audio" && !s.engine.HasTTS("")) {
		return protocol.Error("provider_unavailable")
	}
	ctx, err := session.NewTextResponseContext()
	if err != nil {
		return sendRealtimeError(session.Context(), writer, session.ID(), "", "input_conflict", err.Error())
	}
	s.startLLMGeneration(ctx, session, writer, data.Text, data.Output != "audio")
	return nil
}
func (s *Server) finishTextGeneration(session *realtime.Session, writer *realtimeWriter, id string) error {
	if !session.MarkGenerationDone(id) {
		return nil
	}
	event, err := realtime.NewEvent("generation.done", session.ID(), id, nil)
	if err != nil {
		return err
	}
	return writer.Event(session.Context(), event)
}
