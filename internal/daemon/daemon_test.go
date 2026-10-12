package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mynetx/netqa/internal/config"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

func newDaemon(t *testing.T) (*Daemon, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d := New(config.Default(), s, nil)
	return d, s
}

func TestManageOutageLifecycle(t *testing.T) {
	d, s := newDaemon(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa"})
	t0 := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)

	var opened, closed *model.Outage
	d.OnOutageOpen = func(o model.Outage) { c := o; opened = &c }
	d.OnOutageClose = func(o model.Outage) { c := o; closed = &c }

	// ISP outage starts.
	d.manageOutage(nid, t0, false, true, model.OutageISP)
	if opened == nil || opened.Class != model.OutageISP {
		t.Fatalf("expected open ISP outage, got %+v", opened)
	}
	if og, _ := s.OngoingOutage(nid); og == nil {
		t.Fatal("expected ongoing outage persisted")
	}

	// Same outage continues: no duplicate open.
	opened = nil
	d.manageOutage(nid, t0.Add(5*time.Second), false, true, model.OutageISP)
	if opened != nil {
		t.Fatal("must not re-open an already-open outage")
	}

	// Recovery on the next tick closes it.
	t1 := t0.Add(10 * time.Second)
	d.manageOutage(nid, t1, false, false, "")
	if closed == nil || !closed.End.Equal(t1) {
		t.Fatalf("expected close at %v, got %+v", t1, closed)
	}
	if og, _ := s.OngoingOutage(nid); og != nil {
		t.Fatal("expected no ongoing outage after recovery")
	}
}

func TestManageOutageIgnoresLocal(t *testing.T) {
	d, s := newDaemon(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa"})
	t0 := time.Now()

	called := false
	d.OnOutageOpen = func(model.Outage) { called = true }

	// Local gap (wifi off / asleep) must never be recorded as an outage.
	d.manageOutage(nid, t0, false, true, model.OutageLocal)
	if called {
		t.Fatal("local gap must not open an outage")
	}
	if og, _ := s.OngoingOutage(nid); og != nil {
		t.Fatal("local gap must not persist an outage")
	}
}

// A gap in observation (the Mac slept, the daemon was down) must not be billed
// to the ISP: an outage open before the gap closes at the last observed tick.
func TestManageOutageGapClosesAtLastObservation(t *testing.T) {
	d, s := newDaemon(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa"})
	t0 := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)

	var closed *model.Outage
	d.OnOutageClose = func(o model.Outage) { c := o; closed = &c }

	d.manageOutage(nid, t0, false, true, model.OutageISP)
	d.manageOutage(nid, t0.Add(5*time.Second), false, true, model.OutageISP)

	// Eight hours of sleep, then the line is fine.
	d.manageOutage(nid, t0.Add(8*time.Hour), false, false, "")
	if closed == nil || !closed.End.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("outage must end at the last observation before the gap, got %+v", closed)
	}
}

// After a daemon restart (no in-process history) the last stored sample is the
// last observation, so a stale open outage is capped there too.
func TestManageOutageRestartUsesLastSample(t *testing.T) {
	d, s := newDaemon(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa"})
	t0 := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	if _, err := s.OpenOutage(model.Outage{NetworkID: nid, Start: t0, Class: model.OutageISP}); err != nil {
		t.Fatal(err)
	}
	last := t0.Add(30 * time.Second)
	if err := s.InsertSample(model.Sample{NetworkID: nid, TS: last, Target: "x"}); err != nil {
		t.Fatal(err)
	}

	var closed *model.Outage
	d.OnOutageClose = func(o model.Outage) { c := o; closed = &c }
	d.manageOutage(nid, t0.Add(2*time.Hour), false, false, "")
	if closed == nil || !closed.End.Equal(last) {
		t.Fatalf("stale outage must end at the last stored sample %v, got %+v", last, closed)
	}
}
