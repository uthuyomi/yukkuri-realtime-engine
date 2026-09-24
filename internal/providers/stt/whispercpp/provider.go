package whispercpp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

type Config struct {
	Executable string
	Model      string
	Language   string
}

type Provider struct {
	executable string
	model      string
	language   string
}

func New(config Config) (*Provider, error) {
	if config.Executable == "" {
		return nil, errors.New("whisper executable is required")
	}

	if config.Model == "" {
		return nil, errors.New("whisper model is required")
	}

	executable, err := filepath.Abs(config.Executable)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve whisper executable path: %w",
			err,
		)
	}

	model, err := filepath.Abs(config.Model)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve whisper model path: %w",
			err,
		)
	}

	if _, err := os.Stat(executable); err != nil {
		return nil, fmt.Errorf(
			"whisper executable not found: %s: %w",
			executable,
			err,
		)
	}

	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf(
			"whisper model not found: %s: %w",
			model,
			err,
		)
	}

	language := config.Language

	if language == "" {
		language = "ja"
	}

	return &Provider{
		executable: executable,
		model:      model,
		language:   language,
	}, nil
}

func (p *Provider) Name() string {
	return "whisper.cpp"
}

func (p *Provider) Transcribe(
	ctx context.Context,
	req stt.Request,
) (*stt.Result, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp(
		"",
		"yukkuri-whisper-*",
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create whisper temp directory: %w",
			err,
		)
	}
	defer os.RemoveAll(tempDir)

	inputPath := filepath.Join(
		tempDir,
		"input.wav",
	)

	if err := writePCM16WAV(
		inputPath,
		req.Audio,
		req.Format.SampleRate,
		req.Format.Channels,
	); err != nil {
		return nil, err
	}

	command := exec.CommandContext(
		ctx,
		p.executable,
		"-m",
		p.model,
		"-f",
		inputPath,
		"-l",
		p.language,
		"-nt",
		"-np",
	)

	// Keep stderr separate from the transcript.
	// whisper.cpp writes diagnostic/model information there.
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, fmt.Errorf(
			"whisper.cpp failed: %w: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}

	text := strings.TrimSpace(
		stdout.String(),
	)

	if text == "" {
		return &stt.Result{
			Text:     "",
			Language: p.language,
		}, nil
	}

	return &stt.Result{
		Text:     text,
		Language: p.language,
	}, nil
}

func validateRequest(req stt.Request) error {
	if len(req.Audio) == 0 {
		return errors.New("STT audio is empty")
	}

	if req.Format.Encoding != "pcm_s16le" {
		return fmt.Errorf(
			"unsupported STT encoding: %s",
			req.Format.Encoding,
		)
	}

	if req.Format.SampleRate <= 0 {
		return fmt.Errorf(
			"invalid STT sample rate: %d",
			req.Format.SampleRate,
		)
	}

	if req.Format.Channels != 1 {
		return fmt.Errorf(
			"unsupported STT channel count: %d",
			req.Format.Channels,
		)
	}

	if len(req.Audio)%2 != 0 {
		return fmt.Errorf(
			"PCM16 audio byte length must be even: %d",
			len(req.Audio),
		)
	}

	return nil
}

func writePCM16WAV(
	path string,
	pcm []byte,
	sampleRate int,
	channels int,
) error {
	const (
		bitsPerSample = 16
		wavHeaderSize = 44
	)

	if sampleRate <= 0 {
		return fmt.Errorf(
			"invalid WAV sample rate: %d",
			sampleRate,
		)
	}

	if channels <= 0 {
		return fmt.Errorf(
			"invalid WAV channel count: %d",
			channels,
		)
	}

	if len(pcm)%2 != 0 {
		return fmt.Errorf(
			"PCM16 data is not sample-aligned: %d bytes",
			len(pcm),
		)
	}

	dataSize := len(pcm)

	if uint64(dataSize) > uint64(^uint32(0))-36 {
		return errors.New("PCM data is too large for WAV")
	}

	byteRate :=
		sampleRate *
			channels *
			(bitsPerSample / 8)

	blockAlign :=
		channels *
			(bitsPerSample / 8)

	var wav bytes.Buffer

	wav.Grow(
		wavHeaderSize + dataSize,
	)

	wav.WriteString("RIFF")

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint32(36+dataSize),
	); err != nil {
		return err
	}

	wav.WriteString("WAVE")
	wav.WriteString("fmt ")

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint32(16),
	); err != nil {
		return err
	}

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint16(1),
	); err != nil {
		return err
	}

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint16(channels),
	); err != nil {
		return err
	}

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint32(sampleRate),
	); err != nil {
		return err
	}

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint32(byteRate),
	); err != nil {
		return err
	}

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint16(blockAlign),
	); err != nil {
		return err
	}

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint16(bitsPerSample),
	); err != nil {
		return err
	}

	wav.WriteString("data")

	if err := binary.Write(
		&wav,
		binary.LittleEndian,
		uint32(dataSize),
	); err != nil {
		return err
	}

	if _, err := wav.Write(pcm); err != nil {
		return err
	}

	if err := os.WriteFile(
		path,
		wav.Bytes(),
		0600,
	); err != nil {
		return fmt.Errorf(
			"write temporary WAV: %w",
			err,
		)
	}

	return nil
}
