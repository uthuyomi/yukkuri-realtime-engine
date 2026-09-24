package httptransport

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

type workerGroup struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	active  int
	closing bool
	once    sync.Once
	done    chan struct{}
}
type realtimeWriter struct {
	conn    *websocket.Conn
	lock    chan struct{}
	related string
	session string
	workers *workerGroup
}

func newRealtimeWriter(conn *websocket.Conn) *realtimeWriter {
	return &realtimeWriter{conn: conn, lock: make(chan struct{}, 1), workers: &workerGroup{done: make(chan struct{})}}
}
func (w *realtimeWriter) withRelated(id string) *realtimeWriter {
	return &realtimeWriter{conn: w.conn, lock: w.lock, related: id, session: w.session, workers: w.workers}
}
func (w *realtimeWriter) payload(event realtime.Event) ([]byte, error) {
	if event.RelatedEventID == "" {
		event.RelatedEventID = w.related
	}
	return json.Marshal(event)
}
func (w *realtimeWriter) write(ctx context.Context, kind websocket.MessageType, payload, pcm []byte) error {
	ctx, cancel := context.WithTimeout(ctx, protocol.WriteTimeout)
	defer cancel()
	select {
	case w.lock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.lock }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.conn.Write(ctx, kind, payload); err != nil {
		return err
	}
	if pcm != nil {
		return w.conn.Write(ctx, websocket.MessageBinary, pcm)
	}
	return nil
}
func (w *realtimeWriter) Event(ctx context.Context, event realtime.Event) error {
	payload, err := w.payload(event)
	if err != nil {
		return err
	}
	return w.write(ctx, websocket.MessageText, payload, nil)
}
func (w *realtimeWriter) Binary(ctx context.Context, data []byte) error {
	return w.write(ctx, websocket.MessageBinary, data, nil)
}

// Credit waits occur before this atomic pair; the lock is shared by scoped writers.
func (w *realtimeWriter) AudioDelta(ctx context.Context, event realtime.Event, pcm []byte) error {
	payload, err := w.payload(event)
	if err != nil {
		return err
	}
	return w.write(ctx, websocket.MessageText, payload, pcm)
}
func (w *realtimeWriter) Go(ctx context.Context, fn func()) {
	g := w.workers
	g.mu.Lock()
	if g.closing || ctx.Err() != nil {
		g.mu.Unlock()
		return
	}
	if g.active >= protocol.MaxWorkers {
		g.mu.Unlock()
		e := protocol.Error("resource_limit")
		e.Recoverable = false
		_ = emitError(ctx, w, w.session, "", e)
		w.conn.CloseNow()
		return
	}
	g.active++
	g.wg.Add(1)
	g.mu.Unlock()
	go func() { defer func() { g.mu.Lock(); g.active--; g.mu.Unlock(); g.wg.Done() }(); fn() }()
}
func (w *realtimeWriter) wait(ctx context.Context) bool {
	g := w.workers
	g.once.Do(func() { g.mu.Lock(); g.closing = true; g.mu.Unlock(); go func() { g.wg.Wait(); close(g.done) }() })
	select {
	case <-g.done:
		return true
	case <-ctx.Done():
		return false
	}
}
