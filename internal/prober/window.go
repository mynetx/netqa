// Package prober probes connectivity targets and maintains rolling connectivity
// statistics (loss %, average RTT, jitter) over a sliding window.
package prober

import (
	"math"
	"time"
)

// Result is one probe outcome against one target.
type Result struct {
	TS      time.Time
	Target  string
	Success bool
	RTT     time.Duration // valid only when Success
}

// Window is a fixed-capacity ring of recent Results used to compute rolling
// statistics. It is not safe for concurrent use; callers serialise access.
type Window struct {
	cap     int
	results []Result
}

// NewWindow returns a Window retaining the most recent cap results.
func NewWindow(capacity int) *Window {
	if capacity < 1 {
		capacity = 1
	}
	return &Window{cap: capacity}
}

// Add appends a result, evicting the oldest when capacity is exceeded.
func (w *Window) Add(r Result) {
	w.results = append(w.results, r)
	if len(w.results) > w.cap {
		w.results = w.results[len(w.results)-w.cap:]
	}
}

// Len returns the number of retained results.
func (w *Window) Len() int { return len(w.results) }

// LossPct returns packet loss as a percentage over the window (0 when empty).
func (w *Window) LossPct() float64 {
	if len(w.results) == 0 {
		return 0
	}
	var lost int
	for _, r := range w.results {
		if !r.Success {
			lost++
		}
	}
	return 100 * float64(lost) / float64(len(w.results))
}

// AvgRTTms returns the mean RTT in milliseconds over successful probes only.
func (w *Window) AvgRTTms() float64 {
	var sum float64
	var n int
	for _, r := range w.results {
		if r.Success {
			sum += float64(r.RTT) / float64(time.Millisecond)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// JitterMs returns mean absolute difference between consecutive successful RTTs,
// in milliseconds (the common "average jitter" definition). 0 with <2 successes.
func (w *Window) JitterMs() float64 {
	var prev float64
	var have bool
	var sum float64
	var n int
	for _, r := range w.results {
		if !r.Success {
			continue
		}
		cur := float64(r.RTT) / float64(time.Millisecond)
		if have {
			sum += math.Abs(cur - prev)
			n++
		}
		prev = cur
		have = true
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
