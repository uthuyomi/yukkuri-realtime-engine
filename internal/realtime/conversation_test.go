package realtime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/conversation"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

func conversationResponse(t *testing.T, s *Session, text string, textOnly bool) string {
	t.Helper()
	id, _, _, err := s.StartConversationResponse(s.NewInputResponseContext(), text, textOnly)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	return id
}
func assistantItem(t *testing.T, s *Session, id string) conversation.Item {
	t.Helper()
	for _, p := range s.ConversationSnapshot().Items {
		if p.GenerationID == id {
			return p
		}
	}
	t.Fatal("missing assistant")
	return conversation.Item{}
}
func addConversationAudio(t *testing.T, s *Session, id string) {
	t.Helper()
	_ = s.AppendAssistantText(id, "heard unheard", false)
	_ = s.AppendAssistantText(id, "heard unheard", true)
	for i, text := range []string{"heard ", "unheard"} {
		if err := s.RegisterSpeechChunk(id, speech.Chunk{Sequence: i, Text: "phonetic", SourceText: text}, 16000, 1, 1600); err != nil {
			t.Fatal(err)
		}
	}
	s.RecordAudioSent(id, 3200)
}
func TestConversationCommitOnceAndStaleTurn(t *testing.T) {
	s := NewSession(context.Background())
	defer s.Close()
	ctx := s.NewInputResponseContext()
	id, _, _, err := s.StartConversationResponse(ctx, "札幌", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.StartConversationResponse(ctx, "duplicate", true); err == nil {
		t.Fatal("duplicate response accepted")
	}
	_ = s.AppendAssistantText(id, "晴れです", false)
	_ = s.AppendAssistantText(id, "晴れです", true)
	s.MarkAssistantTextDone(id)
	s.MarkGenerationDone(id)
	id2 := conversationResponse(t, s, "明日は", true)
	q, ok := s.GenerationRequest(id2)
	if !ok || len(q.Messages) != 4 || q.Messages[2].Content != "晴れです" {
		t.Fatal(q)
	}
	if err = s.AppendAssistantText(id, "stale", false); err == nil {
		t.Fatal("stale generation changed history")
	}
	if s.MarkAssistantTextDone(id) || s.CancelGenerationID(id) != "" {
		t.Fatal("stale finalize")
	}
	old := s.NewInputResponseContext()
	s.NewInputResponseContext()
	if _, _, _, err = s.StartConversationResponse(old, "stale turn", true); err == nil {
		t.Fatal("stale turn committed")
	}
	if len(s.ConversationSnapshot().Items) != 5 {
		t.Fatal("history polluted")
	}
}
func TestConversationPlaybackCompletionAndCancellation(t *testing.T) {
	s := NewSession(context.Background())
	defer s.Close()
	id := conversationResponse(t, s, "weather", false)
	addConversationAudio(t, s, id)
	s.MarkAssistantTextDone(id)
	s.MarkGenerationDone(id)
	if assistantItem(t, s, id).Status != conversation.Pending {
		t.Fatal("done became heard")
	}
	frames := int64(2400)
	_ = s.RecordPlayback(id, 0, &frames)
	s.CancelGenerationID(id)
	p := assistantItem(t, s, id)
	if p.Status != conversation.Interrupted || p.Content != "heard unheard" || !p.Chunks[0].Played || p.Chunks[1].Played {
		t.Fatal(p)
	}
	frames = 3200
	_ = s.RecordPlayback(id, 0, &frames)
	s.CancelGenerationID(id)
	id2 := conversationResponse(t, s, "next", true)
	q, _ := s.GenerationRequest(id2)
	if q.Messages[2].Content != "heard " {
		t.Fatal("unplayed tail leaked", q)
	}
	id3 := conversationResponse(t, s, "another", false)
	addConversationAudio(t, s, id3)
	s.MarkGenerationDone(id3)
	s.MarkAssistantTextDone(id3)
	_ = s.RecordPlayback(id3, 0, &frames)
	if assistantItem(t, s, id3).Status != conversation.Completed {
		t.Fatal("fully played not completed")
	}
}
func TestConversationSpeculationBarrierAndPromotion(t *testing.T) {
	s, gate := specSession(t, goodSpecSTT(), goodSpecLLM(), specConfig())
	say(t, s)
	specEvent(t, s, "ready", "llm")
	if len(s.ConversationSnapshot().Items) != 1 {
		t.Fatal("speculation wrote history")
	}
	close(gate)
	u := nextCommit(t, s)
	p := s.PromoteSpeculation(u.Context, *u.SpeculationKey)
	if p == nil {
		t.Fatal("no promotion")
	}
	defer p.Stream.Close()
	if len(s.ConversationSnapshot().Items) != 3 {
		t.Fatal("missing/duplicate items")
	}
	if _, _, _, err := s.StartConversationResponse(u.Context, "duplicate", false); err == nil {
		t.Fatal("normal path duplicated speculative user")
	}
	if assistantItem(t, s, p.GenerationID).Content != "" {
		t.Fatal("speculative buffer directly copied to history")
	}
}
func TestConversationChangedDuringSpeculationReusesOnlySTT(t *testing.T) {
	s, gate := specSession(t, goodSpecSTT(), goodSpecLLM(), specConfig())
	old := conversationResponse(t, s, "previous", false)
	addConversationAudio(t, s, old)
	s.MarkAssistantTextDone(old)
	s.MarkGenerationDone(old)
	say(t, s)
	specEvent(t, s, "ready", "llm")
	frames := int64(3200)
	_ = s.RecordPlayback(old, 0, &frames)
	close(gate)
	u := nextCommit(t, s)
	p := s.PromoteSpeculation(u.Context, *u.SpeculationKey)
	if p == nil || p.Stream != nil || p.Transcript.Text != "real transcript" {
		t.Fatal("stale speculative answer adopted")
	}
	q, _ := s.GenerationRequest(p.GenerationID)
	if len(q.Messages) != 4 || q.Messages[2].Content != "heard unheard" {
		t.Fatal(q)
	}
}
func TestConversationInvalidSpeculationLeavesNoItems(t *testing.T) {
	s, _ := specSession(t, goodSpecSTT(), goodSpecLLM(), specConfig())
	say(t, s)
	specEvent(t, s, "ready", "llm")
	_ = s.SpeechStart()
	s.CancelInput(true)
	if len(s.ConversationSnapshot().Items) != 1 {
		t.Fatal("invalid speculation wrote history")
	}
}
func TestConversationRecoveryRetainsAssistantIdentity(t *testing.T) {
	for _, kind := range []backchannel.Kind{backchannel.Backchannel, backchannel.FalseInterruption} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, _ := interruptionSession(t, classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
				return backchannel.Result{Kind: kind}, nil
			}))
			id := conversationResponse(t, s, "old user", false)
			addConversationAudio(t, s, id)
			before := assistantItem(t, s, id)
			shortSpeech(t, s, id)
			decision(t, s, "interruption.recovered")
			after := assistantItem(t, s, id)
			if before.ID != after.ID || after.Status != conversation.Pending {
				t.Fatal("recovery replaced/finalized assistant")
			}
			for _, p := range s.ConversationSnapshot().Items {
				if p.Role == conversation.User && p.Content != "old user" {
					t.Fatal("backchannel wrote user")
				}
			}
		})
	}
}
func TestConversationConcurrentFinalizeAndDisconnect(t *testing.T) {
	s := NewSession(context.Background())
	id := conversationResponse(t, s, "test", false)
	addConversationAudio(t, s, id)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.RecordPlayback(id, .1)
			s.MarkAssistantTextDone(id)
			s.MarkGenerationDone(id)
			s.CancelGenerationID(id)
		}()
	}
	wg.Wait()
	s.Close()
	s.Close()
	p := assistantItem(t, s, id)
	if p.Status == conversation.Pending {
		t.Fatal("disconnect left pending item")
	}
	if err := s.AppendAssistantText(id, "late", false); err == nil {
		t.Fatal("late write")
	}
	if strings.Contains(p.Content, "late") {
		t.Fatal("stale delta stored")
	}
}

