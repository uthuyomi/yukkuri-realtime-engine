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
	Voice string  `json:"voice,omitempty"`
	Speed float64 `json:"speed,omitempty"`
}

type TextDeltaData struct {
	Text string `json:"text"`
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

type PlaybackProgressData struct {
	PlayedSeconds float64 `json:"played_seconds"`
}

type InputAudioCommitData struct {
	SampleRate int `json:"sample_rate"`
	Channels   int `json:"channels"`
	Bytes      int `json:"bytes"`
}

type InputAudioFormatData struct {
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Encoding   string `json:"encoding"`
}
