package env

import (
	"regexp"
	"strings"
	"time"
)

var (
	reGateway = regexp.MustCompile(`(?m)^\s*gateway:\s*([0-9.]+)`)
	reARPMAC  = regexp.MustCompile(`at ([0-9a-fA-F]{1,2}(?::[0-9a-fA-F]{1,2}){5}) on`)
	rePmset   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [+-]\d{4})\s+(Sleep|Wake)\b`)
)

// parseDefaultGatewayIP extracts the gateway IP from `route -n get default`.
func parseDefaultGatewayIP(routeOut string) string {
	m := reGateway.FindStringSubmatch(routeOut)
	if m == nil {
		return ""
	}
	return m[1]
}

// parseGatewayMAC extracts the MAC from `arp -n <gateway>`; "" if incomplete.
func parseGatewayMAC(arpOut string) string {
	m := reARPMAC.FindStringSubmatch(arpOut)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// parsePhysicalDefaultRoute scans `netstat -rn -f inet` and returns the gateway
// IP and interface of the default route bound to a non-tunnel interface. This is
// the real LAN gateway even when a VPN owns the primary default route, so it
// gives a VPN-proof network identity. Returns "","" when only tunnels exist.
func parsePhysicalDefaultRoute(netstatOut string) (gateway, iface string) {
	for _, line := range strings.Split(netstatOut, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "default" {
			continue
		}
		gw, nif := f[1], f[len(f)-1]
		if isTunnelIface(nif) {
			continue
		}
		// Gateway must be a real IP (skip "link#NN" entries).
		if !strings.Contains(gw, ".") {
			continue
		}
		return gw, nif
	}
	return "", ""
}

// parseSSID extracts the SSID from `networksetup -getairportnetwork <iface>`.
func parseSSID(out string) string {
	const prefix = "Current Wi-Fi Network: "
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// vpnActiveInet reports whether a VPN carries traffic, judged ONLY from the IPv4
// (`netstat -rn -f inet`) table: true when the primary default route's interface
// is a tunnel. This deliberately ignores the idle utun0..N interfaces macOS keeps
// for system services — those only appear as IPv6 link-local defaults and must
// not be mistaken for a VPN.
func vpnActiveInet(netstatInetOut string) bool {
	for _, line := range strings.Split(netstatInetOut, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "default" {
			continue
		}
		// First default route is the active one (highest priority).
		return isTunnelIface(f[len(f)-1])
	}
	return false
}

func isTunnelIface(s string) bool {
	return strings.HasPrefix(s, "utun") || strings.HasPrefix(s, "ppp") ||
		strings.HasPrefix(s, "ipsec") || strings.HasPrefix(s, "tun")
}

// pmEvent is a parsed power transition.
type pmEvent struct {
	TS   time.Time
	Kind string // "sleep" | "wake"
}

// parsePmsetLog extracts sleep/wake transitions from `pmset -g log`.
func parsePmsetLog(out string) []pmEvent {
	var events []pmEvent
	for _, line := range strings.Split(out, "\n") {
		m := rePmset.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts, err := time.Parse("2006-01-02 15:04:05 -0700", m[1])
		if err != nil {
			continue
		}
		kind := "sleep"
		if m[2] == "Wake" {
			kind = "wake"
		}
		events = append(events, pmEvent{TS: ts, Kind: kind})
	}
	return events
}
