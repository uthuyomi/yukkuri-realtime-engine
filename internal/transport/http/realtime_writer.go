package httptransport

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

type realtimeWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func newRealtimeWriter(
	conn *websocket.Conn,
) *realtimeWriter {
	return &realtimeWriter{
		conn: conn,
	}
}

func (w *realtimeWriter) Event(
	ctx context.Context,
	event realtime.Event,
) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	return w.conn.Write(
		ctx,
		websocket.MessageText,
		payload,
	)
}

func (w *realtimeWriter) Binary(
	ctx context.Context,
	data []byte,
) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.conn.Write(
		ctx,
		websocket.MessageBinary,
		data,
	)
}

// Keep metadata and its PCM indivisible across generations and control events.
func (w *realtimeWriter) AudioDelta(ctx context.Context, event realtime.Event, pcm []byte) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err = w.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return err
	}
	return w.conn.Write(ctx, websocket.MessageBinary, pcm)
}
