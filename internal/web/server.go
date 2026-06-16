// Package web serves the local dashboard: live status (SSE), historical charts,
// provider/target editing, and a single-provider presentation mode so the view
// and exports can be locked to one ISP without leaking other providers' data.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/mynetx/netqa/internal/daemon"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/store"
)

//go:embed static/*
var staticFS embed.FS

// StatusFunc returns the current live daemon status.
type StatusFunc func() daemon.Status

// SpeedtestFunc forces an immediate throughput test and returns the rates.
type SpeedtestFunc func(ctx context.Context) (down, up float64, err error)

// Server is the dashboard HTTP server.
type Server struct {
	store     *store.Store
	status    StatusFunc
	speedtest SpeedtestFunc
	mux       *http.ServeMux
}

// New builds the dashboard server. speedtest may be nil (manual test disabled).
func New(s *store.Store, status StatusFunc, speedtest SpeedtestFunc) *Server {
	srv := &Server{store: s, status: status, speedtest: speedtest, mux: http.NewServeMux()}
	srv.routes()
	return srv
}

// Handler exposes the mux (useful for tests).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	sub, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
	// Avoid a noisy 404 for the browser's automatic favicon request.
	s.mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/stream", s.handleStream)
	s.mux.HandleFunc("/api/providers", s.handleProviders)
	s.mux.HandleFunc("/api/networks", s.handleNetworks)
	s.mux.HandleFunc("/api/history", s.handleHistory)
	s.mux.HandleFunc("/api/speedtest", s.handleSpeedtest)
}

// handleSpeedtest forces an immediate throughput test (POST) and returns the
// measured rates. The dashboard calls this when the throughput card is clicked.
func (s *Server) handleSpeedtest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.speedtest == nil {
		http.Error(w, "speedtest unavailable", 503)
		return
	}
	// Generous timeout: a slow link still needs to finish the transfer.
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	down, up, err := s.speedtest(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]float64{"down_mbit": down, "up_mbit": up})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.status())
}

// handleStream pushes the live status as Server-Sent Events once per second.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			b, _ := json.Marshal(s.status())
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handleProviders: GET lists, POST upserts (edit name/targets, e.g. 40 -> 20).
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ps, err := s.store.Providers()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if ps == nil {
			ps = []model.Provider{}
		}
		writeJSON(w, ps)
	case http.MethodPost:
		var p model.Provider
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		id, err := s.store.UpsertProvider(p)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]int64{"id": id})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// networkPayload is the editable shape for assigning a network to a provider.
type networkPayload struct {
	SSID       string `json:"ssid"`
	GatewayMAC string `json:"gateway_mac"`
	ProviderID *int64 `json:"provider_id"`
	Label      string `json:"label"`
}

// handleNetworks: GET lists, POST assigns provider/label to a network.
func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ns, err := s.store.Networks()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if ns == nil {
			ns = []model.Network{}
		}
		writeJSON(w, ns)
	case http.MethodPost:
		var p networkPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		id, err := s.store.UpsertNetwork(model.Network{
			SSID: p.SSID, GatewayMAC: p.GatewayMAC, ProviderID: p.ProviderID, Label: p.Label,
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]int64{"id": id})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// historyResponse bundles the chart data for one network over a window.
type historyResponse struct {
	From       time.Time          `json:"from"`
	To         time.Time          `json:"to"`
	Points     []Point            `json:"points"`
	Outages    []model.Outage     `json:"outages"`
	Throughput []model.Throughput `json:"throughput"`
}

// handleHistory: GET /api/history?network=<id>&hours=<n>&buckets=<n>
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	nid, _ := strconv.ParseInt(r.URL.Query().Get("network"), 10, 64)
	hours := atoiDefault(r.URL.Query().Get("hours"), 24)
	buckets := atoiDefault(r.URL.Query().Get("buckets"), 120)

	to := time.Now()
	from := to.Add(-time.Duration(hours) * time.Hour)

	samples, err := s.store.SamplesBetween(nid, from, to)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	outs, _ := s.store.OutagesBetween(nid, from, to)
	tps, _ := s.store.ThroughputBetween(nid, from, to)

	writeJSON(w, historyResponse{
		From: from, To: to,
		Points:     bucketSamples(samples, from, to, buckets),
		Outages:    outs,
		Throughput: tps,
	})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
