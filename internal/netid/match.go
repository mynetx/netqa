package netid

import (
	"strings"

	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

// ReassignUnassigned applies provider match rules to every network that has no
// provider yet, using each network's stored gateway MAC and ASN. It is the
// backfill counterpart to Resolve: it links networks recorded before any rule
// existed (or before a newly added rule). A network whose signal is unknown or
// ambiguous is left unassigned. Returns the number of networks newly assigned.
func ReassignUnassigned(s *store.Store) (int, error) {
	ps, err := s.Providers()
	if err != nil {
		return 0, err
	}
	nets, err := s.Networks()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, n := range nets {
		if n.ProviderID != nil {
			continue
		}
		pid, ok := MatchProvider(ps, n.GatewayMAC, n.ISPASN)
		if !ok {
			continue
		}
		n.ProviderID = &pid
		if _, err := s.UpsertNetwork(n); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// MatchProvider picks the provider whose auto-assign rules fit the given gateway
// MAC or ASN-org string. Gateway-MAC rules are authoritative and tried first, so
// same-ASN links (e.g. a fiber and an LTE link from one operator, sharing an ASN)
// are split by the only signal that can tell them apart; ASN-substring rules are a
// fallback for new devices of a known operator. It returns (id, true) only on a
// single unambiguous match and (0, false) otherwise — when nothing matches, or the
// available signal matches two providers, it refuses to guess rather than mislabel.
func MatchProvider(ps []model.Provider, gatewayMAC, asn string) (int64, bool) {
	switch hits := matchByMAC(ps, strings.ToLower(strings.TrimSpace(gatewayMAC))); {
	case len(hits) == 1:
		return hits[0], true
	case len(hits) > 1:
		return 0, false // ambiguous MAC config — do not guess
	}
	if hits := matchByASN(ps, strings.ToLower(asn)); len(hits) == 1 {
		return hits[0], true
	}
	return 0, false
}

// matchByMAC returns the distinct provider ids whose MatchMACs contains an exact
// MAC or an OUI prefix of mac. mac must already be lower-cased.
func matchByMAC(ps []model.Provider, mac string) []int64 {
	if mac == "" {
		return nil
	}
	var hits []int64
	for _, p := range ps {
		for _, tok := range splitRules(p.MatchMACs) {
			if mac == tok || strings.HasPrefix(mac, tok+":") {
				hits = append(hits, p.ID)
				break
			}
		}
	}
	return hits
}

// matchByASN returns the distinct provider ids whose MatchASN has a substring of
// asn. asn must already be lower-cased.
func matchByASN(ps []model.Provider, asn string) []int64 {
	if asn == "" {
		return nil
	}
	var hits []int64
	for _, p := range ps {
		for _, tok := range splitRules(p.MatchASN) {
			if strings.Contains(asn, tok) {
				hits = append(hits, p.ID)
				break
			}
		}
	}
	return hits
}

// splitRules splits a comma-separated rule list into trimmed, lower-cased,
// non-empty tokens.
func splitRules(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.ToLower(strings.TrimSpace(part)); t != "" {
			out = append(out, t)
		}
	}
	return out
}
