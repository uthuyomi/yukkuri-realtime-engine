package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"unicode/utf8"
)

// ClientData is the single field table used by validation and schema export.
// A trailing ! marks required fields. Unknown fields are forward-compatible,
// except input_text.commit, whose user-only trust boundary is deliberately strict.
var ClientData = map[string]map[string]string{
	"ping": {}, "session.close": {},
	"session.configure":   {"protocol_version": "string!", "audio_flow_control": "string"},
	"generation.create":   {"output": "string", "voice": "string", "speed": "number"},
	"generation.cancel":   {},
	"response.text.delta": {"text": "string!"}, "response.text.done": {},
	"input_text.commit":  {"text": "string!", "output": "string"},
	"input_audio.start":  {"sample_rate": "integer!", "channels": "integer!", "encoding": "string!", "mode": "string"},
	"input_audio.commit": {}, "input_audio.cancel": {}, "input_audio.stop": {},
	"input_audio.speech_start": {"interruption_id": "string"}, "input_audio.speech_end": {}, "input_audio.vad_misfire": {},
	"playback.configure":     {"flow_control": "string!"},
	"playback.credit":        {"capacity_source_frames": "integer!", "received_source_frames": "integer!", "played_source_frames": "integer!", "buffered_source_frames": "integer!"},
	"playback.progress":      {"played_seconds": "number", "played_source_frames": "integer"},
	"playback.paused":        {"interruption_id": "string!", "played_seconds": "number", "played_source_frames": "integer"},
	"playback.overflow":      {"interruption_id": "string"},
	"interruption.suspected": {"interruption_id": "string!"}, "interruption.failed": {"interruption_id": "string!"},
}
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func ValidID(id string) bool { return id == "" || idPattern.MatchString(id) }

func DecodeClient(payload []byte) (Event, error) {
	var e Event
	if len(payload) > MaxJSONBytes {
		return e, Error("payload_too_large")
	}
	if !utf8.Valid(payload) {
		return e, Error("invalid_request")
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	if err := d.Decode(&e); err != nil {
		return e, Error("invalid_request")
	}
	if d.Decode(new(any)) != io.EOF || e.Type == "" || len(e.Type) > MaxIDBytes || !ValidID(e.Type) || !ValidID(e.EventID) || !ValidID(e.Generation) || !ValidID(e.SessionID) {
		return e, Error("invalid_request")
	}
	fields, known := ClientData[e.Type]
	if !known {
		return e, Error("invalid_request")
	}
	if len(e.Data) == 0 || bytes.Equal(e.Data, []byte("null")) {
		e.Data = json.RawMessage(`{}`)
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(e.Data, &data) != nil || data == nil {
		return e, Error("invalid_request")
	}
	for name, kind := range fields {
		required := kind[len(kind)-1] == '!'
		if required {
			kind = kind[:len(kind)-1]
		}
		value, ok := data[name]
		if !ok {
			if required {
				return e, Error("invalid_request")
			}
			continue
		}
		if bytes.Equal(value, []byte("null")) {
			return e, Error("invalid_request")
		}
		switch kind {
		case "string":
			var v string
			if json.Unmarshal(value, &v) != nil || !utf8.ValidString(v) {
				return e, Error("invalid_request")
			}
			if name == "text" && len(v) > MaxTextBytes {
				return e, Error("payload_too_large")
			}
			if name != "text" && len(v) > MaxIDBytes {
				return e, Error("invalid_request")
			}
			if name == "interruption_id" && !ValidID(v) {
				return e, Error("invalid_request")
			}
		case "integer":
			var v int64
			if json.Unmarshal(value, &v) != nil || v < 0 {
				return e, Error("invalid_request")
			}
		case "number":
			var v float64
			if json.Unmarshal(value, &v) != nil || v < 0 {
				return e, Error("invalid_request")
			}
		}
	}
	if e.Type == "input_text.commit" {
		for name := range data {
			if _, ok := fields[name]; !ok {
				return e, LegacyError("invalid_text_input")
			}
		}
	}
	if e.Type == "playback.progress" || e.Type == "playback.paused" {
		if data["played_seconds"] == nil && data["played_source_frames"] == nil {
			return e, Error("invalid_request")
		}
	}
	if e.Type == "playback.credit" || e.Type == "playback.paused" || e.Type == "interruption.suspected" || e.Type == "interruption.failed" {
		if e.Generation == "" {
			return e, Error("invalid_request")
		}
	}
	return e, nil
}

// ClientSchema describes the structurally validated subset; state and arithmetic
// constraints are documented separately and tested at the runtime boundary.
func ClientSchema() map[string]any {
	variants := []any{}
	kinds := make([]string, 0, len(ClientData))
	for kind := range ClientData {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		fields := ClientData[kind]
		props := map[string]any{}
		required := []string{}
		for name, typ := range fields {
			if typ[len(typ)-1] == '!' {
				typ = typ[:len(typ)-1]
				required = append(required, name)
			}
			p := map[string]any{"type": typ}
			if typ == "integer" || typ == "number" {
				p["minimum"] = 0
			}
			props[name] = p
		}
		data := map[string]any{"type": "object", "properties": props, "additionalProperties": kind != "input_text.commit"}
		if len(required) > 0 {
			sort.Strings(required)
			data["required"] = required
		} else {
			data["type"] = []string{"object", "null"}
		}
		rootRequired := []string{"type"}
		if len(required) > 0 {
			rootRequired = append(rootRequired, "data")
		}
		variants = append(variants, map[string]any{"type": "object", "required": rootRequired, "properties": map[string]any{"type": map[string]any{"const": kind}, "data": data}})
	}
	return map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "title": "Yukkuri protocol v1 client event (structural subset)", "oneOf": variants}
}
