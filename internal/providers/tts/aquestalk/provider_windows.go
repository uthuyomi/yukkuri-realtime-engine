//go:build windows

package aquestalk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
)

const (
	defaultSpeed = 100
	minSpeed     = 50
	maxSpeed     = 300
)

type Config struct {
	Voices       map[string]string
	DefaultVoice string

	DevKey string
	UsrKey string

	Kanji2KoeDLL    string
	Kanji2KoeDic    string
	Kanji2KoeDevKey string
}

type Provider struct {
	voices       map[string]*voice
	defaultVoice string
	kanji2Koe    *kanji2Koe
}

type voice struct {
	name    string
	dllPath string

	dll       *syscall.LazyDLL
	synthe    *syscall.LazyProc
	freeWave  *syscall.LazyProc
	setDevKey *syscall.LazyProc
	setUsrKey *syscall.LazyProc
}

func New(config Config) (*Provider, error) {
	if len(config.Voices) == 0 {
		return nil, fmt.Errorf("no AquesTalk voices configured")
	}

	p := &Provider{
		voices: make(map[string]*voice, len(config.Voices)),
	}

	for name, dllPath := range config.Voices {
		name = normalizeVoiceName(name)

		if name == "" {
			return nil, fmt.Errorf("AquesTalk voice name is empty")
		}

		if dllPath == "" {
			return nil, fmt.Errorf(
				"AquesTalk DLL path for voice %q is empty",
				name,
			)
		}

		if _, exists := p.voices[name]; exists {
			return nil, fmt.Errorf(
				"AquesTalk voice %q is configured more than once",
				name,
			)
		}

		v, err := loadVoice(
			name,
			dllPath,
			config.DevKey,
			config.UsrKey,
		)
		if err != nil {
			return nil, err
		}

		p.voices[name] = v
	}

	defaultVoice := normalizeVoiceName(config.DefaultVoice)

	if defaultVoice == "" {
		names := p.VoiceNames()
		defaultVoice = names[0]
	}

	if _, exists := p.voices[defaultVoice]; !exists {
		return nil, fmt.Errorf(
			"default AquesTalk voice %q is not configured",
			defaultVoice,
		)
	}

	p.defaultVoice = defaultVoice

	if config.Kanji2KoeDLL != "" || config.Kanji2KoeDic != "" {
		if config.Kanji2KoeDLL == "" {
			return nil, fmt.Errorf(
				"AqKanji2Koe DLL path is required",
			)
		}

		if config.Kanji2KoeDic == "" {
			return nil, fmt.Errorf(
				"AqKanji2Koe dictionary path is required",
			)
		}

		k, err := newKanji2Koe(
			config.Kanji2KoeDLL,
			config.Kanji2KoeDic,
			config.Kanji2KoeDevKey,
		)
		if err != nil {
			return nil, err
		}

		p.kanji2Koe = k
	}

	return p, nil
}

func loadVoice(
	name string,
	dllPath string,
	devKey string,
	usrKey string,
) (*voice, error) {
	if _, err := os.Stat(dllPath); err != nil {
		return nil, fmt.Errorf(
			"AquesTalk DLL for voice %q not found: %s: %w",
			name,
			dllPath,
			err,
		)
	}

	dll := syscall.NewLazyDLL(dllPath)

	v := &voice{
		name:      name,
		dllPath:   dllPath,
		dll:       dll,
		synthe:    dll.NewProc("AquesTalk_Synthe_Utf8"),
		freeWave:  dll.NewProc("AquesTalk_FreeWave"),
		setDevKey: dll.NewProc("AquesTalk_SetDevKey"),
		setUsrKey: dll.NewProc("AquesTalk_SetUsrKey"),
	}

	if err := v.dll.Load(); err != nil {
		return nil, fmt.Errorf(
			"failed to load AquesTalk voice %q: %w",
			name,
			err,
		)
	}

	if err := v.synthe.Find(); err != nil {
		return nil, fmt.Errorf(
			"AquesTalk_Synthe_Utf8 not found for voice %q: %w",
			name,
			err,
		)
	}

	if err := v.freeWave.Find(); err != nil {
		return nil, fmt.Errorf(
			"AquesTalk_FreeWave not found for voice %q: %w",
			name,
			err,
		)
	}

	if devKey != "" {
		if err := setKey(v.setDevKey, devKey); err != nil {
			return nil, fmt.Errorf(
				"failed to set development key for voice %q: %w",
				name,
				err,
			)
		}
	}

	if usrKey != "" {
		if err := setKey(v.setUsrKey, usrKey); err != nil {
			return nil, fmt.Errorf(
				"failed to set user key for voice %q: %w",
				name,
				err,
			)
		}
	}

	return v, nil
}

