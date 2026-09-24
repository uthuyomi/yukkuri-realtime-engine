package realtime

import "github.com/uthuyomi/yukkuri-realtime-engine/internal/protocol"

// Compatibility alias: public wire ownership lives in protocol.
type Event = protocol.Event

type GenerationCreateData struct {
	Voice  string  `json:"voice,omitempty"`
	Speed  float64 `json:"speed,omitempty"`
	Output string  `json:"output,omitempty"`
}

type TextDeltaData struct {
	Text string `json:"text"`
}

var NewEvent = protocol.NewEvent

type PlaybackProgressData struct {
	PlayedSeconds      float64 `json:"played_seconds"`
	PlayedSourceFrames *int64  `json:"played_source_frames,omitempty"`
}

type InterruptionRequestData struct {
	InterruptionID     string  `json:"interruption_id"`
	PlayedSeconds      float64 `json:"played_seconds,omitempty"`
	PlayedSourceFrames *int64  `json:"played_source_frames,omitempty"`
}

type InputAudioCommitData struct {
	SampleRate int `json:"sample_rate"`
	Channels   int `json:"channels"`
	Bytes      int `json:"bytes"`
}

type InputAudioFormatData = protocol.InputAudioFormatData
