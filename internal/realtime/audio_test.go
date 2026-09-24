package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/conversation"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/speech"
)

func TestAudioCreditWaitGenerationLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "replace", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			s := NewSession(context.Background())
			defer s.Close()
			s.RequireAudioCredit()
			id, ctx, _ := s.StartGeneration()
			f, err := s.AudioFlow(id, 8000)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, err := f.Reserve(ctx, 1); result <- err }()
			deadline := time.After(time.Second)
			for f.Snapshot().WaitCount == 0 {
				select {
				case <-deadline:
					t.Fatal("not waiting")
				case <-time.After(time.Millisecond):
				}
			}
			// Holding all credits while paused does not affect session control access.
			s.Timeline().SetPaused(true)
			switch action {
			case "cancel":
				s.CancelGeneration()
			case "replace":
				s.StartGeneration()
			case "disconnect":
				s.Close()
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("cancel succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("credit waiter survived lifecycle")
			}
			if err := s.UpdateAudioCredit(id, audio.Credit{Capacity: 800}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAudioCreditPreservesPlayedChunkHistory(t *testing.T) {
	s := NewSession(context.Background())
	defer s.Close()
	id := conversationResponse(t, s, "question", false)
	f, err := s.AudioFlow(id, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Reserve(s.Context(), 3200); err != nil {
		t.Fatal(err)
	}
	addConversationAudio(t, s, id)
	s.MarkAssistantTextDone(id)
	s.MarkGenerationDone(id)
	if assistantItem(t, s, id).Status != conversation.Pending {
		t.Fatal("send completion counted as playback")
	}
	if err = s.UpdateAudioCredit(id, audio.Credit{Capacity: 32000, Received: 3200, Played: 2400, Buffered: 800}); err != nil {
		t.Fatal(err)
	}
	// An older receipt snapshot cannot advance history even with a larger played value.
	if err = s.UpdateAudioCredit(id, audio.Credit{Capacity: 32000, Received: 3100, Played: 3000, Buffered: 100}); err != nil {
		t.Fatal(err)
	}
	if s.Timeline().Snapshot().PlayedFrames != 2400 {
		t.Fatal("stale credit altered history")
	}
	s.CancelGenerationID(id)
	if assistantItem(t, s, id).Status != conversation.Interrupted {
		t.Fatal("wrong final status")
	}
	next := conversationResponse(t, s, "next", true)
	request, _ := s.GenerationRequest(next)
	if request.Messages[2].Content != "heard " {
		t.Fatal("unplayed semantic chunk in context", request)
	}
}

func TestCreditAcknowledgementRacingWriteAccountingIsRetained(t *testing.T) {
	s := NewSession(context.Background())
	defer s.Close()
	id, ctx, _ := s.StartGeneration()
	f, _ := s.AudioFlow(id, 8000)
	if err := s.RegisterSpeechChunk(id, speech.Chunk{Sequence: 0, Text: "heard"}, 8000, 1, 100); err != nil {
		t.Fatal(err)
	}
	f.Reserve(ctx, 100)
	// The client can consume the socket write before RecordAudioSent acquires mu.
	if err := s.UpdateAudioCredit(id, audio.Credit{Capacity: 800, Received: 100, Played: 100}); err != nil {
		t.Fatal(err)
	}
	if s.Timeline().Snapshot().PlayedFrames != 0 {
		t.Fatal("played before write completion")
	}
	s.RecordAudioSent(id, 100)
	if s.Timeline().Snapshot().PlayedFrames != 100 {
		t.Fatal("lost final acknowledgement")
	}
	s.RecordPlayback(id, 0)
	if s.Timeline().Snapshot().PlayedFrames != 100 {
		t.Fatal("regressed")
	}
}

func TestLegacySecondsAcknowledgementRacingWriteIsRetained(t *testing.T) {
	s := NewSession(context.Background())
	defer s.Close()
	id, ctx, _ := s.StartGeneration()
	f, _ := s.AudioFlow(id, 8000)
	if err := s.RegisterSpeechChunk(id, speech.Chunk{Sequence: 0, Text: "heard"}, 8000, 1, 8000); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Reserve(ctx, 8000); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlayback(id, 1); err != nil {
		t.Fatal(err)
	}
	if s.Timeline().Snapshot().PlayedFrames != 0 {
		t.Fatal("played before sent")
	}
	s.RecordAudioSent(id, 8000)
	if s.Timeline().Snapshot().PlayedFrames != 8000 {
		t.Fatal("lost legacy final ACK")
	}
}
