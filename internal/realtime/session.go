package realtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

type Session struct {
	id string

	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex

	generationID     string
	generationCtx    context.Context
	generationCancel context.CancelFunc
}

func NewSession(parent context.Context) *Session {
	ctx, cancel := context.WithCancel(parent)

	return &Session{
		id:     newID("sess"),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) StartGeneration() (
	string,
	context.Context,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.generationCancel != nil {
		s.generationCancel()
	}

	generationID := newID("gen")

	ctx, cancel := context.WithCancel(s.ctx)

	s.generationID = generationID
	s.generationCtx = ctx
	s.generationCancel = cancel

	return generationID, ctx
}

func (s *Session) CancelGeneration() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.generationID

	if s.generationCancel != nil {
		s.generationCancel()
	}

	s.generationID = ""
	s.generationCtx = nil
	s.generationCancel = nil

	return id
}

func (s *Session) CurrentGeneration() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.generationID
}

func (s *Session) Close() {
	s.CancelGeneration()
	s.cancel()
}

func newID(prefix string) string {
	var buffer [12]byte

	if _, err := rand.Read(buffer[:]); err != nil {
		panic(fmt.Sprintf(
			"failed to generate realtime ID: %v",
			err,
		))
	}

	return prefix + "_" + hex.EncodeToString(buffer[:])
}
