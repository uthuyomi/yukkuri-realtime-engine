// Package conversation owns provider-neutral, bounded conversation state.
// Runtime is intentionally serialized by its owner (the realtime Session lock).
// No provider calls, storage I/O or transport writes occur here.
package conversation

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
)

type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
	System    Role = "system"
)

type Status string

const (
	Pending     Status = "pending"
	Committed   Status = "committed"
	Completed   Status = "completed"
	Interrupted Status = "interrupted"
	Cancelled   Status = "cancelled"
)
const DefaultSystemPrompt = "あなたは音声会話アシスタントです。自然な日本語で簡潔に応答してください。回答は音声合成されるため、Markdownを必要以上に使わないでください。"

var ErrLimit = errors.New("conversation limit exceeded")

type Config struct {
	SystemPrompt    string
	MaxItems        int
	MaxBytes        int
	MaxItemBytes    int
	MaxContextItems int
	MaxContextBytes int
	MaxChunks       int
}

func DefaultConfig() Config {
	return Config{SystemPrompt: DefaultSystemPrompt, MaxItems: 50, MaxBytes: 256 * 1024, MaxItemBytes: 32 * 1024, MaxContextItems: 20, MaxContextBytes: 48 * 1024, MaxChunks: 1024}
}
func (c Config) Validate() error {
	if !utf8.ValidString(c.SystemPrompt) || c.MaxItems < 3 || c.MaxItems > 1000 || c.MaxContextItems < 2 || c.MaxContextItems > c.MaxItems || c.MaxItemBytes < 1 || c.MaxItemBytes > 1<<20 || c.MaxBytes < 4*c.MaxItemBytes+len(c.SystemPrompt) || c.MaxBytes > 16<<20 || c.MaxContextBytes < len(c.SystemPrompt)+c.MaxItemBytes || c.MaxContextBytes > c.MaxBytes || c.MaxChunks < 1 || c.MaxChunks > 4096 {
		return ErrLimit
	}
	return nil
}

type Chunk struct {
	Sequence                         int
	Text                             string // semantic source text, before pronunciation/normalization
	StartFrame, EndFrame, SentFrames int64
	Played                           bool
}
type Item struct {
	ID                                        string
	Role                                      Role
	Content                                   string // generated content, never implicitly treated as delivered
	SentText                                  string // acknowledged by the server's text transport write
	TurnID, GenerationID                      string
	Status                                    Status
	CreatedAt                                 time.Time
	TextOnly                                  bool
	LLMDone, AudioDone                        bool
	Chunks                                    []Chunk
	GeneratedFrames, SentFrames, PlayedFrames int64
}
type Snapshot struct {
	ID    string
	Items []Item
}

// MemoryStore holds the full bounded audit state. Snapshot is a deep copy and
// the future persistence boundary; persistence must happen outside Session.mu.
type MemoryStore struct{ items []Item }

func (m *MemoryStore) Snapshot() []Item {
	result := append([]Item(nil), m.items...)
	for i := range result {
		result[i].Chunks = append([]Chunk(nil), result[i].Chunks...)
	}
	return result
}

type Runtime struct {
	ID     string
	config Config
	store  MemoryStore
}

