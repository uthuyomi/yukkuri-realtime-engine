package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts/aquestalk"
	httptransport "github.com/uthuyomi/yukkuri-realtime-engine/internal/transport/http"
)

func main() {
	log.Println("Yukkuri Realtime Engine starting...")

	e := engine.New()

	const aqRoot = `internal\providers\tts\aquestalk\aqtk1_win\lib64`
	const aqk2kRoot = `internal\providers\tts\aquestalk\aqk2k_win`

	aq, err := aquestalk.New(aquestalk.Config{
		DefaultVoice: "f1",

		Voices: map[string]string{
			"f1":   filepath.Join(aqRoot, "f1", "AquesTalk.dll"),
			"f2":   filepath.Join(aqRoot, "f2", "AquesTalk.dll"),
			"f3":   filepath.Join(aqRoot, "f3", "AquesTalk.dll"),
			"m1":   filepath.Join(aqRoot, "m1", "AquesTalk.dll"),
			"m2":   filepath.Join(aqRoot, "m2", "AquesTalk.dll"),
			"r1":   filepath.Join(aqRoot, "r1", "AquesTalk.dll"),
			"dvd":  filepath.Join(aqRoot, "dvd", "AquesTalk.dll"),
			"imd1": filepath.Join(aqRoot, "imd1", "AquesTalk.dll"),
			"jgr":  filepath.Join(aqRoot, "jgr", "AquesTalk.dll"),
		},

		Kanji2KoeDLL: filepath.Join(
			aqk2kRoot,
			"lib64",
			"AqKanji2Koe.dll",
		),

		Kanji2KoeDic: filepath.Join(
			aqk2kRoot,
			"aq_dic",
		),
	})
	if err != nil {
		log.Fatal(err)
	}

	defer aq.Close()

	if err := e.RegisterTTS(aq); err != nil {
		log.Fatal(err)
	}

	log.Printf(
		"AquesTalk voices loaded: %v",
		aq.VoiceNames(),
	)

	server := httptransport.New(
		httptransport.Config{
			Address: "127.0.0.1:8765",
		},
		e,
	)

	errCh := make(chan error, 1)

	go func() {
		if err := server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()

	signalCh := make(chan os.Signal, 1)

	signal.Notify(
		signalCh,
		os.Interrupt,
		syscall.SIGTERM,
	)

	select {
	case sig := <-signalCh:
		log.Printf("received signal: %s", sig)

	case err := <-errCh:
		log.Fatalf("HTTP server failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	log.Println("Yukkuri Realtime Engine stopped")
}