func (p *Provider) Name() string {
	return "aquestalk"
}

func (p *Provider) VoiceNames() []string {
	names := make([]string, 0, len(p.voices))

	for name := range p.voices {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func (p *Provider) Synthesize(
	ctx context.Context,
	req tts.Request,
) (*tts.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if req.Text == "" {
		return nil, fmt.Errorf("speech text is empty")
	}

	voiceName := normalizeVoiceName(req.Voice)

	if voiceName == "" || voiceName == "default" {
		voiceName = p.defaultVoice
	}

	v, exists := p.voices[voiceName]
	if !exists {
		return nil, fmt.Errorf(
			"AquesTalk voice %q is not configured; available voices: %s",
			voiceName,
			strings.Join(p.VoiceNames(), ", "),
		)
	}

	speed := defaultSpeed

	if req.Speed > 0 {
		speed = int(req.Speed * 100)
	}

	if speed < minSpeed || speed > maxSpeed {
		return nil, fmt.Errorf(
			"AquesTalk speed must be between %d and %d percent",
			minSpeed,
			maxSpeed,
		)
	}

	speechText := req.Text

	if p.kanji2Koe != nil {
		converted, err := p.kanji2Koe.Convert(req.Text)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to convert Japanese text to AquesTalk symbols: %w",
				err,
			)
		}

		speechText = converted
	}

	koe, err := syscall.BytePtrFromString(speechText)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to encode AquesTalk input: %w",
			err,
		)
	}

	var size int32

	wavPtr, _, _ := v.synthe.Call(
		uintptr(unsafe.Pointer(koe)),
		uintptr(speed),
		uintptr(unsafe.Pointer(&size)),
	)

	runtime.KeepAlive(koe)

	if wavPtr == 0 {
		return nil, fmt.Errorf(
			"AquesTalk voice %q synthesis failed with error code %d",
			voiceName,
			size,
		)
	}

	if size <= 0 {
		v.freeWave.Call(wavPtr)

		return nil, fmt.Errorf(
			"AquesTalk voice %q returned invalid WAV size %d",
			voiceName,
			size,
		)
	}

	wav := unsafe.Slice(
		(*byte)(unsafe.Pointer(wavPtr)),
		int(size),
	)

	data := append([]byte(nil), wav...)

	v.freeWave.Call(wavPtr)

	return &tts.Stream{
		Format: tts.AudioFormat{
			Codec:      "wav",
			SampleRate: 8000,
			Channels:   1,
		},
		Audio: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func (p *Provider) Close() {
	if p == nil {
		return
	}

	if p.kanji2Koe != nil {
		p.kanji2Koe.Close()
	}
}

func setKey(proc *syscall.LazyProc, key string) error {
	if err := proc.Find(); err != nil {
		return err
	}

	keyPtr, err := syscall.BytePtrFromString(key)
	if err != nil {
		return err
	}

	result, _, _ := proc.Call(
		uintptr(unsafe.Pointer(keyPtr)),
	)

	runtime.KeepAlive(keyPtr)

	if result != 0 {
		return fmt.Errorf("AquesTalk rejected license key")
	}

	return nil
}

func normalizeVoiceName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
