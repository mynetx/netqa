package netid

import (
	"context"
	"path/filepath"
	"testing"

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
}
