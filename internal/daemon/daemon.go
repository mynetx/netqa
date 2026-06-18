// Package daemon is the always-on coordinator. Each tick it observes the
// environment, resolves the current network, probes targets, classifies the
// connectivity state, persists samples/outages, and publishes a live status for
// the web dashboard.
package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mynetx/netqa/internal/config"
	"github.com/mynetx/netqa/internal/env"
	"github.com/mynetx/netqa/internal/model"
	"github.com/mynetx/netqa/internal/netid"
	"github.com/mynetx/netqa/internal/outage"
	"github.com/mynetx/netqa/internal/prober"
	"github.com/mynetx/netqa/internal/store"
	"github.com/mynetx/netqa/internal/throughput"
	"github.com/mynetx/netqa/internal/trace"
)

// Status is the live snapshot the dashboard renders.
type Status struct {
	TS          time.Time         `json:"ts"`
	NetworkID   int64             `json:"network_id"`
	SSID        string            `json:"ssid"`
	Iface       string            `json:"iface"`
	GatewayIP   string            `json:"gateway_ip"`
	VPN         bool              `json:"vpn"`
	Online      bool              `json:"online"`
	GatewayUp   bool              `json:"gateway_up"`
	LossPct     float64           `json:"loss_pct"`
	AvgRTTms    float64           `json:"avg_rtt_ms"`
	JitterMs    float64           `json:"jitter_ms"`
	OutageOpen  bool              `json:"outage_open"`
	OutageClass model.OutageClass `json:"outage_class,omitempty"`
	OutageSince time.Time         `json:"outage_since,omitempty"`
}

// Daemon coordinates collectors and persistence.
type Daemon struct {
	cfg      config.Config
	store    *store.Store
	resolver *netid.Resolver
	icmp     prober.ICMPPinger
	tcp      prober.TCPPinger
	window   *prober.Window
	measurer throughput.Measurer

	// Optional hooks; nil-safe. Called outside the status lock.
	OnOutageOpen  func(model.Outage)
	OnOutageClose func(model.Outage)

	// measuring is set while a throughput test saturates the link, so the prober
	// does not misread the deliberate saturation as an ISP outage.
	measuring atomic.Bool

	mu        sync.RWMutex
	status    Status
	curIfce   string    // physical interface of the current network
	lastTPRun time.Time // when the last throughput test was recorded
}

var (
	errNoNetwork = errors.New("no network identified yet")
	errLinkBusy  = errors.New("link busy")
)

// New builds a Daemon. The resolver's Fetcher should be a real HTTPASNFetcher in
// production; tests may inject a fake.
func New(cfg config.Config, s *store.Store, resolver *netid.Resolver) *Daemon {
	return &Daemon{
		cfg:      cfg,
		store:    s,
		resolver: resolver,
		icmp:     prober.ICMPPinger{Timeout: 2 * time.Second},
		tcp:      prober.TCPPinger{Port: "443", Timeout: 2 * time.Second},
		window:   prober.NewWindow(cfg.WindowSize),
	}
}

// Status returns the latest live status.
func (d *Daemon) Status() Status {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.status
}

// Run loops until ctx is cancelled, ticking at the configured probe interval.
// A separate goroutine runs idle-aware throughput tests.
func (d *Daemon) Run(ctx context.Context) error {
	go d.throughputLoop(ctx)
	d.tick(ctx) // immediate first sample
	t := time.NewTicker(d.cfg.ProbeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			d.tick(ctx)
		}
	}
}

// throughputLoop periodically runs a speed test, but only when the link is idle
// (so it never fights user traffic on the shared line) and a network is known.
// throughputLoop runs adaptive idle-triggered speed tests: it evaluates the link
// every ThroughputCheckInterval and runs a test whenever the link is using less
// than ThroughputIdleMbit, but never more often than ThroughputMinGap apart.
func (d *Daemon) throughputLoop(ctx context.Context) {
	// First evaluation soon after start so the chart fills quickly.
	first := time.NewTimer(20 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
		d.maybeMeasure(ctx)
	}
	t := time.NewTicker(d.cfg.ThroughputCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.maybeMeasure(ctx)
		}
	}
}

// maybeMeasure runs an idle-gated test if at least ThroughputMinGap has passed
// since the last recorded one.
func (d *Daemon) maybeMeasure(ctx context.Context) {
	d.mu.RLock()
	last := d.lastTPRun
	d.mu.RUnlock()
	if !last.IsZero() && time.Since(last) < d.cfg.ThroughputMinGap {
		return
	}
	d.measureThroughput(ctx, false)
}

// RunSpeedtestNow forces an immediate test regardless of the idle gate (used by
// the dashboard's manual trigger) and returns the measured rates.
func (d *Daemon) RunSpeedtestNow(ctx context.Context) (down, up float64, err error) {
	return d.measureThroughput(ctx, true)
}

