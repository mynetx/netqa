// Command netqa logs connectivity quality and serves a local dashboard.
//
// Usage:
//
//	netqa serve            run the collector + dashboard (default)
//	netqa providers        list configured providers
//	netqa version          print version
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mynetx/netqa/internal/config"
	"github.com/mynetx/netqa/internal/daemon"
	"github.com/mynetx/netqa/internal/env"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/netid"
	"github.com/mynetx/netqa/internal/notify"
	"github.com/mynetx/netqa/internal/store"
	"github.com/mynetx/netqa/internal/throughput"
	"github.com/mynetx/netqa/internal/web"
)

const version = "0.1.0"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		mustServe()
	case "providers":
		mustListProviders()
	case "speedtest":
		mustSpeedtest()
	case "version", "-v", "--version":
		fmt.Println("netqa", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\nusage: netqa [serve|providers|version]\n", cmd)
		os.Exit(2)
	}
}

func setup() (config.Config, *store.Store) {
	dir, err := config.DataDir()
	if err != nil {
		fatal("data dir: %v", err)
	}
	cfg, err := config.Load(config.ConfigPath(dir))
	if err != nil {
		fatal("config: %v", err)
	}
	s, err := store.Open(config.DBPath(dir))
	if err != nil {
		fatal("store: %v", err)
	}
	return cfg, s
}

func mustServe() {
	cfg, s := setup()
	defer s.Close()

	resolver := &netid.Resolver{Store: s, Fetcher: netid.HTTPASNFetcher{}}
	d := daemon.New(cfg, s, resolver)

	if cfg.Alerts {
		d.OnOutageOpen = func(o model.Outage) {
			_ = notify.Notify("netqa: internet outage", fmt.Sprintf("Confirmed %s outage started.", o.Class))
		}
		d.OnOutageClose = func(o model.Outage) {
			dur := o.End.Sub(o.Start).Round(time.Second)
			_ = notify.Notify("netqa: internet recovered", fmt.Sprintf("Outage lasted %s.", dur))
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := d.Run(ctx); err != nil && ctx.Err() == nil {
			fatal("daemon: %v", err)
		}
	}()

	srv := web.New(s, d.Status, d.RunSpeedtestNow)

	// Bind the configured port, falling back to the next free one so a port
	// already taken (e.g. by another app) never crash-loops us silently.
	ln, addr := listenWithFallback(cfg.Port)
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// WriteTimeout stays 0: handleStream serves an unbounded SSE response, so a
		// write deadline would kill the live dashboard feed.
		IdleTimeout: 60 * time.Second,
	}

	// Self-watchdog: KeepAlive only respawns us on exit, so an alive-but-wedged
	// daemon (handlers stuck, connections piled up) is never restarted. Probe our
	// own lock-free /healthz and exit on sustained failure so launchd respawns a
	// clean process.
	go watchdog(ctx, addr)

	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sh)
	}()

	fmt.Printf("netqa %s — dashboard http://%s  (data in config dir)\n", version, addr)
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fatal("http: %v", err)
	}
	fmt.Println("netqa stopped.")
}

// watchdog periodically probes the local /healthz endpoint and forces a process
// exit after several consecutive failures, so a wedged-but-alive daemon gets
// restarted by launchd (KeepAlive only fires on exit, never on a hang). It stops
// quietly when ctx is cancelled (normal shutdown), so a clean SIGTERM does not
// trip a spurious exit code.
func watchdog(ctx context.Context, addr string) {
	const (
		interval    = 30 * time.Second
		probeTO     = 3 * time.Second
		maxFailures = 3 // ~90s wedged before we bail
	)
	url := "http://" + addr + "/healthz"
	client := &http.Client{Timeout: probeTO}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if healthOK(ctx, client, url) {
				fails = 0
				continue
			}
			fails++
			fmt.Fprintf(os.Stderr, "watchdog: health probe failed (%d/%d)\n", fails, maxFailures)
			if fails >= maxFailures {
				fmt.Fprintf(os.Stderr, "watchdog: daemon wedged, exiting for launchd restart\n")
				os.Exit(1)
			}
		}
	}
}

// healthOK reports whether a single /healthz probe returned 200.
func healthOK(ctx context.Context, c *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func mustListProviders() {
	_, s := setup()
	defer s.Close()
	ps, err := s.Providers()
	if err != nil {
		fatal("providers: %v", err)
	}
	if len(ps) == 0 {
		fmt.Println("no providers yet — add one in the dashboard.")
		return
	}
	for _, p := range ps {
		fmt.Printf("#%d  %-20s  down=%.0f up=%.0f Mbit  %s\n", p.ID, p.Name, p.TargetDownMbit, p.TargetUpMbit, p.Notes)
	}
}

// listenWithFallback binds 127.0.0.1:port, trying the next ports if it is taken,
// so a busy port degrades gracefully instead of crash-looping under launchd.
func listenWithFallback(port int) (net.Listener, string) {
	for p := port; p < port+20; p++ {
		addr := fmt.Sprintf("127.0.0.1:%d", p)
		if ln, err := net.Listen("tcp", addr); err == nil {
			return ln, addr
		}
	}
	fatal("no free port in range %d-%d", port, port+19)
	return nil, ""
}

// mustSpeedtest runs one throughput measurement immediately (ignoring the idle
// gate), prints it, and records it for the current network. Useful on demand and
// for verifying the throughput pipeline.
func mustSpeedtest() {
	_, s := setup()
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	snap := env.Observe(ctx)
	resolver := &netid.Resolver{Store: s, Fetcher: netid.HTTPASNFetcher{}}
	nid, err := resolver.Resolve(ctx, snap)
	if err != nil {
		fatal("resolve network: %v", err)
	}

	busy := throughput.LinkBusyMbit(ctx, snap.Iface, 800*time.Millisecond)
	fmt.Printf("link busy: %.2f Mbit on %s — running speed test…\n", busy, snap.Iface)

	res, err := (throughput.Measurer{}).Measure(ctx)
	if err != nil {
		fatal("measure: %v", err)
	}
	if err := s.InsertThroughput(model.Throughput{
		NetworkID: nid, TS: time.Now(), DownMbit: res.DownMbit, UpMbit: res.UpMbit, VPN: snap.VPN,
	}); err != nil {
		fatal("store: %v", err)
	}
	fmt.Printf("down %.1f Mbit  up %.1f Mbit  (vpn=%v) — recorded for network #%d\n",
		res.DownMbit, res.UpMbit, snap.VPN, nid)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "netqa: "+format+"\n", a...)
	os.Exit(1)
}
