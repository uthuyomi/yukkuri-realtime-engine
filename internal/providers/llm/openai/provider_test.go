package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/llm"
)

func TestConversationRolesAndSystemOverrideArePreserved(t *testing.T) {
	captured := make(chan responsesRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		captured <- req
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "local-test", Model: "local-test", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{{Role: "system", Content: "server override"}, {Role: "user", Content: "first"}, {Role: "assistant", Content: "heard response"}, {Role: "user", Content: "second"}}
	stream, err := p.Generate(context.Background(), llm.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	req := <-captured
	if req.Instructions != "" || len(req.Input) != 4 {
		t.Fatal("provider overwrote conversation", req)
	}
	for i, m := range req.Input {
		if m.Role != messages[i].Role || m.Content != messages[i].Content {
			t.Fatal(req)
		}
	}
}

func TestStreamRequestCancellation(t *testing.T) {
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"buffered\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "local-test", Model: "local-test", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := p.Generate(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if d, err := stream.Recv(); err != nil || d.Text != "buffered" {
		t.Fatal(d, err)
	}
	done := make(chan error, 1)
	go func() { _, err := stream.Recv(); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel returned successful delta")
		}
	case <-time.After(time.Second):
		t.Fatal("Recv survived cancellation")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP request survived cancellation")
	}
}
