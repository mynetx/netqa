// Package env observes the local machine/network environment on macOS so the
// outage classifier can distinguish self-inflicted gaps from ISP faults.
package env

import (
	"context"
	"os/exec"
	"strings"
)

// Snapshot is the current local network environment.
type Snapshot struct {
	GatewayIP  string
	GatewayMAC string
	SSID       string
	Iface      string // physical default-route interface (e.g. en0)
	LinkUp     bool   // a usable physical link exists (wired or Wi-Fi)
	VPN        bool
}

// WiFiInterface is the macOS Wi-Fi device. en0 on most Macs; overridable.
var WiFiInterface = "en0"

func run(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// Observe gathers a fresh Snapshot using macOS CLI tools. Missing pieces are
// returned empty/false rather than erroring, so a partial environment still
// yields a usable snapshot.
func Observe(ctx context.Context) Snapshot {
	var s Snapshot

	// Use the physical (non-tunnel) default route so a VPN cannot hide the real
	// LAN gateway — this anchors a VPN-proof network identity.
	netstatOut := run(ctx, "netstat", "-rn", "-f", "inet")
	s.GatewayIP, s.Iface = parsePhysicalDefaultRoute(netstatOut)
	if s.GatewayIP != "" {
		s.GatewayMAC = parseGatewayMAC(run(ctx, "arp", "-n", s.GatewayIP))
	}

	// SSID is best-effort: empty on wired links and on modern macOS without
	// Location permission. Gateway MAC remains the reliable identity key.
	if s.Iface != "" {
		s.SSID = parseSSID(run(ctx, "networksetup", "-getairportnetwork", s.Iface))
	}

	// "Link up" means a usable physical path exists (carrier + gateway), whether
	// wired or Wi-Fi. Used by the classifier to exclude wifi-off/no-link gaps.
	s.LinkUp = s.GatewayIP != ""

	// VPN judged from the IPv4 default route only (reuses netstatOut), so macOS's
	// idle IPv6 utun interfaces never trigger a false positive.
	s.VPN = vpnActiveInet(netstatOut)
	return s
}

// RecentPowerEvents returns sleep/wake transitions from the system power log.
// Pass a since filter like "0" for the full buffer; callers dedupe by timestamp.
func RecentPowerEvents(ctx context.Context) []pmEvent {
	out := run(ctx, "pmset", "-g", "log")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return parsePmsetLog(out)
}
