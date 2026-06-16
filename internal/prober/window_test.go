package prober

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestWindowLossPct(t *testing.T) {
	tests := []struct {
		name    string
		results []bool
		want    float64
	}{
		{"all up", []bool{true, true, true, true}, 0},
		{"all down", []bool{false, false}, 100},
		{"half", []bool{true, false, true, false}, 50},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWindow(10)
			base := time.Now()
			for i, ok := range tt.results {
				w.Add(Result{TS: base.Add(time.Duration(i) * time.Second), Success: ok, RTT: 10 * time.Millisecond})
			}
			if got := w.LossPct(); !approx(got, tt.want) {
				t.Fatalf("LossPct = %v want %v", got, tt.want)
			}
		})
	}
}

func TestWindowAvgRTTIgnoresFailures(t *testing.T) {
	w := NewWindow(10)
	base := time.Now()
	w.Add(Result{TS: base, Success: true, RTT: 10 * time.Millisecond})
	w.Add(Result{TS: base.Add(time.Second), Success: false}) // ignored
	w.Add(Result{TS: base.Add(2 * time.Second), Success: true, RTT: 20 * time.Millisecond})
	if got := w.AvgRTTms(); !approx(got, 15) {
		t.Fatalf("AvgRTTms = %v want 15", got)
	}
}

func TestWindowJitter(t *testing.T) {
	// RTTs 10,14,11 -> consecutive diffs |4|,|3| -> mean 3.5ms.
	w := NewWindow(10)
	base := time.Now()
	for i, rtt := range []time.Duration{10, 14, 11} {
		w.Add(Result{TS: base.Add(time.Duration(i) * time.Second), Success: true, RTT: rtt * time.Millisecond})
	}
	if got := w.JitterMs(); !approx(got, 3.5) {
		t.Fatalf("JitterMs = %v want 3.5", got)
	}
}

func TestWindowCapEvicts(t *testing.T) {
	w := NewWindow(3)
	base := time.Now()
	// 5 results, cap 3 -> only last 3 retained. Last 3 all failures => 100% loss.
	seq := []bool{true, true, false, false, false}
	for i, ok := range seq {
		w.Add(Result{TS: base.Add(time.Duration(i) * time.Second), Success: ok, RTT: 10 * time.Millisecond})
	}
	if got := w.LossPct(); !approx(got, 100) {
		t.Fatalf("LossPct = %v want 100 (eviction failed)", got)
	}
	if w.Len() != 3 {
		t.Fatalf("Len = %d want 3", w.Len())
	}
}
