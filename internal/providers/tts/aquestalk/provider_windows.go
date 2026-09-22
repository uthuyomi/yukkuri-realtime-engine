//go:build windows

package aquestalk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
)

const (
	defaultSpeed = 100
	minSpeed     = 50
	maxSpeed     = 300
)

type Provider struct {
	dllPath string

	dll       *syscall.LazyDLL
	synthe    *syscall.LazyProc
	freeWave  *syscall.LazyProc
	setDevKey *syscall.LazyProc
	setUsrKey *syscall.LazyProc
}

type Config struct {
	DLLPath string

	DevKey string
	UsrKey string
}

func New(config Config) (*Provider, error) {
	if config.DLLPath == "" {
		return nil, fmt.Errorf("AquesTalk DLL path is empty")
	}

	if _, err := os.Stat(config.DLLPath); err != nil {
		return nil, fmt.Errorf(
			"AquesTalk DLL not found: %s: %w",
			config.DLLPath,
			err,
		)
	}

	dll := syscall.NewLazyDLL(config.DLLPath)

	p := &Provider{
		dllPath:   config.DLLPath,
		dll:       dll,
		synthe:    dll.NewProc("AquesTalk_Synthe_Utf8"),
		freeWave:  dll.NewProc("AquesTalk_FreeWave"),
		setDevKey: dll.NewProc("AquesTalk_SetDevKey"),
		setUsrKey: dll.NewProc("AquesTalk_SetUsrKey"),
	}

	// DLLと必須シンボルをここで検証する。
	if err := p.dll.Load(); err != nil {
		return nil, fmt.Errorf("failed to load AquesTalk DLL: %w", err)
	}

	if err := p.synthe.Find(); err != nil {
		return nil, fmt.Errorf("AquesTalk_Synthe_Utf8 not found: %w", err)
	}

	if err := p.freeWave.Find(); err != nil {
		return nil, fmt.Errorf("AquesTalk_FreeWave not found: %w", err)
	}

	if config.DevKey != "" {
		if err := p.setKey(p.setDevKey, config.DevKey); err != nil {
			return nil, fmt.Errorf("failed to set AquesTalk development key: %w", err)
		}
	}

	if config.UsrKey != "" {
		if err := p.setKey(p.setUsrKey, config.UsrKey); err != nil {
			return nil, fmt.Errorf("failed to set AquesTalk user key: %w", err)
		}
	}

	return p, nil
}

func (p *Provider) Name() string {
	return "aquestalk"
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

	speed := defaultSpeed

	if req.Speed > 0 {
		// 共通APIでは1.0を標準速度として扱う。
		speed = int(req.Speed * 100)
	}

	if speed < minSpeed || speed > maxSpeed {
		return nil, fmt.Errorf(
			"AquesTalk speed must be between %d and %d percent",
			minSpeed,
			maxSpeed,
		)
	}

	koe, err := syscall.BytePtrFromString(req.Text)
	if err != nil {
		return nil, fmt.Errorf("failed to encode speech text: %w", err)
	}

	var size int32

	wavPtr, _, _ := p.synthe.Call(
		uintptr(unsafe.Pointer(koe)),
		uintptr(speed),
		uintptr(unsafe.Pointer(&size)),
	)

	if wavPtr == 0 {
		return nil, fmt.Errorf(
			"AquesTalk synthesis failed with error code %d",
			size,
		)
	}

	// AquesTalkが確保したメモリをGo側へコピーする。
	// コピー後はDLL側の領域を即座に解放できる。
	wav := unsafe.Slice((*byte)(unsafe.Pointer(wavPtr)), int(size))
	data := append([]byte(nil), wav...)

	p.freeWave.Call(wavPtr)

	// FFI呼び出しが終わるまでポインタを生存させる。
	runtime.KeepAlive(koe)

	return &tts.Stream{
		Format: tts.AudioFormat{
			Codec:      "wav",
			SampleRate: 8000,
			Channels:   1,
		},
		Audio: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func (p *Provider) setKey(proc *syscall.LazyProc, key string) error {
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
