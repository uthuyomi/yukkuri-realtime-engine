package conversation

import (
	"fmt"
	"strings"
	"testing"
)

func runtimeForTest(t *testing.T, c Config) *Runtime {
	t.Helper()
	r, err := New("conversation", "system", c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func exchange(t *testing.T, r *Runtime, turn, user, answer string) {
	t.Helper()
	if _, err := r.CommitUser("u"+turn, turn, user); err != nil {
		t.Fatal(err)
	}
	r.BeginAssistant("a"+turn, turn, turn, true)
	if err := r.Append(turn, answer, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(turn, answer, true); err != nil {
		t.Fatal(err)
	}
	r.Done(turn, false)
}
func TestConversationLifecycleAndSnapshotIsolation(t *testing.T) {
	r := runtimeForTest(t, DefaultConfig())
	exchange(t, r, "one", "札幌の天気", "晴れです。")
	if added, err := r.CommitUser("duplicate", "one", "different"); added || err != nil {
		t.Fatal("duplicate user")
	}
	if added, err := r.CommitUser("empty", "empty", " \n"); added || err != nil {
		t.Fatal("empty user")
	}
	q, err := r.Context("明日は？")
	if err != nil || len(q.Messages) != 4 || q.Messages[2].Content != "晴れです。" || q.Messages[3].Role != "user" {
		t.Fatal(q, err)
	}
	snapshot := r.Snapshot()
	snapshot.Items[1].Content = "changed"
	if r.User("one").Content == "changed" {
		t.Fatal("snapshot aliased store")
	}
	r.Finalize("one")
	r.Finalize("one")
	if r.Assistant("one").Status != Completed {
		t.Fatal("completed item mutated")
	}
}
func TestVoiceHistoryUsesOnlyFullyPlayedChunks(t *testing.T) {
	for _, played := range []int64{0, 99, 100, 150, 200} {
		t.Run(fmt.Sprint(played), func(t *testing.T) {
			r := runtimeForTest(t, DefaultConfig())
			_, _ = r.CommitUser("u", "t", "天気")
			r.BeginAssistant("a", "t", "g", false)
			_ = r.Append("g", "札幌は晴れです。ただし夕方は雨です。", false)
			if err := r.AddChunk("g", 0, "札幌は晴れです。", 0, 100); err != nil {
				t.Fatal(err)
			}
			if err := r.AddChunk("g", 1, "ただし夕方は雨です。", 100, 200); err != nil {
				t.Fatal(err)
			}
			r.Done("g", false)
			r.Done("g", true)
			if r.Assistant("g").Status != Pending {
				t.Fatal("generation done treated as playback")
			}
			r.Progress("g", 200, played)
			r.Finalize("g")
			r.Finalize("g")
			r.Progress("g", 200, 200)
			p := r.Assistant("g")
			want := Cancelled
			if played > 0 {
				want = Interrupted
			}
			if played == 200 {
				want = Completed
			}
			if p.Status != want {
				t.Fatal(p.Status, want)
			}
			q, _ := r.Context("次の質問")
			if played < 100 {
				if len(q.Messages) != 3 {
					t.Fatal("unplayed content included")
				}
			} else {
				wantText := "札幌は晴れです。"
				if played == 200 {
					wantText += "ただし夕方は雨です。"
				}
				if len(q.Messages) != 4 || q.Messages[2].Content != wantText {
					t.Fatal(q)
				}
			}
		})
	}
}
func TestHistoryAndContextLimitsPreserveGroupsAndSystem(t *testing.T) {
	c := DefaultConfig()
	c.SystemPrompt = "server system"
	c.MaxItems = 7
	c.MaxContextItems = 4
	c.MaxItemBytes = 100
	c.MaxBytes = 500
	c.MaxContextBytes = 120
	r := runtimeForTest(t, c)
	for i := 0; i < 12; i++ {
		exchange(t, r, fmt.Sprint(i), "question", "answer")
	}
	snap := r.Snapshot()
	if len(snap.Items) != 7 || snap.Items[1].TurnID != "9" {
		t.Fatal("oldest pairs not evicted", snap)
	}
	q, _ := r.Context("current")
	if len(q.Messages) != 4 || q.Messages[0].Content != c.SystemPrompt {
		t.Fatal(q)
	}
	n := 0
	for _, m := range q.Messages {
		n += len(m.Content)
	}
	if n > c.MaxContextBytes {
		t.Fatal("context overflow")
	}
	if _, err := r.Context(strings.Repeat("x", 101)); err != ErrLimit {
		t.Fatal("candidate limit")
	}
	if _, err := r.CommitUser("oversized", "new", strings.Repeat("x", 101)); err != ErrLimit {
		t.Fatal("item limit")
	}
	if len(r.Snapshot().Items) != 7 {
		t.Fatal("invalid write changed history")
	}
	// Storage and request windows are independent; no split exchanges on bytes.
	exchange(t, r, "large", strings.Repeat("q", 70), strings.Repeat("a", 70))
	q, _ = r.Context("current")
	if len(q.Messages) != 2 {
		t.Fatal("oversized pair was split", q)
	}
	if r.size() > c.MaxBytes {
		t.Fatal("history byte overflow")
	}
	c.MaxItems = 50
	c.MaxBytes = 4*c.MaxItemBytes + len(c.SystemPrompt)
	bounded := runtimeForTest(t, c)
	for i := 0; i < 8; i++ {
		exchange(t, bounded, fmt.Sprint(i), strings.Repeat("q", 100), strings.Repeat("a", 100))
		if bounded.size() > c.MaxBytes {
			t.Fatal("retained text exceeded byte budget")
		}
	}
	if len(bounded.Snapshot().Items) != 3 {
		t.Fatal("old exchanges not evicted by byte limit")
	}
}
func TestGeneratedAndSentTextDifferAndChunksAreBounded(t *testing.T) {
	c := DefaultConfig()
	c.MaxChunks = 1
	r := runtimeForTest(t, c)
	_, _ = r.CommitUser("u", "t", "hi")
	r.BeginAssistant("a", "t", "g", true)
	_ = r.Append("g", "delivered unseen", false)
	_ = r.Append("g", "delivered", true)
	r.Finalize("g")
	q, _ := r.Context("next")
	if q.Messages[2].Content != "delivered" {
		t.Fatal(q)
	}
	r.BeginAssistant("b", "t2", "g2", false)
	if err := r.AddChunk("g2", 0, "first", 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := r.AddChunk("g2", 1, "second", 100, 200); err != ErrLimit {
		t.Fatal(err)
	}
	copy := r.Snapshot()
	copy.Items[len(copy.Items)-1].Chunks[0].Text = "mutated"
	if r.Assistant("g2").Chunks[0].Text != "first" {
		t.Fatal("chunk alias")
	}
	if err := r.Append("g2", strings.Repeat("a", c.MaxItemBytes+1), false); err != ErrLimit {
		t.Fatal("generated text limit", err)
	}
	if r.Assistant("g2").Content != "" {
		t.Fatal("oversized delta retained")
	}
}
