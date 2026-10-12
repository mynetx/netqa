package web

import (
	"time"

	"github.com/mynetx/netqa/internal/model"
)

// Point is one aggregated chart bucket.
type Point struct {
	T       time.Time `json:"t"`
	AvgRTT  float64   `json:"avg_rtt_ms"`
	LossPct float64   `json:"loss_pct"`
	Samples int       `json:"samples"`
}

// pointsFromBuckets turns equal-width sample buckets starting at from into chart
// points: average successful RTT and loss% per bucket. Empty buckets are kept
// (Samples==0) so the chart shows gaps rather than interpolating across them.
func pointsFromBuckets(bs []model.SampleBucket, from, to time.Time) []Point {
	if len(bs) == 0 || !to.After(from) {
		return nil
	}
	width := to.Sub(from) / time.Duration(len(bs))
	out := make([]Point, len(bs))
	for i, b := range bs {
		p := Point{T: from.Add(time.Duration(i) * width), Samples: b.Total}
		if b.Total > 0 {
			p.LossPct = 100 * float64(b.Total-b.OK) / float64(b.Total)
		}
		if b.OK > 0 {
			p.AvgRTT = b.RTTSum / float64(b.OK)
		}
		out[i] = p
	}
	return out
}
