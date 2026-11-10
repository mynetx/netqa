package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mynetx/netqa/internal/daemon"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

// TestHealthz proves the liveness route answers 200 without touching the store
// or daemon state (nil store, trivial status func), so a wedged handler can't
// stall it.
func TestHealthz(t *testing.T) {
	s := New(nil, func() daemon.Status { return daemon.Status{} }, nil)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/healthz", nil)
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
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8799/api/providers", strings.NewReader(body))
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

// The unauthenticated dashboard must refuse foreign Host headers (DNS rebinding)
// and cross-site POSTs (CSRF), while serving local names and the Herd proxy.
func TestLocalOnlyGuards(t *testing.T) {
	srv := New(nil, func() daemon.Status { return daemon.Status{} }, nil)
	cases := []struct {
		name, method, url string
		hdr               map[string]string
		want              int
	}{
		{"loopback", "GET", "http://127.0.0.1:8799/healthz", nil, 200},
		{"ipv6 loopback", "GET", "http://[::1]:8799/healthz", nil, 200},
		{"herd proxy", "GET", "https://netqa.test/healthz", nil, 200},
		{"rebinding host", "GET", "http://evil.example/healthz", nil, 403},
		{"cross-site post", "POST", "http://localhost:8799/api/speedtest",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"same-origin post reaches handler", "POST", "http://localhost:8799/api/speedtest",
			map[string]string{"Sec-Fetch-Site": "same-origin"}, 503}, // speedtest nil -> 503
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.url, nil)
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %s %s = %d want %d", c.name, c.method, c.url, rec.Code, c.want)
		}
	}
}

// buckets=0 is how the menu bar plugin asks for speed tests only: it must return
// no points (and so skip the sample scan), while buckets=1 still aggregates.
func TestHistoryBucketsZeroSkipsPoints(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	nid, err := st.UpsertNetwork(model.Network{GatewayMAC: "a1:b2:c3:77:8a:3d"})
	if err != nil {
		t.Fatalf("seed network: %v", err)
	}
	if err := st.InsertSample(model.Sample{NetworkID: nid, TS: time.Now().Add(-time.Minute), Target: "192.0.2.1", Success: true, RTTms: 5}); err != nil {
		t.Fatalf("seed sample: %v", err)
	}
	srv := New(st, func() daemon.Status { return daemon.Status{} }, nil)

	for buckets, want := range map[string]int{"0": 0, "1": 1} {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/history?network=1&hours=720&buckets="+buckets, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		var got historyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("buckets=%s: status %d, bad JSON: %v", buckets, rec.Code, err)
		}
		if len(got.Points) != want {
			t.Errorf("buckets=%s: %d points, want %d", buckets, len(got.Points), want)
		}
	}
}
