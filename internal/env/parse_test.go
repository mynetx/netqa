package env

import "testing"

func TestParseDefaultGatewayIP(t *testing.T) {
	// `route -n get default` output (trimmed).
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC>
`
	got := parseDefaultGatewayIP(out)
	if got != "192.168.1.1" {
		t.Fatalf("gateway = %q want 192.168.1.1", got)
	}
}

func TestParseGatewayMACFromARP(t *testing.T) {
	// `arp -n 192.168.1.1` output.
	out := `? (192.168.1.1) at a1:b2:c3:d4:e5:f6 on en0 ifscope [ethernet]`
	got := parseGatewayMAC(out)
	if got != "a1:b2:c3:d4:e5:f6" {
		t.Fatalf("mac = %q want a1:b2:c3:d4:e5:f6", got)
	}
}

func TestParseGatewayMACIncomplete(t *testing.T) {
	out := `? (192.168.1.1) at (incomplete) on en0 ifscope [ethernet]`
	if got := parseGatewayMAC(out); got != "" {
		t.Fatalf("expected empty MAC for incomplete arp, got %q", got)
	}
}

func TestParsePhysicalDefaultRoute(t *testing.T) {
	// `netstat -rn -f inet` while a VPN is up: the utun default must be ignored
	// and the underlying physical (en0) gateway returned — this is what keeps
	// network identity VPN-proof.
	out := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            link#38            UCSg          utun16
default            192.168.178.1      UGScIg          en0
127                127.0.0.1          UCS             lo0
`
	gw, iface := parsePhysicalDefaultRoute(out)
	if gw != "192.168.178.1" {
		t.Fatalf("gateway = %q want 192.168.178.1", gw)
	}
	if iface != "en0" {
		t.Fatalf("iface = %q want en0", iface)
	}
}

func TestParsePhysicalDefaultRouteNoVPN(t *testing.T) {
	out := `Routing tables
Internet:
Destination        Gateway            Flags        Netif Expire
default            10.0.0.1           UGScg          en0
`
	gw, iface := parsePhysicalDefaultRoute(out)
	if gw != "10.0.0.1" || iface != "en0" {
		t.Fatalf("got %q/%q want 10.0.0.1/en0", gw, iface)
	}
}

func TestParsePhysicalDefaultRouteOnlyTunnel(t *testing.T) {
	// All defaults are tunnels (e.g. full-tunnel VPN with no visible underlay):
	// return empty rather than mis-identify the tunnel as the LAN gateway.
	out := `default            link#38            UCSg          utun16`
	if gw, _ := parsePhysicalDefaultRoute(out); gw != "" {
		t.Fatalf("expected empty gateway, got %q", gw)
	}
}

func TestParseSSID(t *testing.T) {
	// `networksetup -getairportnetwork en0` output.
	out := "Current Wi-Fi Network: Home-5G\n"
	if got := parseSSID(out); got != "Home-5G" {
		t.Fatalf("ssid = %q want Home-5G", got)
	}
}

func TestParseSSIDWhenOff(t *testing.T) {
	out := "You are not associated with an AirPort network.\n"
	if got := parseSSID(out); got != "" {
		t.Fatalf("expected empty SSID when not associated, got %q", got)
	}
}

func TestVPNActiveInet(t *testing.T) {
	// VPN ON: the primary IPv4 default routes through a tunnel (utun16 listed
	// before the physical en0 default).
	vpnOn := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            link#38            UCScg          utun16
default            192.168.178.1      UGScIg          en0
`
	if !vpnActiveInet(vpnOn) {
		t.Fatal("expected VPN active when inet default routes via utun")
	}

	// VPN OFF: only the physical default exists. The many idle IPv6 utun
	// defaults macOS keeps are NOT in the inet table, so they can't false-positive.
	vpnOff := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            192.168.178.1      UGScg          en0
`
	if vpnActiveInet(vpnOff) {
		t.Fatal("must not report VPN when inet default is the physical link")
	}
}

func TestParsePmsetSleepWake(t *testing.T) {
	// `pmset -g log` lines for sleep/wake transitions.
	out := `2026-06-16 10:00:00 +0000 Sleep            	Entering Sleep state due to 'Clamshell Sleep'
2026-06-16 10:05:30 +0000 Wake             	Wake from Standby due to EC.LidOpen
2026-06-16 11:00:00 +0000 Notification     	Display is turned off
`
	events := parsePmsetLog(out)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d (%+v)", len(events), events)
	}
	if events[0].Kind != "sleep" || events[1].Kind != "wake" {
		t.Fatalf("unexpected kinds: %+v", events)
	}
	if events[0].TS.Hour() != 10 || events[0].TS.Minute() != 0 {
		t.Fatalf("sleep ts wrong: %v", events[0].TS)
	}
}
