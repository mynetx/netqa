// Package netid maps the current environment to a stored Network identity using
// a VPN-proof key: (SSID, gateway MAC), or (SSID, gateway IP + ASN) where the OS
// hides the MAC. ASN/ISP-name enrichment is fetched only
// when no VPN is active, so a VPN exit in another country never poisons which
// network (and therefore which provider) the data is attributed to.
package netid

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mynetx/netqa/internal/env"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

// ASNFetcher resolves the public ISP/ASN of the current real connection.
type ASNFetcher interface {
	ASN(ctx context.Context) (string, error)
}

// Resolver ensures a Network row exists for the current environment and
// opportunistically enriches it with ASN data when safe.
type Resolver struct {
	Store   *store.Store
	Fetcher ASNFetcher
	// OnProviderCreated, if set, is called when an unmatched ASN registers a new
	// provider (e.g. on arrival at a new ISP), so the user can set its target.
	OnProviderCreated func(model.Provider)
	// LinkASN looks up the ASN through the physical interface, bypassing any VPN.
	// It separates networks whose router MAC the OS hides (see hiddenKey).
	LinkASN func(ctx context.Context, iface string) (string, error)

	hidGW, hidKey, hidASN string
	hidAt                 time.Time
}

// hiddenKeyTTL is how long a hidden-MAC network key is trusted before the ASN is
// looked up again (one small HTTPS request).
const hiddenKeyTTL = 15 * time.Minute

// hiddenKey extends an "ip:<gateway>" key (router MAC hidden by the OS) with the
// link's AS number, because private gateway IPs repeat across networks: every
// FRITZ!Box is 192.168.178.1, at home and abroad. It also returns the ASN/org,
// which is trustworthy even under a VPN since the lookup is interface-bound.
func (r *Resolver) hiddenKey(ctx context.Context, snap env.Snapshot) (key, asn string) {
	now := time.Now()
	if snap.GatewayMAC == r.hidGW && now.Sub(r.hidAt) < hiddenKeyTTL {
		return r.hidKey, r.hidASN
	}
	if r.LinkASN != nil {
		if org, err := r.LinkASN(ctx, snap.Iface); err == nil && strings.HasPrefix(org, "AS") {
			r.hidGW, r.hidKey, r.hidASN, r.hidAt = snap.GatewayMAC, snap.GatewayMAC+" "+strings.Fields(org)[0], org, now
			return r.hidKey, r.hidASN
		}
	}
	// Lookup failed (offline, say): stay on the last key for this gateway, else
	// fall back to the bare IP key until a lookup succeeds.
	if snap.GatewayMAC == r.hidGW {
		return r.hidKey, r.hidASN
	}
	return snap.GatewayMAC, ""
}

// shouldLookupASN reports whether an ASN lookup is worthwhile and trustworthy:
// only when no VPN is active and we do not already know the ASN.
func shouldLookupASN(vpn bool, existingASN string) bool {
	return !vpn && existingASN == ""
}

// Resolve returns the network id for the snapshot, creating the row if new and
// enriching ASN when appropriate.
func (r *Resolver) Resolve(ctx context.Context, snap env.Snapshot) (int64, error) {
	linkASN := ""
	if strings.HasPrefix(snap.GatewayMAC, "ip:") {
		snap.GatewayMAC, linkASN = r.hiddenKey(ctx, snap)
	}
	existing, err := r.Store.NetworkByFingerprint(snap.SSID, snap.GatewayMAC)
	if err != nil {
		return 0, err
	}
	existingASN := ""
	alreadyAssigned := false
	if existing != nil {
		existingASN = existing.ISPASN
		alreadyAssigned = existing.ProviderID != nil
	}

	n := model.Network{SSID: snap.SSID, GatewayMAC: snap.GatewayMAC}

	asn := existingASN
	if asn == "" && linkASN != "" {
		n.ISPASN, asn = linkASN, linkASN
	}
	if r.Fetcher != nil && shouldLookupASN(snap.VPN, asn) {
		if fresh, err := r.Fetcher.ASN(ctx); err == nil && fresh != "" {
			n.ISPASN = fresh
			asn = fresh
		}
	}

	// Auto-assign a provider for a not-yet-assigned network from its match rules
	// (creating one for a new real ASN). An existing manual assignment is left
	// untouched. Best-effort: a failure here must not stop the tick from logging.
	if !alreadyAssigned {
		if ps, err := r.Store.Providers(); err != nil {
			log.Printf("netid: providers: %v", err)
		} else if pid, created, err := assignProvider(r.Store, &ps, snap.GatewayMAC, asn); err != nil {
			log.Printf("netid: auto-create provider for %q: %v", asn, err)
		} else if pid != 0 {
			n.ProviderID = &pid
			if created != nil && r.OnProviderCreated != nil {
				r.OnProviderCreated(*created)
			}
		}
	}
	return r.Store.UpsertNetwork(n)
}

// HTTPASNFetcher resolves ASN/org via ipinfo.io (no API key for low volume).
type HTTPASNFetcher struct {
	Client *http.Client
}

// ASN fetches the org string (e.g. "AS13335 Cloudflare, Inc.").
func (h HTTPASNFetcher) ASN(ctx context.Context) (string, error) {
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	return fetchOrg(ctx, c)
}

// LinkASN fetches the org string through iface itself, so a VPN exit can't
// answer instead of the real ISP. Usable as Resolver.LinkASN.
func LinkASN(ctx context.Context, iface string) (string, error) {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: boundControl(iface)}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp4", addr) // IP_BOUND_IF is the IPv4 option
	}}
	defer tr.CloseIdleConnections()
	return fetchOrg(ctx, &http.Client{Timeout: 5 * time.Second, Transport: tr})
}

func fetchOrg(ctx context.Context, c *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipinfo.io/org", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
