// Package throughput runs idle-aware speed tests so the measured Mbit can be
// compared against the provider's advertised target. It refuses to run while the
// link is already busy with user traffic, so a test never fights real usage or
// self-induces a drop on a constrained shared line.
//
// Tests use several parallel TCP streams and time only the body-transfer window
// (after connection setup), so a single stream's congestion-window limit on a
// high-latency or lossy path cannot make a fast line look slow. The payload is
// sized to the advertised speed so every transfer runs for roughly the same
// wall-clock duration regardless of plan, keeping TCP slow-start a small,
// constant fraction of the measurement. A transfer is cut off after that
// duration and rated on what got through, so a line far below its plan is not
// saturated for minutes.
package throughput

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Result is one measurement.
type Result struct {
	DownMbit float64
	UpMbit   float64
	Bytes    int64 // payload actually transferred, both directions
}

// parseInterfaceBytes extracts (inBytes, outBytes) from `netstat -ib -I <iface>`
// using the hardware "<Link#>" row, whose Ibytes/Obytes are columns 6 and 9.
func parseInterfaceBytes(out string) (inB, outB uint64, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "<Link") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		ib, err1 := strconv.ParseUint(f[6], 10, 64)
		ob, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return ib, ob, true
	}
	return 0, 0, false
}

// mbit converts a byte delta over a duration into megabits per second.
func mbit(deltaBytes uint64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(deltaBytes) * 8 / d.Seconds() / 1e6
}

func ifaceBytes(ctx context.Context, iface string) (uint64, uint64, bool) {
	out, err := exec.CommandContext(ctx, "netstat", "-ib", "-I", iface).Output()
	if err != nil {
		return 0, 0, false
	}
	return parseInterfaceBytes(string(out))
}

// LinkBusyMbit samples the interface twice over window and returns the observed
// combined throughput in Mbit (max of in/out). Callers compare against an idle
// threshold to decide whether to skip a speed test.
func LinkBusyMbit(ctx context.Context, iface string, window time.Duration) float64 {
	in0, out0, ok := ifaceBytes(ctx, iface)
	if !ok {
		return 0
	}
	select {
	case <-ctx.Done():
		return 0
	case <-time.After(window):
	}
	in1, out1, ok := ifaceBytes(ctx, iface)
	if !ok {
		return 0
	}
	down := mbit(saturatingSub(in1, in0), window)
	up := mbit(saturatingSub(out1, out0), window)
	if up > down {
		return up
	}
	return down
}

func saturatingSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// Cloudflare endpoints require no API key for modest volumes.
const (
	downURL = "https://speed.cloudflare.com/__down?bytes="
	upURL   = "https://speed.cloudflare.com/__up"

	// measureSecs is the target wall-clock duration of a transfer, and its hard
	// cap. Sizing the payload to the advertised speed keeps slow-start a small
	// fraction of the run at any plan, so the measured rate reflects steady-state
	// throughput.
	measureSecs = 10

	defaultStreams = 4

	// cloudflareMaxBytes is the largest payload Cloudflare's __down endpoint
	// serves in a single request; a larger ?bytes= returns 403 with a 1-byte
	// body. Each stream's share must stay at or under it, so we add streams when
	// the per-stream payload would otherwise exceed it.
	cloudflareMaxBytes = 9_000_000

	minDownBytes = 3_000_000
	maxDownBytes = 100_000_000
	minUpBytes   = 1_000_000
	maxUpBytes   = 25_000_000

	// Fallbacks when the advertised target is unknown.
	fallbackDownBytes = 25_000_000
	fallbackUpBytes   = 5_000_000
)

var errDownloadFailed = errors.New("throughput: all download streams failed")

// BytesForMbit returns a download payload sized so a line advertised at mbit
// takes ~measureSecs to transfer, clamped to a sane range. A non-positive mbit
// (target unknown) yields a fixed fallback.
func BytesForMbit(mbit float64) int {
	if mbit <= 0 {
		return fallbackDownBytes
	}
	return clampBytes(int(mbit*1e6/8*measureSecs), minDownBytes, maxDownBytes)
}

// BytesForMbitUp is BytesForMbit for the upload direction, with a lower ceiling
// since upstream is usually smaller and to keep test cost modest.
func BytesForMbitUp(mbit float64) int {
	if mbit <= 0 {
		return fallbackUpBytes
	}
	return clampBytes(int(mbit*1e6/8*measureSecs), minUpBytes, maxUpBytes)
}

