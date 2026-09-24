package audio

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Credit is cumulative and measured exclusively in source PCM frames. Received
// includes frames held by the resampler; capacity also covers data in flight.
type Credit struct {
	Capacity int64 `json:"capacity_source_frames"`
	Received int64 `json:"received_source_frames"`
	Played   int64 `json:"played_source_frames"`
	Buffered int64 `json:"buffered_source_frames"`
}

type FlowSnapshot struct {
	Capacity, Reserved, Received, Played, Available int64
	Explicit                                        bool
	WaitDuration                                    time.Duration
	WaitCount                                       int64
}

// FlowController owns no PCM and never holds its mutex while waiting. One
// instance belongs to one generation; its caller supplies that generation's ctx.
type FlowController struct {
	mu      sync.Mutex
	rate    int
	state   FlowSnapshot
	changed chan struct{}
	timeout time.Duration
}

func NewFlowController(rate int) (*FlowController, error) {
	if rate < 1000 || rate > 192000 {
		return nil, errors.New("unsupported source sample rate")
	}
	return &FlowController{rate: rate, state: FlowSnapshot{Capacity: int64(rate) * 2}, changed: make(chan struct{}), timeout: 30 * time.Second}, nil
}

// RequireCredit is called before exposing a newly created controller.
func (f *FlowController) RequireCredit() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Explicit = true
	f.state.Capacity = 0
}

func (f *FlowController) signalLocked() { close(f.changed); f.changed = make(chan struct{}) }

func (f *FlowController) Update(c Credit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Capacity < int64(f.rate)/10 || c.Capacity > int64(f.rate)*30 ||
		c.Received < 0 || c.Received > f.state.Reserved || c.Played < 0 || c.Played > c.Received ||
		c.Buffered != c.Received-c.Played {
		return errors.New("invalid playback credit")
	}
	// Duplicate/older snapshots cannot mint credit or shrink a newer window.
	if c.Received < f.state.Received || c.Played < f.state.Played {
		return nil
	}
	f.state.Explicit = true
	f.state.Capacity = c.Capacity
	f.state.Received = c.Received
	f.state.Played = c.Played
	f.signalLocked()
	return nil
}

// Legacy acknowledgements replenish only the bounded two-second fallback.
func (f *FlowController) Acknowledge(played int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Explicit {
		return
	}
	played = min(played, f.state.Reserved)
	if played > f.state.Played {
		f.state.Played = played
		f.state.Received = max(f.state.Received, played)
		f.signalLocked()
	}
}

func (f *FlowController) Snapshot() FlowSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.state
	s.Available = max(int64(0), s.Capacity+s.Played-s.Reserved)
	return s
}

// Reserve may return a smaller packet to fit the current window. Reservations
// are never refunded: a failed/partial websocket write must cancel generation.
func (f *FlowController) Reserve(ctx context.Context, requested int64) (int64, error) {
	if requested <= 0 {
		return 0, errors.New("invalid audio reservation")
	}
	var started time.Time
	var timer *time.Timer
	var expired <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
			f.mu.Lock()
			f.state.WaitDuration += time.Since(started)
			f.mu.Unlock()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		f.mu.Lock()
		available := f.state.Capacity + f.state.Played - f.state.Reserved
		if available > 0 {
			n := min(requested, available)
			f.state.Reserved += n
			f.mu.Unlock()
			return n, nil
		}
		changed := f.changed
		if timer == nil {
			started = time.Now()
			timer = time.NewTimer(f.timeout)
			expired = timer.C
			f.state.WaitCount++
		}
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-expired:
			return 0, errors.New("audio credit timeout")
		case <-changed:
		}
	}
}
