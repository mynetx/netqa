package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mynetx/netqa/internal/daemon"
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