// measureThroughput runs one speed test and stores it. When force is false it is
// idle-aware: it records nothing if no network is known or the link is already
// busy with user traffic, so an automatic test never fights real usage.
func (d *Daemon) measureThroughput(ctx context.Context, force bool) (float64, float64, error) {
	d.mu.RLock()
	iface, nid, vpn := d.curIfce, d.status.NetworkID, d.status.VPN
	d.mu.RUnlock()
	if nid == 0 {
		return 0, 0, errNoNetwork
	}
	if !force {
		if busy := throughput.LinkBusyMbit(ctx, iface, 800*time.Millisecond); busy > d.cfg.ThroughputIdleMbit {
			return 0, 0, errLinkBusy
		}
	}
	// Size the payload to the advertised plan so every transfer runs for roughly
	// the same duration regardless of speed (target unknown -> default payload).
	down, up, _ := d.store.TargetForNetwork(nid)
	m := d.measurer
	m.DownBytes = throughput.BytesForMbit(down)
	m.UpBytes = throughput.BytesForMbitUp(up)

	// Suppress outage detection while the test saturates the link.
	d.measuring.Store(true)
	res, err := m.Measure(ctx)
	d.measuring.Store(false)
	if err != nil {
		return 0, 0, err
	}
	if err := d.store.InsertThroughput(model.Throughput{
		NetworkID: nid, TS: time.Now(), DownMbit: res.DownMbit, UpMbit: res.UpMbit, VPN: vpn,
	}); err != nil {
		return 0, 0, err
	}
	d.mu.Lock()
	d.lastTPRun = time.Now()
	d.mu.Unlock()
	return res.DownMbit, res.UpMbit, nil
}

// reachable returns true if the host answers ICMP or a TCP:443 connect.
func (d *Daemon) reachable(ctx context.Context, host string) (bool, time.Duration) {
	if r := d.icmp.Ping(ctx, host); r.Success {
		return true, r.RTT
	}
	if r := d.tcp.Ping(ctx, host); r.Success {
		return true, r.RTT
	}
	return false, 0
}

func (d *Daemon) tick(ctx context.Context) {
	// A throughput test deliberately saturates the link; probing during it would
	// register false packet loss and a false ISP outage. Skip the tick.
	if d.measuring.Load() {
		return
	}
	tickCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	now := time.Now()

	snap := env.Observe(tickCtx)
	nid, err := d.resolver.Resolve(tickCtx, snap)
	if err != nil {
		return // transient store/error; skip this tick rather than crash
	}

	gatewayUp := false
	if snap.GatewayIP != "" {
		gatewayUp, _ = d.reachable(tickCtx, snap.GatewayIP)
	}

	internetUp := false
	for _, target := range d.cfg.Targets {
		ok, rtt := d.reachable(tickCtx, target)
		_ = d.store.InsertSample(model.Sample{
			NetworkID: nid, TS: now, Target: target, Success: ok, RTTms: msOf(rtt), VPN: snap.VPN,
		})
		d.window.Add(prober.Result{TS: now, Target: target, Success: ok, RTT: rtt})
		if ok {
			internetUp = true
		}
	}

	isOut, class := outage.Classify(outage.State{
		Awake: true, WifiOn: snap.LinkUp, GatewayUp: gatewayUp, InternetUp: internetUp, VPN: snap.VPN,
	})

	d.manageOutage(nid, now, snap.VPN, isOut, class)

	st := Status{
		TS: now, NetworkID: nid, SSID: snap.SSID, Iface: snap.Iface, GatewayIP: snap.GatewayIP, VPN: snap.VPN,
		Online: internetUp, GatewayUp: gatewayUp,
		LossPct: d.window.LossPct(), AvgRTTms: d.window.AvgRTTms(), JitterMs: d.window.JitterMs(),
	}
	if isOut && outage.CountsAgainstISP(class) {
		st.OutageOpen = true
		st.OutageClass = class
	}
	d.mu.Lock()
	if st.OutageOpen {
		st.OutageSince = d.status.OutageSince
		if st.OutageSince.IsZero() {
			st.OutageSince = now
		}
	}
	d.status = st
	d.curIfce = snap.Iface
	d.mu.Unlock()
}

// manageOutage opens/closes the persisted outage for the network. Only ISP and
// upstream outages are recorded as evidence; local/asleep gaps are ignored.
func (d *Daemon) manageOutage(nid int64, now time.Time, vpn, isOut bool, class model.OutageClass) {
	ongoing, err := d.store.OngoingOutage(nid)
	if err != nil {
		return
	}
	evidence := isOut && outage.CountsAgainstISP(class)

	switch {
	case evidence && ongoing == nil:
		o := model.Outage{NetworkID: nid, Start: now, Class: class, VPN: vpn}
		if id, err := d.store.OpenOutage(o); err == nil {
			o.ID = id
			d.captureTraceroute(nid, id)
			if d.OnOutageOpen != nil {
				d.OnOutageOpen(o)
			}
		}
	case !evidence && ongoing != nil:
		if err := d.store.CloseOutage(ongoing.ID, now); err == nil {
			ongoing.End = now
			if d.OnOutageClose != nil {
				d.OnOutageClose(*ongoing)
			}
		}
	}
}

// captureTraceroute runs a traceroute in the background when an ISP outage opens
// and stores it against the outage, so the evidence shows where the path breaks.
// It runs detached (traceroute takes seconds) and is best-effort: a failure or
// empty result is silently dropped rather than blocking outage handling.
func (d *Daemon) captureTraceroute(nid, outageID int64) {
	if len(d.cfg.Targets) == 0 {
		return
	}
	target := d.cfg.Targets[0]
	go func() {
		raw, _, err := trace.Run(context.Background(), target)
		if raw == "" || err != nil {
			return
		}
		_ = d.store.InsertTraceroute(model.Traceroute{
			OutageID: outageID, NetworkID: nid, TS: time.Now(), Target: target, Raw: raw,
		})
	}()
}

func msOf(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
