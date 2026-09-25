package httptransport

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
)

// More sequential sessions than MaxSessions catches leaked admission slots.
// A reconnect is a fresh session, never a replay of the disconnected session.
func TestRepeatedConnectDisconnectReleasesAdmission(t *testing.T) {
	s := New(Config{}, engine.New())
	server := httptest.NewServer(s.server.Handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seen := map[string]bool{}
	for i := 0; i < 80; i++ {
		path := "/v1/realtime"
		if i%2 == 1 {
			path = "/v1/transcription"
		}
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
		if err != nil {
			t.Fatal(i, err)
		}
		created := publicEvent(t, c, ctx, "session.created")
		if seen[created.SessionID] {
			t.Fatal("reused session ID on reconnect")
		}
		seen[created.SessionID] = true
		if i%3 == 0 {
			sendID(t, c, ctx, "session.close", "close", nil)
			publicEvent(t, c, ctx, "session.closed")
		}
		c.CloseNow()
		deadline := time.Now().Add(time.Second)
		for len(s.connections) != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(s.connections) != 0 {
			t.Fatal("connection admission not released", i)
		}
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
