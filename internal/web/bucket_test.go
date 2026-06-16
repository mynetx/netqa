package web

import (
	"testing"
	"time"

	"github.com/mynetx/netqa/internal/model"
)

func TestBucketSamples(t *testing.T) {
	from := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Second)
	// 2 buckets of 5s. Bucket0: 2 ok (10,20ms). Bucket1: 1 ok(30) + 1 fail.
	samples := []model.Sample{
		{TS: from.Add(1 * time.Second), Success: true, RTTms: 10},
		{TS: from.Add(2 * time.Second), Success: true, RTTms: 20},
		{TS: from.Add(6 * time.Second), Success: true, RTTms: 30},
		{TS: from.Add(7 * time.Second), Success: false},
	}
	pts := bucketSamples(samples, from, to, 2)
	if len(pts) != 2 {
		t.Fatalf("want 2 points, got %d", len(pts))
	}
	if pts[0].AvgRTT != 15 || pts[0].LossPct != 0 || pts[0].Samples != 2 {
		t.Fatalf("bucket0 wrong: %+v", pts[0])
	}
	if pts[1].AvgRTT != 30 || pts[1].LossPct != 50 || pts[1].Samples != 2 {
		t.Fatalf("bucket1 wrong: %+v", pts[1])
	}
}

func TestBucketSamplesEmptyKeepsGaps(t *testing.T) {
	from := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	to := from.Add(6 * time.Second)
	samples := []model.Sample{{TS: from.Add(1 * time.Second), Success: true, RTTms: 5}}
	pts := bucketSamples(samples, from, to, 3)
	if len(pts) != 3 {
		t.Fatalf("want 3, got %d", len(pts))
	}
	if pts[0].Samples != 1 {
		t.Fatalf("bucket0 should have 1 sample: %+v", pts[0])
	}
	if pts[1].Samples != 0 || pts[2].Samples != 0 {
		t.Fatalf("empty buckets should remain empty: %+v", pts)
	}
}
