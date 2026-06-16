// Package outage holds the connectivity-gap classifier: the defensible logic
// that decides whether a gap is the ISP's fault or self-inflicted (wifi off,
// asleep, local router down). Only ISP/upstream gaps count as evidence.
package outage

import "github.com/mynetx/netqa/internal/model"

// State is the measured environment at one decision point. InternetUp must be
// measured on the underlying physical link (not through a VPN tunnel) so a dead
// VPN exit is never mistaken for an ISP outage.
type State struct {
	Awake             bool
	WifiOn            bool
	GatewayUp         bool // default gateway / router reachable on the LAN
	InternetUp        bool // public internet reachable on the real link
	VPN               bool // a VPN tunnel is active (context only)
	PathBreaksPastISP bool // traceroute shows the break is beyond the ISP edge
}

// Classify returns whether the state represents an outage and, if so, its class.
//
//	Internet up                     -> no outage
//	Internet down + asleep          -> local (excluded from ISP accounting)
//	Internet down + wifi off        -> local
//	Internet down + gateway down    -> local (LAN/router problem, not provable ISP)
//	Internet down + gateway up      -> isp (or upstream if path breaks past ISP)
func Classify(s State) (bool, model.OutageClass) {
	if s.InternetUp {
		return false, ""
	}
	switch {
	case !s.Awake:
		return true, model.OutageLocal
	case !s.WifiOn:
		return true, model.OutageLocal
	case !s.GatewayUp:
		return true, model.OutageLocal
	case s.PathBreaksPastISP:
		return true, model.OutageUpstream
	default:
		return true, model.OutageISP
	}
}

// CountsAgainstISP reports whether a class is usable as evidence against the ISP.
func CountsAgainstISP(c model.OutageClass) bool {
	return c == model.OutageISP || c == model.OutageUpstream
}
