package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
)

type Engine struct {
	mu sync.RWMutex

	ttsProviders map[string]tts.Provider
	defaultTTS   string
}

func New() *Engine {
	return &Engine{
		ttsProviders: make(map[string]tts.Provider),
	}
}

func (e *Engine) RegisterTTS(provider tts.Provider) error {
	if provider == nil {
		return errors.New("tts provider is nil")
	}

	name := provider.Name()
	if name == "" {
		return errors.New("tts provider name is empty")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.ttsProviders[name]; exists {
		return fmt.Errorf("tts provider %q is already registered", name)
	}

	e.ttsProviders[name] = provider

	if e.defaultTTS == "" {
		e.defaultTTS = name
	}

	return nil
}

func (e *Engine) SetDefaultTTS(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.ttsProviders[name]; !exists {
		return fmt.Errorf("tts provider %q is not registered", name)
	}

	e.defaultTTS = name
	return nil
}

func (e *Engine) Synthesize(
	ctx context.Context,
	providerName string,
	req tts.Request,
) (*tts.Stream, error) {
	e.mu.RLock()

	if providerName == "" {
		providerName = e.defaultTTS
	}

	provider, exists := e.ttsProviders[providerName]

	e.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf(
			"tts provider %q is not registered",
			providerName,
		)
	}

	return provider.Synthesize(ctx, req)
}

// HasTTS exposes configured availability without leaking provider configuration.
func (e *Engine) HasTTS(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if name == "" {
		name = e.defaultTTS
	}
	_, ok := e.ttsProviders[name]
	return ok
}
