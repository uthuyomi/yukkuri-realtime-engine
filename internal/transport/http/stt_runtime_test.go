package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

type runtimeSTT struct {
	info stt.RuntimeInfo
	err  error
}

func (p *runtimeSTT) Name() string                 { return "whisper.cpp" }
func (p *runtimeSTT) RuntimeInfo() stt.RuntimeInfo { return p.info }
func (p *runtimeSTT) Transcribe(context.Context, stt.Request) (*stt.Result, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &stt.Result{Text: "", Language: "ja"}, nil
}
func TestRuntimeDiscoveryAndActualAdmissionLimit(t *testing.T) {
	for _, available := range []bool{true, false} {
		s := New(Config{}, engine.New())
		s.SetSTTProvider(&runtimeSTT{info: stt.RuntimeInfo{Backend: "whisper.cpp", RequestedDevice: "auto", SelectedDevice: "cpu", Model: "small", Persistent: true, Available: available, State: "ready", FallbackFrom: "cuda", FallbackReason: "cuda_initialization_failed", Concurrency: 1, QueueCapacity: 8}})
		caps := s.capabilities()
		feature := caps.Features["transcription"]
		if feature.Available != available || feature.Runtime == nil || feature.Runtime.SelectedDevice != "cpu" || caps.Limits["concurrent_stt"] != 1 || caps.Limits["stt_admitted_requests"] != 8 || s.speculativeSTT.RuntimeInfo().Concurrency != 1 {
			t.Fatal(caps)
		}
		b, _ := json.Marshal(caps)
		for _, secret := range []string{"C:\\", "model_path", "executable", "GTX", "API_KEY"} {
			if strings.Contains(string(b), secret) {
				t.Fatal("metadata leak", secret)
			}
		}
		if caps.ProtocolVersion != "1" || len(feature.Modes) != 2 || feature.Modes[1] != "final-only" {
			t.Fatal("compatibility", caps)
		}
	}
}
func TestRuntimeErrorsHaveStablePublicCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{stt.ErrUnavailable, "provider_unavailable"}, {stt.ErrCapacity, "resource_limit"}, {context.DeadlineExceeded, "timeout"}, {errors.New("C:/private/models/secret api-key raw CUDA error"), "transcription_failed"}} {
		s := New(Config{}, engine.New())
		s.SetSTTProvider(&runtimeSTT{info: stt.RuntimeInfo{Available: true, Concurrency: 1}, err: tc.err})
		r, e := s.transcribe(context.Background(), []byte{0, 0}, inputConfig())
		var wire *protocol.PublicError
		if r != nil || !errors.As(e, &wire) || wire.Code != tc.code || strings.Contains(wire.Message, "private") {
			t.Fatal(r, e)
		}
	}
}
