// stt-bench runs local inference, never selects a device in an SDK/client.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt/whispercpp"
)

type record struct {
	Mode             string                 `json:"mode"`
	Runtime          stt.RuntimeInfo        `json:"runtime"`
	Fixture          string                 `json:"fixture"`
	Repeat           int                    `json:"repeat"`
	AudioSeconds     float64                `json:"audio_seconds"`
	WallMS           float64                `json:"wall_ms"`
	RTF              float64                `json:"rtf"`
	EOTFinalMS       float64                `json:"eot_to_final_ms"`
	FirstPartialMS   *float64               `json:"first_partial_ms"`
	FinalizationMS   *float64               `json:"finalization_ms"`
	InitializationMS float64                `json:"initialization_ms"`
	Measurement      whispercpp.Measurement `json:"measurement"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "STT benchmark:", err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stt-bench", flag.ContinueOnError)
	mode := fs.String("mode", "persistent", "persistent, process, or legacy (exact old CLI flags)")
	device := fs.String("device", "auto", "auto, cpu, cuda; legacy requires cpu")
	model := fs.String("model", "small", "model identifier, independent of device")
	modelPath := fs.String("model-path", "", "custom model file")
	cpu := fs.String("cpu-executable", "", "CPU server/CLI override")
	cuda := fs.String("cuda-executable", "", "CUDA server/CLI override")
	wav := fs.String("wav", "", "PCM16 mono 16kHz WAV; transcript is never printed")
	durations := fs.String("durations", "2,5,10,30", "synthetic silence seconds; latency only")
	repeat := fs.Int("repeat", 2, "repetitions per input")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repeat < 1 || *repeat > 100 {
		return fmt.Errorf("repeat must be 1..100")
	}
	if *mode != "persistent" && *mode != "process" && *mode != "legacy" {
		return fmt.Errorf("unsupported mode (streaming is not implemented)")
	}
	values := map[string]string{"STT_DEVICE": *device, "STT_MODEL": *model, "STT_MODEL_PATH": *modelPath, "STT_CPU_EXECUTABLE": *cpu, "STT_CUDA_EXECUTABLE": *cuda, "STT_RUNTIME": *mode}
	if *mode == "legacy" {
		values["STT_RUNTIME"] = "process"
		if *device != "cpu" {
			return fmt.Errorf("legacy requires device=cpu and a verified CPU-only binary")
		}
	}
	cfg, err := whispercpp.RuntimeConfigFromEnv(func(k string) string { return values[k] })
	if err != nil {
		return err
	}
	var inputs [][]byte
	fixture := "synthetic-silence-latency-only"
	if *wav != "" {
		b, e := readWAV(*wav)
		if e != nil {
			return e
		}
		inputs = append(inputs, b)
		fixture = "user-wav-accuracy-not-scored"
	} else {
		for _, value := range strings.Split(*durations, ",") {
			seconds, e := strconv.Atoi(value)
			if e != nil || seconds < 1 || seconds > 600 {
				return fmt.Errorf("durations must be integer seconds 1..600")
			}
			inputs = append(inputs, make([]byte, seconds*32000))
		}
	}
	var provider stt.Provider
	var runtime *whispercpp.Runtime
	start := time.Now()
	if *mode == "legacy" {
		provider, err = whispercpp.New(whispercpp.Config{Executable: cfg.CPUExecutable, Model: cfg.ModelPath, Language: cfg.Language})
	} else {
		runtime, err = whispercpp.NewRuntime(context.Background(), cfg)
		if runtime != nil {
			defer runtime.Close()
		}
		provider = runtime
	}
	if err != nil {
		return fmt.Errorf("runtime initialization unavailable (no benchmark result)")
	}
	initMS := float64(time.Since(start).Microseconds()) / 1000
	encoder := json.NewEncoder(out)
	for _, pcm := range inputs {
		for i := 1; i <= *repeat; i++ {
			start = time.Now()
			_, err = provider.Transcribe(context.Background(), stt.Request{Audio: pcm, Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
			wall := float64(time.Since(start).Microseconds()) / 1000
			if err != nil {
				return fmt.Errorf("transcription failed (sample not recorded)")
			}
			info := stt.RuntimeInfo{Backend: "whisper.cpp", RequestedDevice: "cpu", SelectedDevice: "cpu", Model: cfg.ModelName, Available: true, Concurrency: 1}
			measurement := whispercpp.Measurement{RequestMS: wall}
			if runtime != nil {
				info = runtime.RuntimeInfo()
				measurement = runtime.Measurement()
			}
			seconds := float64(len(pcm)) / 32000
			if err = encoder.Encode(record{Mode: *mode, Runtime: info, Fixture: fixture, Repeat: i, AudioSeconds: seconds, WallMS: wall, RTF: wall / (1000 * seconds), EOTFinalMS: wall, InitializationMS: initMS, Measurement: measurement}); err != nil {
				return err
			}
		}
	}
	return nil
}
func readWAV(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open WAV")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, whispercpp.MaxRuntimeAudioBytes+65537))
	if err != nil || len(b) > whispercpp.MaxRuntimeAudioBytes+65536 || len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("invalid or oversized WAV")
	}
	valid := false
	var pcm []byte
	for offset := 12; offset+8 <= len(b); {
		size := int64(binary.LittleEndian.Uint32(b[offset+4:]))
		end := int64(offset) + 8 + size
		if end > int64(len(b)) {
			return nil, fmt.Errorf("truncated WAV")
		}
		chunk := b[offset+8 : int(end)]
		switch string(b[offset : offset+4]) {
		case "fmt ":
			valid = len(chunk) >= 16 && binary.LittleEndian.Uint16(chunk) == 1 && binary.LittleEndian.Uint16(chunk[2:]) == 1 && binary.LittleEndian.Uint32(chunk[4:]) == 16000 && binary.LittleEndian.Uint16(chunk[14:]) == 16
		case "data":
			pcm = chunk
		}
		offset = int(end + size%2)
	}
	if !valid || len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > whispercpp.MaxRuntimeAudioBytes {
		return nil, fmt.Errorf("WAV must be nonempty PCM16 mono 16kHz")
	}
	return pcm, nil
}