func clampBytes(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// streamsFor returns a stream count of at least want such that each stream's
// share of total stays at or under cloudflareMaxBytes, so no single request trips
// the endpoint's per-request size limit.
func streamsFor(total, want int) int {
	if want < 1 {
		want = 1
	}
	need := (total + cloudflareMaxBytes - 1) / cloudflareMaxBytes
	if need > want {
		return need
	}
	return want
}

// splitBytes divides total across n streams as evenly as possible, handing the
// remainder to the first streams. Zero-sized parts are still returned so callers
// can skip them.
func splitBytes(total, n int) []int {
	if n < 1 {
		n = 1
	}
	parts := make([]int, n)
	base := total / n
	rem := total % n
	for i := range parts {
		parts[i] = base
		if i < rem {
			parts[i]++
		}
	}
	return parts
}

// Measurer runs the actual speed test.
type Measurer struct {
	Client    *http.Client
	DownBytes int
	UpBytes   int
	Streams   int
	// Window caps each direction's transfer time (0 = measureSecs).
	Window time.Duration

	// DownURL/UpURL override the Cloudflare endpoints (used by tests). DownURL
	// must end with the "bytes=" query so a size can be appended.
	DownURL string
	UpURL   string
}

// Measure downloads then uploads the configured payload across parallel streams
// and returns the rates. Each direction stops after Window and is rated on the
// bytes that got through. It errors only when the download fails entirely; an
// upload failure still yields the download result.
func (m Measurer) Measure(ctx context.Context) (Result, error) {
	c := m.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	downBytes := m.DownBytes
	if downBytes == 0 {
		downBytes = fallbackDownBytes
	}
	upBytes := m.UpBytes
	if upBytes == 0 {
		upBytes = fallbackUpBytes
	}
	streams := m.Streams
	if streams <= 0 {
		streams = defaultStreams
	}
	dURL := m.DownURL
	if dURL == "" {
		dURL = downURL
	}
	uURL := m.UpURL
	if uURL == "" {
		uURL = upURL
	}

	window := m.Window
	if window <= 0 {
		window = measureSecs * time.Second
	}

	var res Result
	dctx, cancel := context.WithTimeout(ctx, window)
	down, dn, ok := download(dctx, c, dURL, downBytes, streams)
	cancel()
	if !ok {
		return res, errDownloadFailed
	}
	up, un := upload(ctx, c, uURL, upBytes, streams, window)
	res.DownMbit, res.UpMbit, res.Bytes = down, up, int64(dn+un)
	return res, nil
}

// download runs `streams` parallel GETs totalling `total` bytes. The clock starts
// before any request is issued and each stream is issued and read inside the
// timed window, so the elapsed time always corresponds to the bytes counted.
// (Reading bodies only after issuing every request lets the transport buffer an
// untimed, variable chunk of each payload, decoupling bytes from time and
// collapsing the rate on a high-latency link.) The payload is sized to ~10s of
// transfer, so the handshake/TTFB now inside the window is negligible. The
// combined streams still fill a high-latency pipe a single connection cannot. A
// ctx that expires mid-transfer still yields the rate of the bytes read so far.
// ok is false when no stream established a body (e.g. a real outage).
func download(ctx context.Context, c *http.Client, url string, total, streams int) (rate float64, n uint64, ok bool) {
	var sum atomic.Uint64
	var got atomic.Bool
	var wg sync.WaitGroup
	start := time.Now()
	for _, p := range splitBytes(total, streamsFor(total, streams)) {
		if p <= 0 {
			continue
		}
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+strconv.Itoa(n), nil)
			resp, err := c.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			// A non-200 (e.g. an over-limit ?bytes= 403) carries no payload; count
			// it as a failed stream rather than a silent zero-byte success.
			if resp.StatusCode != http.StatusOK {
				return
			}
			got.Store(true)
			m, _ := io.Copy(io.Discard, resp.Body)
			sum.Add(uint64(m))
		}(p)
	}
	wg.Wait()
	if !got.Load() {
		return 0, 0, false
	}
	return mbit(sum.Load(), time.Since(start)), sum.Load(), true
}

// countingReader counts the bytes the HTTP transport takes from an upload body.
type countingReader struct {
	r io.Reader
	n *atomic.Uint64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(uint64(n))
	return n, err
}

// upload runs `streams` parallel POSTs totalling `total` random bytes for at
// most window and returns the aggregate rate and the bytes sent. Best-effort:
// failed streams simply contribute no bytes. The handshake cannot be excluded
// here (the body must be sent as the request runs), but multi-stream still
// avoids a single connection's window limit.
//
// When every stream finishes in time the rate is exact: completed bytes over
// elapsed time. When the window cuts the upload off, no server has confirmed
// anything, so the rate is what the transport took from the bodies after a
// warm-up. The warm-up excludes the initial burst that only fills the socket
// buffers; after it the transport takes bytes only as fast as the line drains.
// shortcut: reads too low if the whole payload fits in the socket buffers
// (tiny plan, line far below it), count acked bytes via TCP info if that matters.
func upload(ctx context.Context, c *http.Client, url string, total, streams int, window time.Duration) (float64, uint64) {
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	var done, sent, warm atomic.Uint64
	warmup := window / 5
	t := time.AfterFunc(warmup, func() { warm.Store(sent.Load()) })
	defer t.Stop()
	var wg sync.WaitGroup
	start := time.Now()
	for _, p := range splitBytes(total, streamsFor(total, streams)) {
		if p <= 0 {
			continue
		}
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := countingReader{io.LimitReader(rand.Reader, int64(n)), &sent}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
			req.ContentLength = int64(n)
			resp, err := c.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			done.Add(uint64(n))
		}(p)
	}
	wg.Wait()
	if ctx.Err() == nil {
		return mbit(done.Load(), time.Since(start)), done.Load()
	}
	return mbit(saturatingSub(sent.Load(), warm.Load()), window-warmup), sent.Load()
}
