package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mynetx/netqa/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "netqa.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "netqa.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s.Close()
	// Re-opening must not fail re-running schema migrations.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	s2.Close()
}

func TestProviderCRUD(t *testing.T) {
	s := openTemp(t)

	id, err := s.UpsertProvider(model.Provider{Name: "My ISP", TargetDownMbit: 40, TargetUpMbit: 40})
	if err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero provider id")
	}

	// Editing the target (40 -> 20) must update in place, not duplicate.
	id2, err := s.UpsertProvider(model.Provider{ID: id, Name: "My ISP", TargetDownMbit: 20, TargetUpMbit: 20})
	if err != nil {
		t.Fatalf("UpsertProvider update: %v", err)
	}
	if id2 != id {
		t.Fatalf("update changed id: got %d want %d", id2, id)
	}

	ps, err := s.Providers()
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(ps))
	}
	if ps[0].TargetDownMbit != 20 {
		t.Fatalf("target not updated: got %v want 20", ps[0].TargetDownMbit)
	}
}

func TestNetworkUpsertByFingerprint(t *testing.T) {
	s := openTemp(t)

	n := model.Network{SSID: "Home-5G", GatewayMAC: "aa:bb:cc:dd:ee:ff"}
	id, err := s.UpsertNetwork(n)
	if err != nil {
		t.Fatalf("UpsertNetwork: %v", err)
	}

	// Same fingerprint => same row, even with new enrichment (ASN).
	n.ISPASN = "AS3320"
	id2, err := s.UpsertNetwork(n)
	if err != nil {
		t.Fatalf("UpsertNetwork again: %v", err)
	}
	if id2 != id {
		t.Fatalf("same fingerprint produced new row: %d vs %d", id, id2)
	}

	got, err := s.NetworkByFingerprint("Home-5G", "aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("NetworkByFingerprint: %v", err)
	}
	if got == nil || got.ISPASN != "AS3320" {
		t.Fatalf("enrichment not persisted: %+v", got)
	}

	// Different gateway MAC => different network.
	id3, err := s.UpsertNetwork(model.Network{SSID: "Home-5G", GatewayMAC: "11:22:33:44:55:66"})
	if err != nil {
		t.Fatalf("UpsertNetwork distinct: %v", err)
	}
	if id3 == id {
		t.Fatal("different gateway MAC collapsed into same network")
	}
}

func TestNetworkByFingerprintMissing(t *testing.T) {
	s := openTemp(t)
	got, err := s.NetworkByFingerprint("nope", "00:00:00:00:00:00")
	if err != nil {
		t.Fatalf("NetworkByFingerprint: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing network, got %+v", got)
	}
}

func TestInsertAndQuerySamples(t *testing.T) {
	s := openTemp(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa:aa:aa:aa:aa:aa"})

	base := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		err := s.InsertSample(model.Sample{
			NetworkID: nid,
			TS:        base.Add(time.Duration(i) * time.Second),
			Target:    "1.1.1.1",
			Success:   i != 1, // middle one failed
			RTTms:     12.5,
		})
		if err != nil {
			t.Fatalf("InsertSample: %v", err)
		}
	}

	got, err := s.SamplesBetween(nid, base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("SamplesBetween: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(got))
	}
	if got[1].Success {
		t.Fatalf("expected sample[1] failed")
	}
}

func TestOutageOpenAndClose(t *testing.T) {
	s := openTemp(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa:aa:aa:aa:aa:aa"})
	start := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)

	oid, err := s.OpenOutage(model.Outage{NetworkID: nid, Start: start, Class: model.OutageISP})
	if err != nil {
		t.Fatalf("OpenOutage: %v", err)
	}

	open, err := s.OngoingOutage(nid)
	if err != nil {
		t.Fatalf("OngoingOutage: %v", err)
	}
	if open == nil || open.ID != oid {
		t.Fatalf("expected ongoing outage %d, got %+v", oid, open)
	}

	end := start.Add(2 * time.Minute)
	if err := s.CloseOutage(oid, end); err != nil {
		t.Fatalf("CloseOutage: %v", err)
	}
	open, err = s.OngoingOutage(nid)
	if err != nil {
		t.Fatalf("OngoingOutage after close: %v", err)
	}
	if open != nil {
		t.Fatalf("expected no ongoing outage, got %+v", open)
	}

	outs, err := s.OutagesBetween(nid, start.Add(-time.Hour), end.Add(time.Hour))
	if err != nil {
		t.Fatalf("OutagesBetween: %v", err)
	}
	if len(outs) != 1 || !outs[0].End.Equal(end) {
		t.Fatalf("unexpected outages: %+v", outs)
	}
}

func TestInsertThroughputDNSPower(t *testing.T) {
	s := openTemp(t)
	nid, _ := s.UpsertNetwork(model.Network{SSID: "Home", GatewayMAC: "aa:aa:aa:aa:aa:aa"})
	ts := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)

	if err := s.InsertThroughput(model.Throughput{NetworkID: nid, TS: ts, DownMbit: 18.2, UpMbit: 9.1}); err != nil {
		t.Fatalf("InsertThroughput: %v", err)
	}
	if err := s.InsertDNS(model.DNSResult{NetworkID: nid, TS: ts, Server: "1.1.1.1", Host: "example.com", Success: true, LatencyMs: 30}); err != nil {
		t.Fatalf("InsertDNS: %v", err)
	}
	if err := s.InsertPowerEvent(model.PowerEvent{TS: ts, Kind: model.PowerSleep}); err != nil {
		t.Fatalf("InsertPowerEvent: %v", err)
	}

	tps, err := s.ThroughputBetween(nid, ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil {
		t.Fatalf("ThroughputBetween: %v", err)
	}
	if len(tps) != 1 || tps[0].DownMbit != 18.2 {
		t.Fatalf("unexpected throughput: %+v", tps)
	}

	pes, err := s.PowerEventsBetween(ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil {
		t.Fatalf("PowerEventsBetween: %v", err)
	}
	if len(pes) != 1 || pes[0].Kind != model.PowerSleep {
		t.Fatalf("unexpected power events: %+v", pes)
	}
}
