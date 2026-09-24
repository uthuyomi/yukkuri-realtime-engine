package whispercpp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

type fakeWorker struct {
	closed atomic.Bool
	calls  atomic.Int32
	fn     func(context.Context) (*stt.Result, error)
}

func (w *fakeWorker) alive() bool              { return !w.closed.Load() }
func (w *fakeWorker) close()                   { w.closed.Store(true) }
func (w *fakeWorker) measurement() Measurement { return Measurement{} }
func (w *fakeWorker) infer(ctx context.Context, _ stt.Request) (*stt.Result, error) {
	w.calls.Add(1)
	if w.fn != nil {
		return w.fn(ctx)
	}
	return &stt.Result{Text: "", Language: "ja"}, nil
}

func TestRuntimeDeviceSelection(t *testing.T) {
	for _, tc := range []struct {
		name, device string
		failCUDA     bool
		want         []string
		selected     string
		fail         bool
	}{
		{"explicit CPU", "cpu", false, []string{"cpu"}, "cpu", false},
		{"explicit CUDA", "cuda", false, []string{"cuda"}, "cuda", false},
		{"explicit unavailable", "cuda", true, []string{"cuda"}, "", true},
		{"auto CUDA", "auto", false, []string{"cuda"}, "cuda", false},
		{"auto fallback", "auto", true, []string{"cuda", "cpu"}, "cpu", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultRuntimeConfig()
			c.Device = tc.device
			var attempts []string
			p, err := newRuntime(context.Background(), c, func(_ context.Context, got RuntimeConfig, d string) (worker, error) {
				if got.ModelName != "small" || got.ModelPath != c.ModelPath {
					t.Fatal("device changed model")
				}
				attempts = append(attempts, d)
				if d == "cuda" && tc.failCUDA {
					return nil, errors.New("private C:/secret/CUDA SDK response")
				}
				return &fakeWorker{}, nil
			})
			defer p.Close()
			if (err != nil) != tc.fail || !reflect.DeepEqual(attempts, tc.want) {
				t.Fatalf("%v %v", err, attempts)
			}
			info := p.RuntimeInfo()
			if info.SelectedDevice != tc.selected || info.Available == tc.fail || info.Persistent != true {
				t.Fatal(info)
			}
			if tc.device == "auto" && tc.failCUDA && info.FallbackReason != "cuda_initialization_failed" {
				t.Fatal(info)
			}
			if err != nil && err.Error() != stt.ErrUnavailable.Error() {
				t.Fatal("raw error exposed", err)
			}
		})
	}
}
func TestAutoInitializationTimeoutHasIndependentCPUFallbackBudget(t *testing.T) {
	c := DefaultRuntimeConfig()
	c.InitTimeout = 10 * time.Millisecond
	p, err := newRuntime(context.Background(), c, func(ctx context.Context, _ RuntimeConfig, d string) (worker, error) {
		if d == "cuda" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if ctx.Err() != nil {
			t.Fatal("CPU inherited expired timeout")
		}
		return &fakeWorker{}, nil
	})
	defer p.Close()
	if err != nil || p.RuntimeInfo().SelectedDevice != "cpu" {
		t.Fatal(err)
	}
}
func TestPersistentReuseAndShutdown(t *testing.T) {
	var loads int
	w := &fakeWorker{}
	c := DefaultRuntimeConfig()
	c.Device = "cpu"
	p, err := newRuntime(context.Background(), c, func(context.Context, RuntimeConfig, string) (worker, error) { loads++; return w, nil })
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		r, e := p.Transcribe(context.Background(), probeRequest())
		if e != nil || r.Text != "" {
			t.Fatal(r, e)
		}
	}
	if loads != 1 || w.calls.Load() != 3 {
		t.Fatal("model not reused", loads, w.calls.Load())
	}
	p.Close()
	p.Close()
	if !w.closed.Load() {
		t.Fatal("worker survived")
	}
	if _, err = p.Transcribe(context.Background(), probeRequest()); !errors.Is(err, stt.ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestCancellationDiscardsLateResultAndReloads(t *testing.T) {
	started := make(chan struct{})
	w := &fakeWorker{fn: func(ctx context.Context) (*stt.Result, error) {
		close(started)
		<-ctx.Done()
		return &stt.Result{Text: "obsolete"}, nil
	}}
	c := DefaultRuntimeConfig()
	c.Device = "cpu"
	loads := 0
	p, _ := newRuntime(context.Background(), c, func(context.Context, RuntimeConfig, string) (worker, error) {
		loads++
		if loads == 1 {
			return w, nil
		}
		return &fakeWorker{}, nil
	})
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		r, e := p.Transcribe(ctx, probeRequest())
		if r != nil {
			done <- errors.New("obsolete result escaped")
		} else {
			done <- e
		}
	}()
	<-started
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if !w.closed.Load() {
		t.Fatal("worker not reaped")
	}
	if _, e := p.Transcribe(context.Background(), probeRequest()); e != nil || loads != 2 {
		t.Fatal(e, loads)
	}
	if p.RuntimeInfo().FallbackFrom != "" {
		t.Fatal("cancellation treated as device failure")
	}
}
func TestBoundedRuntimeQueueAndShutdownCancellation(t *testing.T) {
	c := DefaultRuntimeConfig()
	c.Device = "cpu"
	c.QueueCapacity = 2
	entered := make(chan struct{})
	p, _ := newRuntime(context.Background(), c, func(context.Context, RuntimeConfig, string) (worker, error) {
		return &fakeWorker{fn: func(ctx context.Context) (*stt.Result, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }}, nil
	})
	done := make(chan error, 2)
	go func() { _, e := p.Transcribe(context.Background(), probeRequest()); done <- e }()
	<-entered
	go func() { _, e := p.Transcribe(context.Background(), probeRequest()); done <- e }()
	deadline := time.Now().Add(time.Second)
	for len(p.queue) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, e := p.Transcribe(context.Background(), probeRequest()); !errors.Is(e, stt.ErrCapacity) {
		t.Fatal(e)
	}
	p.Close()
	for range 2 {
		if e := <-done; !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	}
}
func TestCrashIsolationAndFallbackPolicy(t *testing.T) {
	for _, device := range []string{"auto", "cuda", "cpu"} {
		t.Run(device, func(t *testing.T) {
			c := DefaultRuntimeConfig()
			c.Device = device
			attempts := []string{}
			w := &fakeWorker{fn: func(context.Context) (*stt.Result, error) { return nil, errors.New("private model path") }}
			p, _ := newRuntime(context.Background(), c, func(_ context.Context, _ RuntimeConfig, d string) (worker, error) {
				attempts = append(attempts, d)
				if len(attempts) == 1 {
					return w, nil
				}
				return &fakeWorker{}, nil
			})
			defer p.Close()
			if _, e := p.Transcribe(context.Background(), probeRequest()); !errors.Is(e, stt.ErrRuntime) {
				t.Fatal(e)
			}
			if !w.closed.Load() {
				t.Fatal("crashed worker not reaped")
			}
			if _, e := p.Transcribe(context.Background(), probeRequest()); e != nil {
				t.Fatal(e)
			}
			want := device
			if device == "auto" {
				want = "cpu"
			}
			if attempts[1] != want {
				t.Fatal(attempts)
			}
		})
	}
}
func TestRuntimeTimeoutAndValidation(t *testing.T) {
	c := DefaultRuntimeConfig()
	c.Device = "cpu"
	c.InferenceTimeout = 10 * time.Millisecond
	p, _ := newRuntime(context.Background(), c, func(context.Context, RuntimeConfig, string) (worker, error) {
		return &fakeWorker{fn: func(ctx context.Context) (*stt.Result, error) { <-ctx.Done(); return nil, ctx.Err() }}, nil
	})
	defer p.Close()
	for _, r := range []stt.Request{{}, {Audio: []byte{0}, Format: probeRequest().Format}, {Audio: []byte{0, 0}, Format: stt.AudioFormat{SampleRate: 48000, Channels: 1, Encoding: "pcm_s16le"}}, {Audio: make([]byte, MaxRuntimeAudioBytes+2), Format: probeRequest().Format}} {
		if _, e := p.Transcribe(context.Background(), r); e == nil {
			t.Fatal("invalid PCM accepted")
		}
	}
	if _, e := p.Transcribe(context.Background(), probeRequest()); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestEnvironmentModelDeviceSeparation(t *testing.T) {
	for _, device := range []string{"cpu", "auto", "cuda"} {
		c, e := RuntimeConfigFromEnv(func(k string) string {
			return map[string]string{"STT_DEVICE": device, "STT_MODEL": "small", "STT_LANGUAGE": "auto"}[k]
		})
		if e != nil || c.ModelName != "small" || c.Device != device || c.Language != "auto" {
			t.Fatal(c, e)
		}
	}
	for _, key := range []string{"STT_DEVICE", "STT_RUNTIME", "STT_MODEL", "STT_LANGUAGE", "STT_THREADS", "STT_QUEUE_CAPACITY"} {
		if _, e := RuntimeConfigFromEnv(func(k string) string {
			if k == key {
				return "invalid/private/path"
			}
			return ""
		}); e == nil {
			t.Fatal(key)
		}
	}
	c, e := RuntimeConfigFromEnv(func(string) string { return "" })
	if e != nil || c.Device != "auto" || c.BeamSize != 5 || c.BestOf != 5 || c.Threads != 4 {
		t.Fatal(c, e)
	}
}
func TestBackendEvidenceIsNotGPUNameOrUseGPUFlag(t *testing.T) {
	d := &diagnostics{}
	d.Write([]byte("use_gpu=1\nGPU GTX 1660\n"))
	if d.cudaOK() {
		t.Fatal("weak evidence accepted")
	}
	d.Write([]byte("whisper_backend_init_gpu: using CUDA0 backend\n"))
	if d.cudaOK() {
		t.Fatal("allocation not proven")
	}
	d.Write([]byte("whisper_model_load:        CUDA0 total size = 123.45 MB\n"))
	if !d.cudaOK() {
		t.Fatal("evidence missing")
	}
	d.Write([]byte("whisper_backend_init_gpu: failed to initialize CUDA0 backend\n"))
	if d.cudaOK() {
		t.Fatal("failure ignored")
	}
	d.Write([]byte(strings.Repeat("x", 100000)))
	if len(d.pending) > 4096 {
		t.Fatal("unbounded stderr")
	}
}
func TestCPUArgumentsDisableGPU(t *testing.T) {
	cpu := strings.Join(commonArgs(DefaultRuntimeConfig(), "cpu"), " ")
	cuda := strings.Join(commonArgs(DefaultRuntimeConfig(), "cuda"), " ")
	if !strings.Contains(cpu, "-ng") || strings.Contains(cuda, "-ng") || !strings.Contains(cpu, "-bo 5 -bs 5") {
		t.Fatal(cpu, cuda)
	}
}
func TestMissingCUDAExecutableNeverUsesCPU(t *testing.T) {
	c := DefaultRuntimeConfig()
	c.Device = "cuda"
	c.CUDAExecutable = filepath.Join(t.TempDir(), "missing.exe")
	c.ModelPath = os.Args[0]
	p, e := NewRuntime(context.Background(), c)
	defer p.Close()
	if !errors.Is(e, stt.ErrUnavailable) || p.RuntimeInfo().SelectedDevice != "" || p.RuntimeInfo().FallbackFrom != "" {
		t.Fatal(e, p.RuntimeInfo())
	}
}
