package whispercpp

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

const MaxRuntimeAudioBytes = 16000 * 2 * 600 // configured continuous turns can reach ten minutes
const UpstreamRevision = "d09f61a708f3487afa956ff578e60eae5e7a233c"

type RuntimeConfig struct {
	Device           string
	Mode             string
	CPUExecutable    string
	CUDAExecutable   string
	ModelPath        string
	ModelName        string
	Language         string
	Threads          int
	BestOf           int
	BeamSize         int
	QueueCapacity    int
	InitTimeout      time.Duration
	InferenceTimeout time.Duration
}

func DefaultRuntimeConfig() RuntimeConfig {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return RuntimeConfig{Device: "auto", Mode: "persistent", CPUExecutable: filepath.Join("runtime", "whisper", "cpu", "whisper-server"+suffix),
		CUDAExecutable: filepath.Join("runtime", "whisper", "cuda", "whisper-server"+suffix), ModelPath: filepath.Join("runtime", "whisper", "models", "ggml-small.bin"),
		ModelName: "small", Language: "ja", Threads: 4, BestOf: 5, BeamSize: 5, QueueCapacity: 8, InitTimeout: 2 * time.Minute, InferenceTimeout: 2 * time.Minute}
}

// Environment configuration belongs to the Engine/local benchmark, never SDKs.
func RuntimeConfigFromEnv(getenv func(string) string) (RuntimeConfig, error) {
	c := DefaultRuntimeConfig()
	if v := getenv("STT_DEVICE"); v != "" {
		c.Device = v
	}
	if v := getenv("STT_RUNTIME"); v != "" {
		c.Mode = v
	}
	if v := getenv("STT_MODEL"); v != "" {
		if !regexp.MustCompile(`^(tiny|base|small|medium|large-v[123]|large-v3-turbo)(\.en)?$`).MatchString(v) {
			return c, errors.New("invalid STT_MODEL identifier; use STT_MODEL_PATH for a custom model")
		}
		c.ModelName = v
		c.ModelPath = filepath.Join("runtime", "whisper", "models", "ggml-"+v+".bin")
	}
	if v := getenv("STT_MODEL_PATH"); v != "" {
		c.ModelPath = v
		c.ModelName = "custom"
	}
	if v := getenv("STT_LANGUAGE"); v != "" {
		c.Language = v
	}
	for key, dst := range map[string]*int{"STT_THREADS": &c.Threads, "STT_BEST_OF": &c.BestOf, "STT_BEAM_SIZE": &c.BeamSize, "STT_QUEUE_CAPACITY": &c.QueueCapacity} {
		if v := getenv(key); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil {
				return c, errors.New("invalid STT integer configuration")
			}
			*dst = n
		}
	}
	name := "whisper-server"
	if c.Mode == "process" {
		name = "whisper-cli"
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	c.CPUExecutable = filepath.Join("runtime", "whisper", "cpu", name)
	if _, err := os.Stat(c.CPUExecutable); err != nil {
		c.CPUExecutable = filepath.Join("runtime", "whisper", name)
	}
	c.CUDAExecutable = filepath.Join("runtime", "whisper", "cuda", name)
	if v := getenv("STT_CPU_EXECUTABLE"); v != "" {
		c.CPUExecutable = v
	}
	if v := getenv("STT_CUDA_EXECUTABLE"); v != "" {
		c.CUDAExecutable = v
	}
	return c, c.validate()
}

func (c RuntimeConfig) validate() error {
	if c.Device != "auto" && c.Device != "cpu" && c.Device != "cuda" {
		return errors.New("STT_DEVICE must be auto, cpu or cuda")
	}
	if c.Mode != "persistent" && c.Mode != "process" {
		return errors.New("STT_RUNTIME must be persistent or process")
	}
	if c.ModelPath == "" || !regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`).MatchString(c.ModelName) {
		return errors.New("invalid STT model configuration")
	}
	if c.Language != "auto" && !regexp.MustCompile(`^[a-z]{2,3}$`).MatchString(c.Language) {
		return errors.New("invalid STT language")
	}
	if c.Threads < 1 || c.Threads > 128 || c.BestOf < 1 || c.BestOf > 16 || c.BeamSize < 1 || c.BeamSize > 16 || c.QueueCapacity < 1 || c.QueueCapacity > 64 || c.InitTimeout <= 0 || c.InferenceTimeout <= 0 {
		return errors.New("invalid STT resource configuration")
	}
	return nil
}
