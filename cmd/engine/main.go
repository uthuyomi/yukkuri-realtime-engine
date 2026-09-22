package main

import (
	"context"
	"io"
	"log"
	"os"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts/aquestalk"
)

func main() {
	log.Println("Yukkuri Realtime Engine starting...")

	e := engine.New()

	aq, err := aquestalk.New(aquestalk.Config{
		DLLPath: `internal\providers\tts\aquestalk\aqtk1_win\lib64\f1\AquesTalk.dll`,
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := e.RegisterTTS(aq); err != nil {
		log.Fatal(err)
	}

	stream, err := e.Synthesize(
		context.Background(),
		"aquestalk",
		tts.Request{
			Text:  "ゆっくりしていってね",
			Voice: "f1",
			Speed: 1.0,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Audio.Close()

	out, err := os.Create("yukkuri.wav")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	if _, err := io.Copy(out, stream.Audio); err != nil {
		log.Fatal(err)
	}

	log.Printf(
		"synthesis complete: yukkuri.wav (%d Hz, %d channel)",
		stream.Format.SampleRate,
		stream.Format.Channels,
	)
}
