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

// bucketSamples groups samples into `buckets` equal time buckets over [from,to],
// computing average successful RTT and loss% per bucket. Empty buckets are kept
// (Samples==0) so the chart shows gaps rather than interpolating across them.
func bucketSamples(samples []model.Sample, from, to time.Time, buckets int) []Point {
	if buckets < 1 {
		buckets = 1
	}
	span := to.Sub(from)
	if span <= 0 {
		return nil
	}
	width := span / time.Duration(buckets)
	if width <= 0 {
		width = time.Nanosecond
	}

	type acc struct {
		rttSum float64
		ok     int
		total  int
	}
	accs := make([]acc, buckets)
	for _, s := range samples {
		idx := int(s.TS.Sub(from) / width)
		if idx < 0 || idx >= buckets {
			continue
		}
		accs[idx].total++
		if s.Success {
			accs[idx].ok++
			accs[idx].rttSum += s.RTTms
		}
	}

	out := make([]Point, buckets)
	for i := range accs {
		p := Point{T: from.Add(time.Duration(i) * width), Samples: accs[i].total}
		if accs[i].total > 0 {
			p.LossPct = 100 * float64(accs[i].total-accs[i].ok) / float64(accs[i].total)
		}
		if accs[i].ok > 0 {
			p.AvgRTT = accs[i].rttSum / float64(accs[i].ok)
		}
		out[i] = p
	}
	return out
}