func TestConversationTrueInterruptionFinalizedBeforeNextRequest(t *testing.T) {
	s, _, _ := interruptionSession(t, classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
		return backchannel.Result{Kind: backchannel.Interruption}, nil
	}))
	id := conversationResponse(t, s, "original question", false)
	addConversationAudio(t, s, id)
	s.MarkAssistantTextDone(id)
	s.MarkGenerationDone(id)
	_ = s.RecordPlayback(id, .1)
	shortSpeech(t, s, id)
	u := nextCommit(t, s)
	if assistantItem(t, s, id).Status != conversation.Interrupted {
		t.Fatal("old assistant was not finalized at true interruption")
	}
	newID, _, _, err := s.StartConversationResponse(u.Context, "new question", false)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := s.GenerationRequest(newID)
	if len(q.Messages) != 4 || q.Messages[2].Content != "heard " || q.Messages[3].Content != "new question" {
		t.Fatal(q)
	}
}
func TestConversationMisfireAndCancelledInputDoNotCommit(t *testing.T) {
	s, id, _ := interruptionSession(t, classifierFunc(func(context.Context, backchannel.Observation) (backchannel.Result, error) {
		return backchannel.Result{Kind: backchannel.Ambiguous}, nil
	}))
	before := len(s.ConversationSnapshot().Items)
	startSuspect(t, s, id)
	_ = s.VADMisfire()
	decision(t, s, "interruption.recovered")
	if len(s.ConversationSnapshot().Items) != before {
		t.Fatal("misfire added item")
	}
	say(t, s)
	u := nextCommit(t, s)
	s.CancelInput(true)
	if _, _, _, err := s.StartConversationResponse(u.Context, "late transcription", false); err == nil {
		t.Fatal("cancelled input committed")
	}
	if len(s.ConversationSnapshot().Items) != before {
		t.Fatal("cancelled input added item")
	}
}
