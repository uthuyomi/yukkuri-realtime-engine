package whispercpp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func runTestServer() {
	args := map[string]string{}
	for i := 1; i+1 < len(os.Args); i++ {
		args[os.Args[i]] = os.Args[i+1]
	}
	if os.Getenv("YUKKURI_TEST_STT_SERVER") == "cuda" {
		fmt.Fprintln(os.Stderr, "whisper_backend_init_gpu: using CUDA0 backend\nwhisper_model_load: CUDA0 total size = 100.00 MB")
	}
	var calls atomic.Int32
	prefix := args["--request-path"]
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc(prefix+"/inference", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		_ = os.WriteFile(os.Getenv("YUKKURI_TEST_STT_MARKER"), []byte(strconv.Itoa(int(n))), 0600)
		if n > 1 && os.Getenv("YUKKURI_TEST_STT_SERVER") == "block" {
			<-r.Context().Done()
			return
		}
		if n > 1 && os.Getenv("YUKKURI_TEST_STT_SERVER") == "crash" {
			os.Exit(3)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		if r.FormValue("beam_size") != "5" || r.FormValue("best_of") != "5" {
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"text":""}`)
	})
	_ = http.ListenAndServe("127.0.0.1:"+args["--port"], mux)
}
func helperConfig(t *testing.T, mode string) RuntimeConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	model := filepath.Join(dir, "model.bin")
	if os.WriteFile(model, []byte("model"), 0600) != nil {
		t.Fatal("write model")
	}
	t.Setenv("YUKKURI_TEST_STT_SERVER", mode)
	t.Setenv("YUKKURI_TEST_STT_MARKER", filepath.Join(dir, "calls"))
	c := DefaultRuntimeConfig()
	c.Device = "cpu"
	c.CPUExecutable = exe
	c.CUDAExecutable = exe
	c.ModelPath = model
	c.InitTimeout = 5 * time.Second
	return c
}
func TestUpstreamHTTPWorkerReuseAndReap(t *testing.T) {
	c := helperConfig(t, "cpu")
	p, e := NewRuntime(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	w := p.worker.(*serverWorker)
	for range 3 {
		if r, e := p.Transcribe(context.Background(), probeRequest()); e != nil || r.Text != "" {
			t.Fatal(r, e)
		}
	}
	calls, _ := os.ReadFile(os.Getenv("YUKKURI_TEST_STT_MARKER"))
	if string(calls) != "4" {
		t.Fatal("probe + three requests must use same model process", string(calls))
	}
	p.Close()
	if w.alive() {
		t.Fatal("child survived shutdown")
	}
	if _, e = os.Stat(w.dir); !os.IsNotExist(e) {
		t.Fatal("private temp dir survived")
	}
}
func TestUpstreamWorkerCancelTimeoutAndCrashReap(t *testing.T) {
	for _, mode := range []string{"block", "crash"} {
		t.Run(mode, func(t *testing.T) {
			c := helperConfig(t, mode)
			p, e := NewRuntime(context.Background(), c)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			w := p.worker.(*serverWorker)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			r, e := p.Transcribe(ctx, probeRequest())
			if e == nil || r != nil {
				t.Fatal("failed worker produced result", r, e)
			}
			if w.alive() {
				t.Fatal("worker not reaped")
			}
		})
	}
}
func TestCUDAProbeRejectsUpstreamSilentCPUFallback(t *testing.T) {
	c := helperConfig(t, "cpu")
	c.Device = "cuda"
	p, e := NewRuntime(context.Background(), c)
	defer p.Close()
	if e == nil || p.RuntimeInfo().Available {
		t.Fatal("CPU-backed CUDA request succeeded")
	}
}
func TestCUDAProbeRequiresInferenceSuccess(t *testing.T) {
	c := helperConfig(t, "cuda")
	c.Device = "cuda"
	p, e := NewRuntime(context.Background(), c)
	defer p.Close()
	if e != nil || p.RuntimeInfo().SelectedDevice != "cuda" {
		t.Fatal(e)
	}
	// This proves integration of evidence + probe using a fake backend, NOT
	// availability of a real CUDA device on the test machine.
}

func TestInstalledPersistentCancellation(t *testing.T) {
	exe, model := os.Getenv("WHISPER_RUNTIME_TEST_EXE"), os.Getenv("WHISPER_RUNTIME_TEST_MODEL")
	if exe == "" || model == "" {
		t.Skip("installed persistent runtime smoke not requested")
	}
	c := DefaultRuntimeConfig()
	c.Device = "cpu"
	c.CPUExecutable = exe
	c.ModelPath = model
	p, err := NewRuntime(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	w := p.worker.(*serverWorker)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := p.Transcribe(ctx, probeRequest())
	if result != nil || err != context.DeadlineExceeded {
		t.Fatal("cancelled inference escaped", err)
	}
	if time.Since(start) > 3*time.Second || w.alive() {
		t.Fatal("installed worker did not exit promptly")
	}
	if p.RuntimeInfo().State != "reload_required" {
		t.Fatal(p.RuntimeInfo())
	}
}