func New(id, systemID string, c Config) (*Runtime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	r := &Runtime{ID: id, config: c}
	r.store.items = []Item{{ID: systemID, Role: System, Content: c.SystemPrompt, Status: Committed, CreatedAt: time.Now().UTC()}}
	return r, nil
}
func (r *Runtime) Snapshot() Snapshot { return Snapshot{r.ID, r.store.Snapshot()} }
func (r *Runtime) User(turn string) *Item {
	for i := range r.store.items {
		p := &r.store.items[i]
		if p.Role == User && p.TurnID == turn {
			return p
		}
	}
	return nil
}
func (r *Runtime) Assistant(gen string) *Item {
	for i := range r.store.items {
		p := &r.store.items[i]
		if p.Role == Assistant && p.GenerationID == gen {
			return p
		}
	}
	return nil
}
func (r *Runtime) CommitUser(id, turn, text string) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	if r.User(turn) != nil {
		return false, nil
	}
	if !utf8.ValidString(text) || len(text) > r.config.MaxItemBytes {
		return false, ErrLimit
	}
	r.store.items = append(r.store.items, Item{ID: id, Role: User, Content: text, TurnID: turn, Status: Committed, CreatedAt: time.Now().UTC()})
	r.trim(turn)
	return true, nil
}
func (r *Runtime) BeginAssistant(id, turn, gen string, textOnly bool) {
	if r.Assistant(gen) != nil {
		return
	}
	r.store.items = append(r.store.items, Item{ID: id, Role: Assistant, TurnID: turn, GenerationID: gen, Status: Pending, CreatedAt: time.Now().UTC(), TextOnly: textOnly})
	r.trim(turn)
}
func (r *Runtime) Append(gen, text string, sent bool) error {
	p := r.Assistant(gen)
	if p == nil || p.Status != Pending {
		return errors.New("assistant is not pending")
	}
	if !utf8.ValidString(text) {
		return ErrLimit
	}
	if sent {
		if len(p.SentText)+len(text) > r.config.MaxItemBytes {
			return ErrLimit
		}
		p.SentText += text
	} else {
		if p.LLMDone || len(p.Content)+len(text) > r.config.MaxItemBytes {
			return ErrLimit
		}
		p.Content += text
	}
	r.trim(p.TurnID)
	return nil
}
func (r *Runtime) AddChunk(gen string, sequence int, text string, start, end int64) error {
	p := r.Assistant(gen)
	if p == nil || p.Status != Pending || p.TextOnly {
		return errors.New("assistant is not pending audio")
	}
	if sequence != len(p.Chunks) || start < 0 || end <= start || (sequence > 0 && start != p.Chunks[sequence-1].EndFrame) {
		return errors.New("invalid semantic frame range")
	}
	bytes := len(text)
	for _, c := range p.Chunks {
		bytes += len(c.Text)
	}
	if len(p.Chunks) >= r.config.MaxChunks || bytes > r.config.MaxItemBytes {
		return ErrLimit
	}
	p.Chunks = append(p.Chunks, Chunk{Sequence: sequence, Text: text, StartFrame: start, EndFrame: end})
	p.GeneratedFrames = end
	r.trim(p.TurnID)
	return nil
}
func (r *Runtime) Progress(gen string, sent, played int64) {
	p := r.Assistant(gen)
	if p == nil || p.Status != Pending || p.TextOnly {
		return
	}
	if sent > p.GeneratedFrames {
		sent = p.GeneratedFrames
	}
	if sent > p.SentFrames {
		p.SentFrames = sent
	}
	if played > p.SentFrames {
		played = p.SentFrames
	}
	if played > p.PlayedFrames {
		p.PlayedFrames = played
	}
	sent, played = p.SentFrames, p.PlayedFrames
	for i := range p.Chunks {
		c := &p.Chunks[i]
		n := sent - c.StartFrame
		if n < 0 {
			n = 0
		}
		if n > c.EndFrame-c.StartFrame {
			n = c.EndFrame - c.StartFrame
		}
		if n > c.SentFrames {
			c.SentFrames = n
		}
		if sent >= c.EndFrame && played >= c.EndFrame {
			c.Played = true
		}
	}
	r.complete(p)
}
func (r *Runtime) Done(gen string, audio bool) {
	p := r.Assistant(gen)
	if p == nil || p.Status != Pending {
		return
	}
	if audio {
		p.AudioDone = true
	} else {
		p.LLMDone = true
	}
	r.complete(p)
}
func (r *Runtime) complete(p *Item) {
	if !p.LLMDone {
		return
	}
	if p.TextOnly {
		if p.Content == p.SentText {
			p.Status = Completed
		}
		return
	}
	if !p.AudioDone {
		return
	}
	if len(p.Chunks) == 0 {
		p.Status = Cancelled
		return
	}
	for _, c := range p.Chunks {
		if !c.Played {
			return
		}
	}
	p.Status = Completed
}
func (r *Runtime) Finalize(gen string) {
	p := r.Assistant(gen)
	if p == nil || p.Status != Pending {
		return
	}
	r.complete(p)
	if p.Status != Pending {
		return
	}
	if delivered(*p) == "" && p.PlayedFrames == 0 {
		p.Status = Cancelled
	} else {
		p.Status = Interrupted
	}
}
func delivered(p Item) string {
	if p.TextOnly {
		return p.SentText
	}
	var text strings.Builder
	for _, c := range p.Chunks {
		if !c.Played {
			break
		}
		text.WriteString(c.Text)
	}
	return text.String()
}
func (r *Runtime) size() int {
	n := 0
	for _, p := range r.store.items {
		n += len(p.Content) + len(p.SentText)
		for _, c := range p.Chunks {
			n += len(c.Text)
		}
	}
	return n
}

// Evict whole oldest exchanges, preserving system + the current exchange.
func (r *Runtime) trim(current string) {
	for len(r.store.items) > r.config.MaxItems || r.size() > r.config.MaxBytes {
		if len(r.store.items) < 2 || r.store.items[1].TurnID == current {
			return
		}
		turn := r.store.items[1].TurnID
		n := 1
		for n < len(r.store.items) && r.store.items[n].TurnID == turn {
			n++
		}
		kept := append(r.store.items[:1], r.store.items[n:]...)
		clear(r.store.items[len(kept):])
		r.store.items = kept
	}
}

// Context is a separate window. Pending/cancelled assistants and orphaned
// assistant input are excluded. Groups are kept whole; text is never sliced.
// candidate is read-only speculative input, not a history write.
func (r *Runtime) Context(candidate string) (llm.Request, error) {
	if len(candidate) > r.config.MaxItemBytes || !utf8.ValidString(candidate) {
		return llm.Request{}, ErrLimit
	}
	groups := [][]llm.Message{}
	turn := ""
	var group []llm.Message
	flush := func() {
		if len(group) > 0 {
			groups = append(groups, group)
		}
		group = nil
	}
	for _, p := range r.store.items[1:] {
		if p.TurnID != turn {
			flush()
			turn = p.TurnID
		}
		if p.Role == User && p.Status == Committed {
			group = append(group, llm.Message{Role: string(User), Content: p.Content})
		}
		if p.Role == Assistant && (p.Status == Completed || p.Status == Interrupted) && len(group) > 0 {
			if text := delivered(p); text != "" {
				group = append(group, llm.Message{Role: string(Assistant), Content: text})
			}
		}
	}
	flush()
	if strings.TrimSpace(candidate) != "" {
		groups = append(groups, []llm.Message{{Role: string(User), Content: candidate}})
	}
	bytes := len(r.config.SystemPrompt)
	count := 1
	first := len(groups)
	for i := len(groups) - 1; i >= 0; i-- {
		n := 0
		for _, m := range groups[i] {
			n += len(m.Content)
		}
		if bytes+n > r.config.MaxContextBytes || count+len(groups[i]) > r.config.MaxContextItems {
			break
		}
		bytes += n
		count += len(groups[i])
		first = i
	}
	request := llm.Request{Messages: []llm.Message{{Role: string(System), Content: r.config.SystemPrompt}}}
	for _, g := range groups[first:] {
		request.Messages = append(request.Messages, g...)
	}
	return request, nil
}
