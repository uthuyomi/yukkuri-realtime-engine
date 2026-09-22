package tts

import (
	"context"
	"io"
)

// Request はTTSエンジンへ渡す共通リクエスト。
// AquesTalk固有の設定はProvider側へ閉じ込める。
type Request struct {
	Text  string
	Voice string
	Speed float64
}

// AudioFormat はProviderが返す音声形式。
type AudioFormat struct {
	Codec      string
	SampleRate int
	Channels   int
}

// Stream は生成された音声ストリーム。
type Stream struct {
	Format AudioFormat
	Audio  io.ReadCloser
}

// Provider はすべてのTTS Providerが実装する共通interface。
type Provider interface {
	Name() string
	Synthesize(ctx context.Context, req Request) (*Stream, error)
}
