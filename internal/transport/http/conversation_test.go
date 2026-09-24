package httptransport

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/conversation"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

func readConversationStatus(t *testing.T, c *websocket.Conn, ctx context.Context, id string, status conversation.Status) {
	t.Helper()
	for {
		e := readType(t, c, ctx, "conversation.item.updated")
		if strings.Contains(string(e.Data), "private") {
			t.Fatal("body leaked into metadata")
		}
		var u realtime.ConversationUpdate
		if err := json.Unmarshal(e.Data, &u); err != nil {
			t.Fatal(err)
		}
		if u.GenerationID == id && u.Status == status {
			return
		}
	}
}
func TestConversationWireMultiTurn(t *testing.T) {
	for _, mode := range []string{"text", "legacy", "speculation"} {
		t.Run(mode, func(t *testing.T) {
			requests := make(chan llm.Request, 4)
			var transcriptions atomic.Int32
			st := specSTT(func(context.Context, stt.Request) (*stt.Result, error) {
				if transcriptions.Add(1) == 1 {
					return &stt.Result{Text: "private first user"}, nil
				}
				return &stt.Result{Text: "private second user"}, nil
			})
			lm := specLLM(func(_ context.Context, r llm.Request) (llm.Stream, error) { requests <- r; return &answerStream{}, nil })
			s, d, tts := specServer(t, st, lm)
			cfg := conversation.DefaultConfig()
			cfg.SystemPrompt = "server-only system"
			if err := s.SetConversationConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if mode != "speculation" {
				sp := realtime.DefaultSpeculationConfig()
				sp.Enabled = false
				_ = s.SetSpeculationConfig(sp)
			} else {
				sp := realtime.DefaultSpeculationConfig()
				sp.Cooldown = 0
				_ = s.SetSpeculationConfig(sp)
			}
			close(d.release)
			c, ctx := connectTest(t, s)
			if mode == "speculation" {
				sendEvent(t, c, ctx, "input_audio.start", realtime.InputAudioFormatData{Mode: "realtime", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
			}
			for turn := 0; turn < 2; turn++ {
				if mode == "text" {
					text := "private first user"
					if turn == 1 {
						text = "private second user"
					}
					sendEvent(t, c, ctx, "input_text.commit", map[string]string{"text": text})
				} else {
					if mode == "legacy" {
						sendEvent(t, c, ctx, "input_audio.start", realtime.InputAudioFormatData{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"})
					} else {
						sendEvent(t, c, ctx, "input_audio.speech_start", nil)
					}
					if err := c.Write(ctx, websocket.MessageBinary, make([]byte, 1024)); err != nil {
						t.Fatal(err)
					}
					if mode == "legacy" {
						sendEvent(t, c, ctx, "input_audio.commit", nil)
					} else {
						sendEvent(t, c, ctx, "input_audio.speech_end", nil)
					}
				}
				created := readType(t, c, ctx, "generation.created")
				readType(t, c, ctx, "generation.done")
				if mode != "text" {
					b, _ := json.Marshal(map[string]any{"type": "playback.progress", "generation_id": created.Generation, "data": map[string]any{"played_source_frames": 1}})
					if err := c.Write(ctx, websocket.MessageText, b); err != nil {
						t.Fatal(err)
					}
					readConversationStatus(t, c, ctx, created.Generation, conversation.Completed)
				}
				select {
				case request := <-requests:
					if request.Messages[0].Role != "system" || request.Messages[0].Content != cfg.SystemPrompt {
						t.Fatal("system override lost", request)
					}
					if turn == 0 && len(request.Messages) != 2 {
						t.Fatal(request)
					}
					if turn == 1 && (len(request.Messages) != 4 || request.Messages[1].Content != "private first user" || request.Messages[2].Content != "private answer." || request.Messages[3].Content != "private second user") {
						t.Fatal("multi-turn context missing", request)
					}
				case <-ctx.Done():
					t.Fatal("no request")
				}
			}
			if mode == "text" && tts.calls.Load() != 0 {
				t.Fatal("text-only invoked TTS")
			}
			if mode != "text" && transcriptions.Load() != 2 {
				t.Fatal("speculative/normal STT duplicated")
			}
		})
	}
}
func TestConversationClientCannotOverrideSystem(t *testing.T) {
	s := New(Config{}, engine.New())
	calls := make(chan llm.Request, 1)
	s.SetLLMProvider(&testLLM{calls: calls})
	c, ctx := connectTest(t, s)
	sendEvent(t, c, ctx, "input_text.commit", map[string]string{"text": "hello", "role": "system", "system_prompt": "untrusted"})
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var e realtime.Event
		_ = json.Unmarshal(b, &e)
		if e.Type == "error" {
			if !strings.Contains(string(e.Data), "invalid_text_input") {
				t.Fatal(string(e.Data))
			}
			break
		}
	}
	select {
	case <-calls:
		t.Fatal("privileged client input reached provider")
	default:
	}
}
