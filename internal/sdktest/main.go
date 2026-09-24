// Command sdktest serves deterministic fake providers through the real Public API.
// It is a test fixture, not an engine entry point or production provider.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	httptransport "github.com/uthuyomi/yukkuri-realtime-engine/internal/transport/http"
)

type speech struct{}

func (speech) Name() string { return "fixture" }
func (speech) Synthesize(ctx context.Context, r tts.Request) (*tts.Stream, error) {
	if r.Text == "fail" {
		return nil, fmt.Errorf("private fixture provider error")
	}
	if r.Text == "slow" {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	pcm := make([]byte, 3200)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return &tts.Stream{Format: tts.AudioFormat{Codec: "wav", SampleRate: 16000, Channels: 1}, Audio: io.NopCloser(bytes.NewReader(b.Bytes()))}, nil
}

type transcript struct{}

func (transcript) Name() string { return "fixture" }
func (transcript) Transcribe(ctx context.Context, r stt.Request) (*stt.Result, error) {
	if len(r.Audio) > 0 && r.Audio[0] == 127 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return &stt.Result{Text: "fixture transcript", Language: "en"}, nil
}

type language struct{}

func (language) Name() string { return "fixture" }
func (language) Generate(ctx context.Context, r llm.Request) (llm.Stream, error) {
	slow := len(r.Messages) > 0 && r.Messages[len(r.Messages)-1].Content == "slow-generation"
	return &deltas{ctx: ctx, slow: slow}, nil
}

type deltas struct {
	ctx  context.Context
	n    int
	slow bool
}

func (d *deltas) Recv() (llm.Delta, error) {
	if d.slow {
		select {
		case <-d.ctx.Done():
			return llm.Delta{}, d.ctx.Err()
		case <-time.After(5 * time.Second):
		}
		d.slow = false
	}
	if d.ctx.Err() != nil {
		return llm.Delta{}, d.ctx.Err()
	}
	d.n++
	if d.n == 1 {
		return llm.Delta{Text: "Hello. "}, nil
	}
	if d.n == 2 {
		return llm.Delta{Text: "SDK fixture."}, nil
	}
	return llm.Delta{}, io.EOF
}
func (*deltas) Close() error { return nil }
func main() {
	addr := "127.0.0.1:18765"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	e := engine.New()
	e.RegisterTTS(speech{})
	s := httptransport.New(httptransport.Config{Address: addr}, e)
	s.SetSTTProvider(transcript{})
	s.SetLLMProvider(language{})
	log.Fatal(s.ListenAndServe())
}
