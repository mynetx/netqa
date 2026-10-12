package netid

import (
	"strings"

	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

// ReassignUnassigned assigns a provider to every network that has none yet,
// using each network's stored gateway MAC and ASN (see assignProvider). It is the
// backfill counterpart to Resolve: it links networks recorded before any rule
// existed (or before a newly added rule). Returns the number of networks newly
// assigned.
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
		pid, _, err := assignProvider(s, &ps, n.GatewayMAC, n.ISPASN)
		if err != nil {
			return count, err
		}
		if pid == 0 {
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

// assignProvider picks the provider for an unassigned network: the single
// provider whose rules match, or — when no rule matches at all and asn is a real
// "AS<num> <org>" line — a newly registered provider keyed on that AS number, so
// a new ISP attributes itself without manual setup. An ambiguous match is never
// resolved by creating yet another provider. A created provider is appended to
// *ps so later calls in the same batch match it instead of creating a duplicate.
// It returns the provider id (0 = leave unassigned) and the provider it created,
// if any.
func assignProvider(s *store.Store, ps *[]model.Provider, gatewayMAC, asn string) (int64, *model.Provider, error) {
	switch hits := matchHits(*ps, gatewayMAC, asn); len(hits) {
	case 1:
		return hits[0], nil, nil
	case 0:
		name, rule, ok := providerFromASN(asn)
		if !ok {
			return 0, nil, nil
		}
		p := model.Provider{Name: name, MatchASN: rule}
		id, err := s.UpsertProvider(p)
		if err != nil {
			return 0, nil, err
		}
		p.ID = id
		*ps = append(*ps, p)
		return id, &p, nil
	default:
		return 0, nil, nil // ambiguous — do not guess
	}
}

// MatchProvider picks the provider whose auto-assign rules fit the given gateway
// MAC or ASN-org string. Gateway-MAC rules are authoritative and tried first, so
// same-ASN links (e.g. a fiber and an LTE link from one operator, sharing an ASN)
// are split by the only signal that can tell them apart; ASN rules are a
// fallback for new devices of a known operator. It returns (id, true) only on a
// single unambiguous match and (0, false) otherwise — when nothing matches, or the
// available signal matches two providers, it refuses to guess rather than mislabel.
func MatchProvider(ps []model.Provider, gatewayMAC, asn string) (int64, bool) {
	if hits := matchHits(ps, gatewayMAC, asn); len(hits) == 1 {
		return hits[0], true
	}
	return 0, false
}

// matchHits returns the provider ids matched by the deciding signal: MAC hits if
// any MAC rule fires, otherwise ASN hits.
func matchHits(ps []model.Provider, gatewayMAC, asn string) []int64 {
	if hits := matchByMAC(ps, strings.ToLower(strings.TrimSpace(gatewayMAC))); len(hits) > 0 {
		return hits
	}
	return matchByASN(ps, strings.ToLower(strings.TrimSpace(asn)))
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

// matchByASN returns the distinct provider ids whose MatchASN fits asn: an
// AS-number rule ("as13335") must equal asn's leading AS token exactly, so it
// never prefix-matches a longer number; any other rule is a substring of asn.
// asn must already be lower-cased.
func matchByASN(ps []model.Provider, asn string) []int64 {
	if asn == "" {
		return nil
	}
	head, _, _ := strings.Cut(asn, " ")
	var hits []int64
	for _, p := range ps {
		for _, tok := range splitRules(p.MatchASN) {
			if tok == head || (!isASToken(tok) && strings.Contains(asn, tok)) {
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

// providerFromASN turns an ipinfo org line ("AS13335 Cloudflare, Inc.") into a
// display name (the org) and a match_asn rule (the AS number, which is stable,
// unique and comma-free). ok is false unless the input is a real
// "AS<digits> <org>" line, so an empty/rate-limited/garbage ASN never spawns a
// provider.
func providerFromASN(asn string) (name, rule string, ok bool) {
	head, org, _ := strings.Cut(strings.TrimSpace(asn), " ")
	org = strings.TrimSpace(org)
	if !isASToken(head) || org == "" {
		return "", "", false
	}
	return org, strings.ToUpper(head), true
}

// isASToken reports whether s is "AS" (any case) followed by at least one digit.
func isASToken(s string) bool {
	return len(s) > 2 && strings.EqualFold(s[:2], "AS") && strings.Trim(s[2:], "0123456789") == ""
}
