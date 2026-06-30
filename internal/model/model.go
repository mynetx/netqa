// Package model holds the shared domain types passed between collectors,
// the store, and the web/report layers. Keeping them here avoids an import
// cycle between the store and the packages that produce data for it.
package model

import "time"

// Provider is an ISP plan the user is measuring. TargetDownMbit/TargetUpMbit are
// the advertised speeds and are editable over time (e.g. 40 -> 20 next month).
// MatchMACs/MatchASN are optional auto-assign rules: a comma-separated list of
// gateway MACs or OUI prefixes (e.g. "a1:b2:c3"), and of case-insensitive ASN-org
// substrings (e.g. "ExampleISP,OtherISP"). They let a new network be linked to this
// provider automatically. MAC rules win over ASN rules so same-operator links
// (e.g. a fiber and an LTE link that share one ASN) are never confused.
type Provider struct {
	ID             int64
	Name           string
	TargetDownMbit float64
	TargetUpMbit   float64
	Notes          string
	MatchMACs      string
	MatchASN       string
}

// Network is a concrete network the machine has joined, fingerprinted by its
// local LAN identity (SSID + gateway MAC) so a VPN cannot change which network
// we think we are on. ISPASN is enrichment only, filled when no VPN is active.
type Network struct {
	ID         int64
	ProviderID *int64 // nil until the user assigns this network to a provider
	SSID       string
	GatewayMAC string
	ISPASN     string
	Label      string
}

// Fingerprint is the VPN-proof key used to match a Network.
func (n Network) Fingerprint() (ssid, gatewayMAC string) {
	return n.SSID, n.GatewayMAC
}

// Sample is one probe result against one target.
type Sample struct {
	ID        int64
	NetworkID int64
	TS        time.Time
	Target    string
	Success   bool
	RTTms     float64 // valid only when Success
	VPN       bool
}

// OutageClass classifies a connectivity gap. Only ISP and Upstream count against
// the provider; Local (wifi off / gateway down / asleep) never does.
type OutageClass string

const (
	OutageLocal    OutageClass = "local"    // self-inflicted: wifi off, gateway down, asleep
	OutageISP      OutageClass = "isp"      // gateway reachable, internet not
	OutageUpstream OutageClass = "upstream" // break beyond the ISP edge
)

// Outage is a confirmed connectivity gap with start/end. End is zero while open.
type Outage struct {
	ID        int64
	NetworkID int64
	Start     time.Time
	End       time.Time // zero => ongoing
	Class     OutageClass
	VPN       bool
}

// Ongoing reports whether the outage has not yet ended.
func (o Outage) Ongoing() bool { return o.End.IsZero() }

// Throughput is one measured speed test, idle-aware (only recorded when the link
// was not already busy with user traffic).
type Throughput struct {
	ID        int64
	NetworkID int64
	TS        time.Time
	DownMbit  float64
	UpMbit    float64
	VPN       bool
}

// DNSResult is one DNS resolution probe.
type DNSResult struct {
	ID        int64
	NetworkID int64
	TS        time.Time
	Server    string
	Host      string
	Success   bool
	LatencyMs float64
	VPN       bool
}

// Traceroute is the raw + parsed path captured when an outage is confirmed.
type Traceroute struct {
	ID        int64
	OutageID  int64
	NetworkID int64
	TS        time.Time
	Target    string
	Raw       string
	Hops      []TracerouteHop
}

// TracerouteHop is one hop in a traceroute.
type TracerouteHop struct {
	Number int
	Host   string
	RTTms  float64
}

// PowerEvent records a sleep or wake transition so asleep windows can be
// excluded from outage accounting.
type PowerEvent struct {
	ID   int64
	TS   time.Time
	Kind PowerKind
}

// PowerKind is the type of a PowerEvent.
type PowerKind string

const (
	PowerSleep PowerKind = "sleep"
	PowerWake  PowerKind = "wake"
)
