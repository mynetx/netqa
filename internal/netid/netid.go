// Package netid maps the current environment to a stored Network identity using
// a VPN-proof key: (SSID, gateway MAC). ASN/ISP-name enrichment is fetched only
// when no VPN is active, so a VPN exit in another country never poisons which
// network (and therefore which provider) the data is attributed to.
package netid

import (
	"context"
	"io"
	"log"
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
}

// shouldLookupASN reports whether an ASN lookup is worthwhile and trustworthy:
// only when no VPN is active and we do not already know the ASN.
func shouldLookupASN(vpn bool, existingASN string) bool {
	return !vpn && existingASN == ""
}

// Resolve returns the network id for the snapshot, creating the row if new and
// enriching ASN when appropriate.
func (r *Resolver) Resolve(ctx context.Context, snap env.Snapshot) (int64, error) {
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
	if r.Fetcher != nil && shouldLookupASN(snap.VPN, existingASN) {
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
