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

	// Recovery closes it.
	t1 := t0.Add(time.Minute)
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
