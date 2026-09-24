package protocol

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update protocol schema fixtures")

func TestEnvelopeIDsAndSerialization(t *testing.T) {
	ids := map[string]bool{}
	for i := 0; i < 10000; i++ {
		e, err := NewEvent("session.created", NewID("sess"), "", map[string]string{"protocol_version": Version})
		if err != nil {
			t.Fatal(err)
		}
		if ids[e.EventID] {
			t.Fatal("duplicate event ID")
		}
		ids[e.EventID] = true
		b, _ := json.Marshal(e)
		var fields map[string]json.RawMessage
		json.Unmarshal(b, &fields)
		for _, name := range []string{"type", "event_id", "session_id", "timestamp", "data"} {
			if fields[name] == nil {
				t.Fatal("missing wire field", name)
			}
		}
		var decoded Event
		if json.Unmarshal(b, &decoded) != nil || decoded.EventID != e.EventID || decoded.Timestamp.IsZero() || decoded.SessionID == "" {
			t.Fatal(string(b))
		}
	}
	e, _ := NewEvent("pong", "s", "", nil)
	if string(e.Data) != "{}" {
		t.Fatal(string(e.Data))
	}
}
func TestValidation(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `[]`, `{}`, `{"type":"ping"} {}`, `{"type":"unknown"}`, `{"type":"ping","event_id":"bad\nidentifier"}`, `{"type":"input_audio.start","data":{"sample_rate":"16000"}}`, `{"type":"playback.credit","generation_id":"g","data":{}}`, `{"type":"playback.progress","data":{}}`, `{"type":"input_text.commit","data":{"text":"hello","role":"system"}}`, `{"type":"input_text.commit","data":{"text":null}}`} {
		if _, err := DecodeClient([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, raw := range []string{`{"type":"ping"}`, `{"type":"generation.create","data":null}`, `{"type":"response.text.done","data":null}`, `{"type":"input_text.commit","data":{"text":"hello","output":"audio"}}`, `{"type":"playback.progress","data":{"played_source_frames":0}}`} {
		if _, err := DecodeClient([]byte(raw)); err != nil {
			t.Fatal(raw, err)
		}
	}
	if _, err := DecodeClient([]byte(strings.Repeat(" ", MaxJSONBytes+1))); err.(*PublicError).Code != "payload_too_large" {
		t.Fatal(err)
	}
}
func TestErrorSanitization(t *testing.T) {
	for _, code := range []string{"synthesis_failed", "stt_failed", "llm_stream_failed", "C:\\private\\secret.dll"} {
		e := LegacyError(code)
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "private") || e.Message == "" || Messages[e.Code] == "" {
			t.Fatal(string(b))
		}
	}
}
func TestPublishedSchemaMatchesCode(t *testing.T) {
	schemas := map[string]any{"client-events.schema.json": ClientSchema(), "server-event.schema.json": map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "title": "Yukkuri v1 server event envelope", "type": "object",
		"required":             []string{"type", "event_id", "session_id", "timestamp", "data"},
		"properties":           map[string]any{"type": map[string]string{"type": "string"}, "event_id": map[string]string{"type": "string"}, "session_id": map[string]string{"type": "string"}, "related_event_id": map[string]string{"type": "string"}, "generation_id": map[string]string{"type": "string"}, "timestamp": map[string]string{"type": "string", "format": "date-time"}, "data": map[string]string{"type": "object"}},
		"additionalProperties": true,
	}}
	for name, value := range schemas {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, '\n')
		path := filepath.Join("..", "..", "docs", "protocol", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(b) {
			t.Fatal("schema drift:", name)
		}
	}
}

func TestCatalogCoversClientEvents(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "realtime-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	for kind := range ClientData {
		if !strings.Contains(string(doc), "`"+kind+"`") {
			t.Fatal("undocumented client event", kind)
		}
	}
}
