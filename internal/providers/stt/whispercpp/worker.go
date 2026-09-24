package whispercpp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

// Only whitelisted backend evidence and numeric timings survive stderr.
// In particular, neither paths nor transcript/provider response text is logged.
type diagnostics struct {
	mu                             sync.Mutex
	pending                        []byte
	cudaBackend, cudaModel, failed bool
	metrics                        Measurement
}

var timingPattern = regexp.MustCompile(`whisper_print_timings:\s+(load|encode|decode) time\s*=\s*([0-9.]+) ms`)
var cudaBackendPattern = regexp.MustCompile(`whisper_backend_init_gpu: using CUDA[0-9]+ backend`)
var cudaModelPattern = regexp.MustCompile(`whisper_model_load:\s+CUDA[0-9]+ total size\s*=\s*([0-9.]+) MB`)

func (d *diagnostics) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, v := range b {
		if v == '\n' {
			d.line(string(d.pending))
			d.pending = d.pending[:0]
		} else if len(d.pending) < 4096 {
			d.pending = append(d.pending, v)
		}
	}
	return len(b), nil
}
func (d *diagnostics) line(s string) {
	if cudaBackendPattern.MatchString(s) {
		d.cudaBackend = true
	}
	if match := cudaModelPattern.FindStringSubmatch(s); match != nil {
		if size, err := strconv.ParseFloat(match[1], 64); err == nil && size > 0 {
			d.cudaModel = true
		}
	}
	if strings.Contains(s, "failed to initialize") || strings.Contains(s, "CUDA error") {
		d.failed = true
	}
	if m := timingPattern.FindStringSubmatch(s); m != nil {
		v, e := strconv.ParseFloat(m[2], 64)
		if e == nil {
			switch m[1] {
			case "load":
				d.metrics.LoadMS = &v
			case "encode":
				d.metrics.EncodeMS = &v
			case "decode":
				d.metrics.DecodeMS = &v
			}
		}
	}
}
func (d *diagnostics) cudaOK() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cudaBackend && d.cudaModel && !d.failed
}
func (d *diagnostics) snapshot() Measurement { d.mu.Lock(); defer d.mu.Unlock(); return d.metrics }

type child struct {
	cmd     *exec.Cmd
	done    chan struct{}
	once    sync.Once
	release func()
	diag    *diagnostics
	stats   *processStats
}

