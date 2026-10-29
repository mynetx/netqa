package netid

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mynetx/netqa/internal/env"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

func TestShouldLookupASN(t *testing.T) {
	cases := []struct {
		vpn      bool
		existing string
		want     bool
	}{
		{vpn: false, existing: "", want: true},        // no vpn, unknown -> look up
		{vpn: true, existing: "", want: false},        // vpn up -> never look up (poisoned)
		{vpn: false, existing: "AS3320", want: false}, // already known -> skip
		{vpn: true, existing: "AS3320", want: false},
	}
	for _, c := range cases {
		if got := shouldLookupASN(c.vpn, c.existing); got != c.want {
			t.Fatalf("shouldLookupASN(%v,%q)=%v want %v", c.vpn, c.existing, got, c.want)
		}
	}
}

type fakeFetcher struct {
	calls int
	asn   string
}

func (f *fakeFetcher) ASN(ctx context.Context) (string, error) {
	f.calls++
	return f.asn, nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestResolveCreatesNetworkAndEnriches(t *testing.T) {
	s := newStore(t)
	f := &fakeFetcher{asn: "AS64500 Example ISP"}
	r := &Resolver{Store: s, Fetcher: f}

	snap := env.Snapshot{SSID: "Home-5G", GatewayMAC: "aa:bb:cc:dd:ee:ff", VPN: false}
	id, err := r.Resolve(context.Background(), snap)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if id == 0 {
		t.Fatal("expected network id")
	}
	if f.calls != 1 {
		t.Fatalf("expected 1 ASN lookup, got %d", f.calls)
	}
	got, _ := s.NetworkByFingerprint("Home-5G", "aa:bb:cc:dd:ee:ff")
	if got.ISPASN != "AS64500 Example ISP" {
		t.Fatalf("ASN not persisted: %q", got.ISPASN)
	}

	// Second resolve: ASN already known -> no further lookup.
	if _, err := r.Resolve(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("expected no extra ASN lookup, got %d", f.calls)
	}
}

func TestResolveAutoAssignsProviderByMAC(t *testing.T) {
	s := newStore(t)
	pid, err := s.UpsertProvider(model.Provider{Name: "Fibernet", MatchMACs: "a1:b2:c3"})
	if err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	r := &Resolver{Store: s, Fetcher: &fakeFetcher{asn: "AS65000 Example Telecom"}}

	snap := env.Snapshot{GatewayMAC: "a1:b2:c3:77:8a:3d", VPN: false}
	if _, err := r.Resolve(context.Background(), snap); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, _ := s.NetworkByFingerprint("", "a1:b2:c3:77:8a:3d")
	if got == nil || got.ProviderID == nil || *got.ProviderID != pid {
		t.Fatalf("network not auto-assigned to provider %d: %+v", pid, got)
	}
}

func TestResolveDoesNotOverrideManualProvider(t *testing.T) {
	s := newStore(t)
	if _, err := s.UpsertProvider(model.Provider{Name: "Auto", MatchMACs: "a1:b2:c3"}); err != nil {
		t.Fatal(err)
	}
	manual, err := s.UpsertProvider(model.Provider{Name: "Manual"})
	if err != nil {
		t.Fatal(err)
	}
	// Network is already assigned to the manual provider by hand.
	if _, err := s.UpsertNetwork(model.Network{GatewayMAC: "a1:b2:c3:77:8a:3d", ProviderID: &manual}); err != nil {
		t.Fatal(err)
	}

	r := &Resolver{Store: s, Fetcher: &fakeFetcher{asn: "AS65000 Example Telecom"}}
	if _, err := r.Resolve(context.Background(), env.Snapshot{GatewayMAC: "a1:b2:c3:77:8a:3d"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, _ := s.NetworkByFingerprint("", "a1:b2:c3:77:8a:3d")
	if got == nil || got.ProviderID == nil || *got.ProviderID != manual {
		t.Fatalf("auto-assign overrode manual provider %d: %+v", manual, got)
	}
}

func TestResolveUnderVPNSkipsASN(t *testing.T) {
	s := newStore(t)
	f := &fakeFetcher{asn: "AS-WRONG-VPN-EXIT"}
	r := &Resolver{Store: s, Fetcher: f}

	snap := env.Snapshot{SSID: "Home-5G", GatewayMAC: "aa:bb:cc:dd:ee:ff", VPN: true}
	if _, err := r.Resolve(context.Background(), snap); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("ASN lookup must NOT run under VPN, got %d calls", f.calls)
	}
	got, _ := s.NetworkByFingerprint("Home-5G", "aa:bb:cc:dd:ee:ff")
	if got.ISPASN != "" {
		t.Fatalf("VPN exit ASN leaked into network identity: %q", got.ISPASN)
	}
	if ps := mustProviders(t, s); len(ps) != 0 || got.ProviderID != nil {
		t.Fatalf("VPN must never auto-create or assign a provider: %+v, %+v", ps, got)
	}
}

// A brand-new network on a real (off-VPN) ASN that no provider claims registers
// its own provider keyed on the AS number; a second network on the same ASN then
// matches that provider instead of creating another.
func TestResolveAutoCreatesProviderFromUnmatchedASN(t *testing.T) {
	s := newStore(t)
	r := &Resolver{Store: s, Fetcher: &fakeFetcher{asn: "AS64496 Example Satellite Networks"}}

	for _, mac := range []string{"02:00:5e:10:00:01", "02:00:5e:10:00:02"} {
		if _, err := r.Resolve(context.Background(), env.Snapshot{GatewayMAC: mac}); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}

	ps := mustProviders(t, s)
	if len(ps) != 1 || ps[0].Name != "Example Satellite Networks" || ps[0].MatchASN != "AS64496" {
		t.Fatalf("want exactly 1 auto-created provider, got %+v", ps)
	}
	for _, mac := range []string{"02:00:5e:10:00:01", "02:00:5e:10:00:02"} {
		got, _ := s.NetworkByFingerprint("", mac)
		if got == nil || got.ProviderID == nil || *got.ProviderID != ps[0].ID {
			t.Fatalf("network %s not linked to auto-created provider: %+v", mac, got)
		}
	}
}

// An ASN matched by two providers is ambiguous: it must stay unassigned and must
// not spawn a third provider (which would only deepen the ambiguity every tick).
func TestResolveAmbiguousASNDoesNotAutoCreate(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"Fiber", "LTE"} {
		if _, err := s.UpsertProvider(model.Provider{Name: name, MatchASN: "Example Telecom"}); err != nil {
			t.Fatal(err)
		}
	}
	r := &Resolver{Store: s, Fetcher: &fakeFetcher{asn: "AS65000 Example Telecom"}}
	if _, err := r.Resolve(context.Background(), env.Snapshot{GatewayMAC: "aa:bb:cc:dd:ee:ff"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ps := mustProviders(t, s); len(ps) != 2 {
		t.Fatalf("ambiguous ASN must not create a provider, got %+v", ps)
	}
}

// The backfill auto-creates too, once per ASN, for networks stored before.
func TestReassignUnassignedAutoCreates(t *testing.T) {
	s := newStore(t)
	for _, mac := range []string{"11:11:11:11:11:11", "22:22:22:22:22:22"} {
		if _, err := s.UpsertNetwork(model.Network{GatewayMAC: mac, ISPASN: "AS64500 Example ISP"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := ReassignUnassigned(s)
	if err != nil || n != 2 {
		t.Fatalf("ReassignUnassigned = %d, %v; want 2, nil", n, err)
	}
	if ps := mustProviders(t, s); len(ps) != 1 || ps[0].MatchASN != "AS64500" {
		t.Fatalf("want 1 provider for the shared ASN, got %+v", ps)
	}
}

func mustProviders(t *testing.T, s *store.Store) []model.Provider {
	t.Helper()
	ps, err := s.Providers()
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestResolveSeparatesHiddenMACNetworksByASN(t *testing.T) {
	s := newStore(t)
	asn := "AS64510 Example Fiber"
	lookups := 0
	r := &Resolver{Store: s, LinkASN: func(context.Context, string) (string, error) { lookups++; return asn, nil }}
	snap := env.Snapshot{GatewayMAC: "ip:192.168.178.1", Iface: "en0", VPN: true}

	gm, err := r.Resolve(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), snap); err != nil || lookups != 1 {
		t.Fatalf("second tick should reuse the cached key: lookups=%d err=%v", lookups, err)
	}
	n, _ := s.NetworkByFingerprint("", "ip:192.168.178.1 AS64510")
	if n == nil || n.ID != gm || n.ISPASN != asn {
		t.Fatalf("network = %+v, want id %d keyed on IP+ASN with ASN %q despite VPN", n, gm, asn)
	}

	// Same router IP behind another ISP (a FRITZ!Box at home) is a different network.
	asn, r.hidAt = "AS64511 Example Telecom", time.Time{}
	de, err := r.Resolve(context.Background(), snap)
	if err != nil || de == gm {
		t.Fatalf("same IP, other ASN must be a new network: got %d (gm %d) err=%v", de, gm, err)
	}
}
