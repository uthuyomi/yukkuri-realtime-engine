package whispercpp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

// The test binary acts as a long-running whisper-cli child, exercising the
// provider's real exec.CommandContext/Run path without loading a model.
func TestMain(m *testing.M) {
	if os.Getenv("YUKKURI_TEST_STT_SERVER") != "" {
		runTestServer()
		os.Exit(0)
	}
	if marker := os.Getenv("YUKKURI_TEST_WHISPER_CHILD"); marker != "" {
		for i, arg := range os.Args {
			if arg == "-f" && i+1 < len(os.Args) {
				_ = os.WriteFile(marker, []byte(os.Args[i+1]), 0600)
			}
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestTranscribeCancellationReapsChildAndRemovesWAV(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	model := filepath.Join(dir, "model.bin")
	if err = os.WriteFile(model, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "started")
	t.Setenv("YUKKURI_TEST_WHISPER_CHILD", marker)
	p, err := New(Config{Executable: exe, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.Transcribe(ctx, stt.Request{Audio: make([]byte, 320), Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
		done <- err
	}()
	var wav []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		wav, _ = os.ReadFile(marker)
		if len(wav) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(wav) == 0 {
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child Run did not return after cancellation")
	}
	if _, err = os.Stat(filepath.Dir(string(wav))); !os.IsNotExist(err) {
		t.Fatal("temporary WAV directory survived", err)
	}
}

// Opt-in smoke for the installed Windows executable, with no transcript/API use.
func TestInstalledWhisperCancellation(t *testing.T) {
	exe := os.Getenv("WHISPER_CANCEL_TEST_EXE")
	model := os.Getenv("WHISPER_CANCEL_TEST_MODEL")
	if exe == "" || model == "" {
		t.Skip("installed Whisper smoke not requested")
	}
	p, err := New(Config{Executable: exe, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = p.Transcribe(ctx, stt.Request{Audio: make([]byte, 32000*10), Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected running child cancellation, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("slow child termination")
	}
}
