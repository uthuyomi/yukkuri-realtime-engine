package smartturn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/turndetection"
)

func TestLocalModelSmoke(t *testing.T) {
	endpoint := os.Getenv("TURN_DETECTOR_TEST_URL")
	if endpoint == "" {
		t.Skip("set TURN_DETECTOR_TEST_URL to exercise real local weights")
	}
	p, err := New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	r, err := p.Detect(ctx, turndetection.Request{Audio: make([]byte, 32000)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real model: complete=%v probability=%f latency=%s", r.Complete, r.Probability, time.Since(start))
}

func TestDetectContract(t *testing.T) {
	for _, body := range []string{`{"complete":true,"probability":0.8}`, `{}`, `{"complete":true,"probability":2}`, `broken`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if len(b) != 2 || r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Error("invalid PCM request")
				}
				_, _ = io.WriteString(w, body)
			}))
			defer s.Close()
			p, err := New(s.URL)
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Detect(context.Background(), turndetection.Request{Audio: []byte{1, 2}})
			if body == `{"complete":true,"probability":0.8}` {
				if err != nil || !result.Complete {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
func TestCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer s.Close()
	p, _ := New(s.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Detect(ctx, turndetection.Request{Audio: []byte{0, 0}}); err == nil {
		t.Fatal("expected cancellation")
	}
}
func TestLoopbackOnly(t *testing.T) {
	for _, url := range []string{"https://example.com", "http://example.com", "http://user@127.0.0.1"} {
		if _, err := New(url); err == nil {
			t.Fatal("accepted", url)
		}
	}
}
