package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/backchannel/multisignal"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm/openai"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt/whispercpp"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts/aquestalk"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection/smartturn"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
	httptransport "github.com/uthuyomi/yukkuri-realtime-engine/internal/transport/http"
)

func main() {

	if err := godotenv.Load(); err != nil {
		log.Printf(".env not loaded: %v", err)
	}

	log.Println("Yukkuri Realtime Engine starting...")
	engineContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	e := engine.New()

	const aqRoot = `internal\providers\tts\aquestalk\aqtk1_win\lib64`
	const aqk2kRoot = `internal\providers\tts\aquestalk\aqk2k_win`

	// --------------------------------------------------
	// TTS: AquesTalk
	// --------------------------------------------------

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
		log.Printf("TTS unavailable: %v", err)
	} else {
		defer aq.Close()
		if err := e.RegisterTTS(aq); err != nil {
			log.Fatal(err)
		}
		log.Println("TTS configured")
	}

	// --------------------------------------------------
	// STT: whisper.cpp
	// --------------------------------------------------

	sttConfig, sttConfigErr := whispercpp.RuntimeConfigFromEnv(os.Getenv)
	var whisper *whispercpp.Runtime
	if sttConfigErr != nil {
		log.Println("STT unavailable: invalid runtime configuration")
	} else {
		whisper, err = whispercpp.NewRuntime(engineContext, sttConfig)
		if whisper != nil {
			defer whisper.Close()
			info := whisper.RuntimeInfo()
			log.Printf("STT runtime=%s requested_device=%s selected_device=%s model=%s persistent=%v available=%v fallback_from=%s fallback_reason=%s", info.Backend, info.RequestedDevice, info.SelectedDevice, info.Model, info.Persistent, info.Available, info.FallbackFrom, info.FallbackReason)
		}
		if err != nil {
			log.Println("STT unavailable: initialization failed")
		}
	}
	if engineContext.Err() != nil {
		return
	}

	// --------------------------------------------------
	// LLM: OpenAI
	// --------------------------------------------------

	openAIAPIKey := os.Getenv(
		"OPENAI_API_KEY",
	)

	openAIModel := os.Getenv(
		"OPENAI_MODEL",
	)

	if openAIModel == "" {
		openAIModel = "gpt-5.6-luna"
	}

	var openAIProvider *openai.Provider
	if openAIAPIKey != "" {
		openAIProvider, err = openai.New(openai.Config{APIKey: openAIAPIKey, Model: openAIModel})
		if err != nil {
			log.Println("LLM unavailable: invalid configuration")
		} else {
			log.Println("LLM configured")
		}
	} else {
		log.Println("LLM unavailable: not configured")
	}

	// --------------------------------------------------
	// HTTP / Realtime server
	// --------------------------------------------------

	server := httptransport.New(
		httptransport.Config{
			Address:        "127.0.0.1:8765",
			AllowedOrigins: strings.FieldsFunc(os.Getenv("API_ALLOWED_ORIGINS"), func(r rune) bool { return r == ',' }),
		},
		e,
	)

	if whisper != nil {
		server.SetSTTProvider(whisper)
	}

	if openAIProvider != nil {
		server.SetLLMProvider(openAIProvider)
	}
	turnURL := os.Getenv("TURN_DETECTOR_URL")
	if turnURL == "" {
		turnURL = "http://127.0.0.1:8766/predict"
	}
	turnProvider, err := smartturn.New(turnURL)
	if err != nil {
		log.Fatal(err)
	}
	endpoint := realtime.DefaultEndpointConfig()
	for key, target := range map[string]*time.Duration{
		"TURN_MIN_DELAY":    &endpoint.MinDelay,
		"TURN_MAX_DELAY":    &endpoint.MaxDelay,
		"TURN_MAX_DURATION": &endpoint.MaxTurnDuration,
	} {
		if value := os.Getenv(key); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				log.Fatalf("%s: %v", key, err)
			}
			*target = parsed
		}
	}
	if err := server.SetTurnDetector(turnProvider, endpoint); err != nil {
		log.Fatal(err)
	}
	log.Printf("Turn detector: %s endpoint=%s min=%s max=%s", turnProvider.Name(), turnURL, endpoint.MinDelay, endpoint.MaxDelay)
	interruptionConfig := realtime.DefaultInterruptionConfig()
	if value := os.Getenv("INTERRUPTION_DECISION_WINDOW"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			log.Fatal(err)
		}
		interruptionConfig.DecisionWindow = d
	}
	acousticRecovery := true
	if value := os.Getenv("BACKCHANNEL_ACOUSTIC_RECOVERY"); value != "" {
		v, err := strconv.ParseBool(value)
		if err != nil {
			log.Fatal(err)
		}
		acousticRecovery = v
	}
	if err := server.SetBackchannelProvider(&multisignal.Policy{AllowAcousticRecovery: acousticRecovery}, interruptionConfig); err != nil {
		log.Fatal(err)
	}
	log.Printf("Interruption policy: multisignal-ja-v1 window=%s acoustic_recovery=%v", interruptionConfig.DecisionWindow, acousticRecovery)
	speculation := realtime.DefaultSpeculationConfig()
	if value := os.Getenv("SPECULATION_ENABLED"); value != "" {
		v, err := strconv.ParseBool(value)
		if err != nil {
			log.Fatal(err)
		}
		speculation.Enabled = v
	}
	for key, target := range map[string]*time.Duration{
		"SPECULATION_TIMEOUT":  &speculation.Timeout,
		"SPECULATION_COOLDOWN": &speculation.Cooldown,
	} {
		if value := os.Getenv(key); value != "" {
			v, err := time.ParseDuration(value)
			if err != nil {
				log.Fatalf("%s: %v", key, err)
			}
			*target = v
		}
	}
	if err := server.SetSpeculationConfig(speculation); err != nil {
		log.Fatal(err)
	}
	log.Printf("Speculation: enabled=%v timeout=%s cooldown=%s transcript_limit=%d delta_limit=%d", speculation.Enabled, speculation.Timeout, speculation.Cooldown, speculation.MaxTranscriptBytes, speculation.MaxDeltaBytes)

	// --------------------------------------------------
	// Start server
	// --------------------------------------------------

	errCh := make(chan error, 1)

	go func() {
		if err := server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-engineContext.Done():
		log.Println("shutdown requested")

	case err := <-errCh:
		log.Printf("HTTP server failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf(
			"HTTP shutdown error: %v",
			err,
		)
	}

	log.Println(
		"Yukkuri Realtime Engine stopped",
	)
}
