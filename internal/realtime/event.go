package realtime

import (
	"encoding/json"
	"time"
)

type Event struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id,omitempty"`
	EventID    string          `json:"event_id,omitempty"`
	Generation string          `json:"generation_id,omitempty"`
	Timestamp  time.Time       `json:"timestamp"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type GenerationCreateData struct {
	Text  string  `json:"text"`
	Voice string  `json:"voice,omitempty"`
	Speed float64 `json:"speed,omitempty"`
}

func NewEvent(
	eventType string,
	sessionID string,
	generationID string,
	data any,
) (Event, error) {
	var raw json.RawMessage

	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return Event{}, err
		}

		raw = encoded
	}

	return Event{
		Type:       eventType,
		SessionID:  sessionID,
		EventID:    newID("evt"),
		Generation: generationID,
		Timestamp:  time.Now().UTC(),
		Data:       raw,
	}, nil
}
