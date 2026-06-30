package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynetx/netqa/internal/daemon"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

// TestHealthz proves the liveness route answers 200 without touching the store
// or daemon state (nil store, trivial status func), so a wedged handler can't
// stall it.
func TestHealthz(t *testing.T) {
	s := New(nil, func() daemon.Status { return daemon.Status{} }, nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("/healthz body = %q want \"ok\"", rec.Body.String())
	}
}

// Saving a provider with a match rule must immediately backfill any already-known
// but unassigned network the rule fits, so the user sees the link without waiting
// for the next live resolve.
func TestPostProviderBackfillsNetworks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	// A network recorded before any provider/rule existed.
	if _, err := st.UpsertNetwork(model.Network{
		GatewayMAC: "a1:b2:c3:77:8a:3d", ISPASN: "AS65000 Example Telecom",
	}); err != nil {
		t.Fatalf("seed network: %v", err)
	}

	srv := New(st, func() daemon.Status { return daemon.Status{} }, nil)
	body := `{"Name":"Example ISP","MatchMACs":"a1:b2:c3"}`
	req := httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/providers = %d want 200 (%s)", rec.Code, rec.Body.String())
	}

	got, _ := st.NetworkByFingerprint("", "a1:b2:c3:77:8a:3d")
	if got == nil || got.ProviderID == nil {
		t.Fatalf("network not backfilled after provider save: %+v", got)
	}
}
