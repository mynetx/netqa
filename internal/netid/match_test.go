package netid

import (
	"testing"

	"github.com/mynetx/netqa/internal/model"
)

// Real-world shape: a fiber and an LTE link from the same operator share one ASN
// and can be split ONLY by gateway MAC; two other operators each have a distinct
// ASN. All identifiers here are fictional (private-use ASNs, example MACs).
func TestMatchProvider(t *testing.T) {
	fiber := model.Provider{ID: 1, Name: "Fibernet", MatchMACs: "a1:b2:c3"}
	mobile := model.Provider{ID: 2, Name: "Fibernet Mobile", MatchMACs: "d4:e5:f6"}
	other := model.Provider{ID: 3, Name: "OtherISP", MatchMACs: "11:22:33", MatchASN: "OtherISP"}
	sat := model.Provider{ID: 4, Name: "SatNet", MatchASN: "SatNet,OrbitCo"}
	all := []model.Provider{fiber, mobile, other, sat}

	cases := []struct {
		name   string
		mac    string
		asn    string
		wantID int64
		wantOK bool
	}{
		{"OUI prefix picks fiber", "a1:b2:c3:19:5:9", "AS65000 Example Telecom", 1, true},
		{"OUI prefix picks mobile", "d4:e5:f6:c5:11:64", "AS65000 Example Telecom", 2, true},
		{"same ASN, MAC splits fiber vs mobile", "d4:e5:f6:00:00:01", "AS65000 Example Telecom", 2, true},
		{"other by unique ASN substring", "ff:ff:ff:00:00:01", "AS65010 OtherISP Networks", 3, true},
		{"other by MAC", "11:22:33:77:8a:3d", "AS65010 OtherISP Networks", 3, true},
		{"sat by ASN substring", "00:11:22:33:44:55", "AS65020 OrbitCo Satellite", 4, true},
		{"MAC match is case-insensitive", "A1:B2:C3:19:05:09", "", 1, true},
		{"no signal at all", "", "", 0, false},
		{"unknown mac and asn", "99:99:99:00:00:01", "AS65099 Unknown Telecom", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, ok := MatchProvider(all, c.mac, c.asn)
			if ok != c.wantOK || id != c.wantID {
				t.Fatalf("MatchProvider(%q,%q)=(%d,%v) want (%d,%v)",
					c.mac, c.asn, id, ok, c.wantID, c.wantOK)
			}
		})
	}
}

// MAC must decide when an ASN substring is ambiguous, and the matcher must refuse
// to guess when only an ambiguous ASN is available — never mislabel one same-ASN
// link as the other.
func TestMatchProviderMACBeatsAmbiguousASN(t *testing.T) {
	fiber := model.Provider{ID: 1, Name: "Fibernet", MatchMACs: "a1:b2:c3", MatchASN: "Example Telecom"}
	mobile := model.Provider{ID: 2, Name: "Fibernet Mobile", MatchMACs: "d4:e5:f6", MatchASN: "Example Telecom"}
	all := []model.Provider{fiber, mobile}

	if id, ok := MatchProvider(all, "d4:e5:f6:c5:11:64", "AS65000 Example Telecom"); !ok || id != 2 {
		t.Fatalf("MAC should decide over ambiguous ASN: got (%d,%v)", id, ok)
	}
	if id, ok := MatchProvider(all, "", "AS65000 Example Telecom"); ok {
		t.Fatalf("ambiguous ASN must not auto-assign: got (%d,%v)", id, ok)
	}
}

// Backfill: networks recorded before any rule existed get assigned from their
// stored MAC/ASN, while a network whose only signal is an ambiguous ASN stays
// unassigned.
func TestReassignUnassignedBackfill(t *testing.T) {
	s := newStore(t)
	fiber, err := s.UpsertProvider(model.Provider{Name: "Fibernet", MatchMACs: "a1:b2:c3"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.UpsertProvider(model.Provider{Name: "OtherISP", MatchASN: "OtherISP"})
	if err != nil {
		t.Fatal(err)
	}

	mustUpsert := func(n model.Network) {
		if _, err := s.UpsertNetwork(n); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsert(model.Network{GatewayMAC: "a1:b2:c3:19:5:9", ISPASN: "AS65000 Example Telecom"})
	mustUpsert(model.Network{GatewayMAC: "11:22:33:77:8a:3d", ISPASN: "AS65010 OtherISP Networks"})
	// Blank MAC + a shared/ambiguous ASN that no rule claims -> must stay unassigned.
	mustUpsert(model.Network{SSID: "blank", GatewayMAC: "", ISPASN: "AS65000 Example Telecom"})

	n, err := ReassignUnassigned(s)
	if err != nil {
		t.Fatalf("ReassignUnassigned: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 assignments, got %d", n)
	}

	gotF, _ := s.NetworkByFingerprint("", "a1:b2:c3:19:5:9")
	if gotF.ProviderID == nil || *gotF.ProviderID != fiber {
		t.Fatalf("fiber net not assigned: %+v", gotF)
	}
	gotO, _ := s.NetworkByFingerprint("", "11:22:33:77:8a:3d")
	if gotO.ProviderID == nil || *gotO.ProviderID != other {
		t.Fatalf("other net not assigned: %+v", gotO)
	}
	gotBlank, _ := s.NetworkByFingerprint("blank", "")
	if gotBlank.ProviderID != nil {
		t.Fatalf("ambiguous net must stay unassigned: %+v", gotBlank)
	}
}