func launch(exe string, args []string, out io.Writer) (*child, error) {
	c := &child{cmd: exec.Command(exe, args...), done: make(chan struct{}), diag: &diagnostics{}}
	c.cmd.Stdout = out
	c.cmd.Stderr = c.diag
	prepareChild(c.cmd)
	start := time.Now()
	if c.cmd.Start() != nil {
		return nil, stt.ErrUnavailable
	}
	ms := float64(time.Since(start).Microseconds()) / 1000
	c.diag.mu.Lock()
	c.diag.metrics.ProcessLaunchMS = &ms
	c.diag.mu.Unlock()
	var err error
	c.release, err = containChild(c.cmd)
	if err != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
		return nil, stt.ErrUnavailable
	}
	c.stats = newProcessStats(c.cmd.Process.Pid)
	go func() { _ = c.cmd.Wait(); close(c.done) }()
	return c, nil
}
func (c *child) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *child) close() {
	c.once.Do(func() {
		if c.alive() {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
		c.stats.close()
		c.release()
	})
}

type serverWorker struct {
	child   *child
	url     string
	client  *http.Client
	cfg     RuntimeConfig
	dir     string
	metrics Measurement
}

func commonArgs(c RuntimeConfig, device string) []string {
	a := []string{"-m", c.ModelPath, "-l", c.Language, "-nt", "-t", strconv.Itoa(c.Threads), "-bo", strconv.Itoa(c.BestOf), "-bs", strconv.Itoa(c.BeamSize)}
	if device == "cpu" {
		a = append(a, "-ng")
	}
	return a
}
func startWorker(ctx context.Context, c RuntimeConfig, device string) (worker, error) {
	exe := c.CPUExecutable
	if device == "cuda" {
		exe = c.CUDAExecutable
	}
	var err error
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, stt.ErrUnavailable
	}
	c.ModelPath, err = filepath.Abs(c.ModelPath)
	if err != nil {
		return nil, stt.ErrUnavailable
	}
	for _, p := range []string{exe, c.ModelPath} {
		s, e := os.Stat(p)
		if e != nil || s.IsDir() {
			return nil, stt.ErrUnavailable
		}
	}
	if c.Mode == "process" {
		w := &processWorker{cfg: c, exe: exe, device: device}
		begin := time.Now()
		_, err = w.infer(ctx, probeRequest())
		w.metrics.ProbeMS = elapsedMS(begin)
		return w, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, stt.ErrUnavailable
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		return nil, stt.ErrUnavailable
	}
	prefix := "/" + hex.EncodeToString(token)
	dir, err := os.MkdirTemp("", "yukkuri-stt-server-")
	if err != nil {
		return nil, stt.ErrUnavailable
	}
	w := &serverWorker{cfg: c, dir: dir, url: "http://127.0.0.1:" + strconv.Itoa(port) + prefix, client: &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	args := append(commonArgs(c, device), "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--request-path", prefix, "--public", dir)
	begin := time.Now()
	w.child, err = launch(exe, args, io.Discard)
	if err != nil {
		w.close()
		return nil, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !w.alive() {
			w.close()
			return nil, stt.ErrUnavailable
		}
		probeCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, w.url+"/health", nil)
		res, e := w.client.Do(req)
		ready := e == nil && res.StatusCode == http.StatusOK
		if res != nil {
			res.Body.Close()
		}
		stop()
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			w.close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	w.metrics.RuntimeReadyMS = elapsedMS(begin)
	// A CUDA build can silently fall back inside upstream. Require actual model
	// allocation on CUDA before doing the inference compatibility probe.
	if device == "cuda" && !w.child.diag.cudaOK() {
		w.close()
		return nil, stt.ErrUnavailable
	}
	begin = time.Now()
	_, err = w.infer(ctx, probeRequest())
	w.metrics.ProbeMS = elapsedMS(begin)
	if err != nil || (device == "cuda" && !w.child.diag.cudaOK()) {
		w.close()
		return nil, stt.ErrUnavailable
	}
	return w, nil
}
func probeRequest() stt.Request {
	return stt.Request{Audio: make([]byte, 32000), Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}}
}
func elapsedMS(start time.Time) *float64 {
	ms := float64(time.Since(start).Microseconds()) / 1000
	return &ms
}
func (w *serverWorker) alive() bool { return w.child != nil && w.child.alive() }
func (w *serverWorker) close() {
	if w.child != nil {
		w.child.close()
	}
	if w.client != nil {
		w.client.CloseIdleConnections()
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
	}
}
func (w *serverWorker) measurement() Measurement {
	m := w.metrics
	if w.child != nil {
		d := w.child.diag.snapshot()
		m.ProcessLaunchMS = d.ProcessLaunchMS
		m.LoadMS = d.LoadMS
	}
	return m
}
func (w *serverWorker) infer(ctx context.Context, r stt.Request) (*stt.Result, error) {
	_, before := w.child.stats.snapshot()
	defer func() {
		peak, after := w.child.stats.snapshot()
		w.metrics.PeakWorkingSetBytes = peak
		if before != nil && after != nil {
			delta := *after - *before
			w.metrics.CPUSeconds = &delta
		}
	}()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "audio.wav")
	if err != nil {
		return nil, stt.ErrRuntime
	}
	if err = writeWAV(part, r.Audio); err != nil {
		return nil, stt.ErrRuntime
	}
	for k, v := range map[string]string{"response_format": "json", "language": w.cfg.Language, "no_timestamps": "true", "token_timestamps": "false", "best_of": strconv.Itoa(w.cfg.BestOf), "beam_size": strconv.Itoa(w.cfg.BeamSize)} {
		if form.WriteField(k, v) != nil {
			return nil, stt.ErrRuntime
		}
	}
	if form.Close() != nil {
		return nil, stt.ErrRuntime
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url+"/inference", &body)
	if err != nil {
		return nil, stt.ErrRuntime
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := w.client.Do(req)
	if err != nil {
		return nil, stt.ErrRuntime
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, stt.ErrRuntime
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(b) > 65536 {
		return nil, stt.ErrRuntime
	}
	var wire struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(b, &wire) != nil || wire.Text == nil {
		return nil, stt.ErrRuntime
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &stt.Result{Text: strings.TrimSpace(*wire.Text), Language: w.cfg.Language}, nil
}

// Fixed provider boundary is mono PCM16/16kHz. One bounded multipart copy, no
// temporary audio file for persistent inference.
func writeWAV(out io.Writer, pcm []byte) error {
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(len(pcm)+36))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], 16000)
	binary.LittleEndian.PutUint32(header[28:], 32000)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	if _, err := out.Write(header); err != nil {
		return err
	}
	_, err := out.Write(pcm)
	return err
}

type boundedText struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedText) Write(p []byte) (int, error) {
	n := len(p)
	room := 65536 - b.Len()
	if len(p) > room {
		p = p[:room]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

type processWorker struct {
	cfg         RuntimeConfig
	exe, device string
	metrics     Measurement
}

func (w *processWorker) alive() bool              { return true }
func (w *processWorker) close()                   {}
func (w *processWorker) measurement() Measurement { return w.metrics }
func (w *processWorker) infer(ctx context.Context, r stt.Request) (*stt.Result, error) {
	dir, err := os.MkdirTemp("", "yukkuri-stt-bench-")
	if err != nil {
		return nil, stt.ErrRuntime
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "audio.wav")
	if writePCM16WAV(path, r.Audio, 16000, 1) != nil {
		return nil, stt.ErrRuntime
	}
	var out boundedText
	c, err := launch(w.exe, append(commonArgs(w.cfg, w.device), "-f", path), &out)
	if err != nil {
		return nil, err
	}
	defer c.close()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
	}
	probe := w.metrics.ProbeMS
	w.metrics = c.diag.snapshot()
	w.metrics.ProbeMS = probe
	w.metrics.PeakWorkingSetBytes, w.metrics.CPUSeconds = c.stats.snapshot()
	if !c.cmd.ProcessState.Success() || out.overflow || (w.device == "cuda" && !c.diag.cudaOK()) {
		return nil, stt.ErrRuntime
	}
	return &stt.Result{Text: strings.TrimSpace(out.String()), Language: w.cfg.Language}, nil
}
