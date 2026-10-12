package web

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

// End to end through SQLite: SampleBuckets aggregates, pointsFromBuckets derives
// RTT/loss, and empty buckets stay as gaps.
func TestBucketedPoints(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	nid, _ := st.UpsertNetwork(model.Network{GatewayMAC: "aa"})

	from := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	to := from.Add(15 * time.Second)
	// 3 buckets of 5s. B0: 2 ok (10,20ms). B1: 1 ok (30) + 1 fail. B2: empty.
	for _, s := range []model.Sample{
		{TS: from.Add(1 * time.Second), Success: true, RTTms: 10},
		{TS: from.Add(2 * time.Second), Success: true, RTTms: 20},
		{TS: from.Add(6 * time.Second), Success: true, RTTms: 30},
		{TS: from.Add(7 * time.Second), Success: false},
		{TS: to, Success: true, RTTms: 99}, // outside [from,to)
	} {
		s.NetworkID, s.Target = nid, "x"
		if err := st.InsertSample(s); err != nil {
			t.Fatal(err)
		}
	}
	bs, err := st.SampleBuckets(nid, from, to, 3)
	if err != nil {
		t.Fatal(err)
	}
	pts := pointsFromBuckets(bs, from, to)
	if len(pts) != 3 {
		t.Fatalf("want 3 points, got %d", len(pts))
	}
	if pts[0].AvgRTT != 15 || pts[0].LossPct != 0 || pts[0].Samples != 2 {
		t.Fatalf("bucket0 wrong: %+v", pts[0])
	}
	if pts[1].AvgRTT != 30 || pts[1].LossPct != 50 || pts[1].Samples != 2 {
		t.Fatalf("bucket1 wrong: %+v", pts[1])
	}
	if pts[2].Samples != 0 || !pts[2].T.Equal(from.Add(10*time.Second)) {
		t.Fatalf("bucket2 should be an empty gap at +10s: %+v", pts[2])
	}
}
