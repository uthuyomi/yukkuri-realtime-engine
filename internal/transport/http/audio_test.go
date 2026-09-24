package httptransport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/audio"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/engine"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/tts"
	"github.com/uthuyomi/yukkuri-realtime-engine/internal/realtime"
)

type longAudioTTS struct{}

func (longAudioTTS) Name() string { return "test" }
func (longAudioTTS) Synthesize(ctx context.Context, r tts.Request) (*tts.Stream, error) {
	stream, _ := (testTTS{}).Synthesize(ctx, r)
	header, _ := io.ReadAll(stream.Audio)
	stream.Audio.Close()
	b := make([]byte, 44+24000*2)
	copy(b, header)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	binary.LittleEndian.PutUint32(b[40:], 48000)
	for i := 0; i < 24000; i++ {
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(i))
	}
	stream.Audio = io.NopCloser(bytes.NewReader(b))
	return stream, nil
}

// Assert actual websocket metadata/binary adjacency, byte identity and absolute
// source offsets while reading up to a credit boundary.
func readAudioFrames(t *testing.T, c *websocket.Conn, ctx context.Context, id string, start, until int64) {
	t.Helper()
	for start < until {
		e := readType(t, c, ctx, "response.audio.delta")
		var m struct {
			Bytes  int   `json:"bytes"`
			Frames int64 `json:"source_frames"`
			Start  int64 `json:"source_start_frame"`
		}
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatal(err)
		}
		kind, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Generation != id || kind != websocket.MessageBinary || len(b) != m.Bytes || m.Frames != int64(len(b)/2) || m.Start != start || start+m.Frames > until {
			t.Fatalf("bad association: %s %+v %d %v", e.Generation, m, len(b), kind)
		}
		for i := int64(0); i < m.Frames; i++ {
			if binary.LittleEndian.Uint16(b[i*2:]) != uint16(start+i) {
				t.Fatal("PCM mismatch")
			}
		}
		start += m.Frames
	}
}

func TestAudioFlowWebSocket(t *testing.T) {
	for _, mode := range []string{"legacy_cancel", "legacy_resume", "explicit_resume", "replace"} {
		t.Run(mode, func(t *testing.T) {
			eng := engine.New()
			eng.RegisterTTS(longAudioTTS{})
			s := New(Config{}, eng)
			c, ctx := connectTest(t, s)
			if mode == "explicit_resume" {
				sendEvent(t, c, ctx, "playback.configure", map[string]string{"flow_control": "credit-v1"})
			}
			sendEvent(t, c, ctx, "generation.create", nil)
			id := readType(t, c, ctx, "generation.created").Generation
			sendScoped(t, c, ctx, "response.text.delta", id, realtime.TextDeltaData{Text: "audio."})
			sendScoped(t, c, ctx, "response.text.done", id, nil)
			readType(t, c, ctx, "response.audio.chunk.started")
			if mode == "explicit_resume" {
				sendEvent(t, c, ctx, "ping", nil)
				readType(t, c, ctx, "pong")
				sendScoped(t, c, ctx, "playback.credit", id, audio.Credit{Capacity: 16000})
			}
			readAudioFrames(t, c, ctx, id, 0, 16000)
			switch mode {
			case "legacy_cancel":
				sendScoped(t, c, ctx, "generation.cancel", id, nil)
				if e := readType(t, c, ctx, "generation.cancelled"); e.Generation != id {
					t.Fatal(e)
				}
			case "replace":
				sendEvent(t, c, ctx, "generation.create", nil)
				if e := readType(t, c, ctx, "generation.created"); e.Generation == id {
					t.Fatal(e)
				}
			default:
				if mode == "explicit_resume" {
					// Receipt while paused offers no new credit. Control still passes the writer.
					sendScoped(t, c, ctx, "playback.credit", id, audio.Credit{Capacity: 16000, Received: 16000, Buffered: 16000})
					sendEvent(t, c, ctx, "ping", nil)
					readType(t, c, ctx, "pong")
					sendScoped(t, c, ctx, "playback.credit", id, audio.Credit{Capacity: 16000, Received: 16000, Played: 8000, Buffered: 8000})
				} else {
					n := int64(8000)
					sendScoped(t, c, ctx, "playback.progress", id, realtime.PlaybackProgressData{PlayedSourceFrames: &n})
				}
				readAudioFrames(t, c, ctx, id, 16000, 24000)
				readType(t, c, ctx, "generation.done")
			}
		})
	}
}

func TestTextProducerBackpressureCannotBlockWebSocketControl(t *testing.T) {
	eng := engine.New()
	eng.RegisterTTS(longAudioTTS{})
	c, ctx := connectTest(t, New(Config{}, eng))
	sendEvent(t, c, ctx, "playback.configure", map[string]string{"flow_control": "credit-v1"})
	sendEvent(t, c, ctx, "generation.create", nil)
	id := readType(t, c, ctx, "generation.created").Generation
	sendScoped(t, c, ctx, "response.text.delta", id, realtime.TextDeltaData{Text: strings.Repeat("bounded sentence.", 100)})
	readType(t, c, ctx, "generation.cancelled")
	// Other metadata may precede the expected bounded-producer error.
	for {
		kind, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var e realtime.Event
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "error" {
			break
		}
	}
	sendEvent(t, c, ctx, "generation.create", nil)
	readType(t, c, ctx, "generation.created")
}

func TestWriterAudioPairsRemainAtomicWithConcurrentControls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.CloseNow()
		writer := newRealtimeWriter(conn)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for producer := 0; producer < 2; producer++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				for i := 0; i < 100; i++ {
					e, _ := realtime.NewEvent("control", "s", "g", i)
					var err error
					if p == 0 {
						e.Type = "response.audio.delta"
						err = writer.AudioDelta(ctx, e, []byte{byte(i)})
					} else {
						err = writer.Event(ctx, e)
					}
					if err != nil {
						errs <- err
						return
					}
				}
			}(producer)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			result <- err
			return
		}
		result <- nil
		<-ctx.Done()
	}))
	defer srv.Close()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for i := 0; i < 200; i++ {
		kind, b, err := c.Read(ctx)
		if err != nil || kind != websocket.MessageText {
			t.Fatal(kind, err)
		}
		var e realtime.Event
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "response.audio.delta" {
			var n int
			json.Unmarshal(e.Data, &n)
			kind, b, err = c.Read(ctx)
			if err != nil || kind != websocket.MessageBinary || len(b) != 1 || b[0] != byte(n) {
				t.Fatal("interleaved pair", kind, err)
			}
		}
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestSpeechEndpointUnaffectedByAudioCredit(t *testing.T) {
	eng := engine.New()
	eng.RegisterTTS(testTTS{})
	s := New(Config{}, eng)
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"text":"hello"}`)))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatal(w.Code, w.Body.String())
	}
	pcm, err := audio.DecodeWAV(w.Body.Bytes())
	if err != nil || len(pcm.Data) != 2 || pcm.SampleRate != 8000 {
		t.Fatal(pcm, err)
	}
}
