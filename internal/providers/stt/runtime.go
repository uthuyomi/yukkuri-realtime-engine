package stt

import "errors"

var (
	ErrUnavailable = errors.New("STT runtime unavailable")
	ErrRuntime     = errors.New("STT runtime failed")
	ErrCapacity    = errors.New("STT runtime queue full")
)

// RuntimeInfo is deliberately safe for discovery/logging. No paths, commands,
// GPU identifiers, provider response, audio or transcript belong here.
type RuntimeInfo struct {
	Backend         string `json:"backend"`
	RequestedDevice string `json:"requested_device"`
	SelectedDevice  string `json:"selected_device,omitempty"`
	Model           string `json:"model"`
	Persistent      bool   `json:"persistent"`
	Available       bool   `json:"available"`
	State           string `json:"state"`
	FallbackFrom    string `json:"fallback_from,omitempty"`
	FallbackReason  string `json:"fallback_reason,omitempty"`
	Concurrency     int    `json:"concurrency"`
	QueueCapacity   int    `json:"queue_capacity"`
}

type RuntimeProvider interface{ RuntimeInfo() RuntimeInfo }

func Describe(p Provider) RuntimeInfo {
	if p == nil {
		return RuntimeInfo{Available: false}
	}
	if r, ok := p.(RuntimeProvider); ok {
		return r.RuntimeInfo()
	}
	return RuntimeInfo{Available: true, Concurrency: 2}
}
