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
// constant fraction of the measurement.
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

	// measureSecs is the target wall-clock duration of a transfer. Sizing the
	// payload to the advertised speed keeps slow-start a small fraction of the
	// run at any plan, so the measured rate reflects steady-state throughput.
	measureSecs = 10

	defaultStreams = 4

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

	// DownURL/UpURL override the Cloudflare endpoints (used by tests). DownURL
	// must end with the "bytes=" query so a size can be appended.
	DownURL string
	UpURL   string
}

// Measure downloads then uploads the configured payload across parallel streams
// and returns the rates. It errors only when the download fails entirely; an
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

	var res Result
	down, ok := download(ctx, c, dURL, downBytes, streams)
	if !ok {
		return res, errDownloadFailed
	}
	res.DownMbit = down
	res.UpMbit = upload(ctx, c, uURL, upBytes, streams)
	return res, nil
}

// download runs `streams` parallel GETs totalling `total` bytes. Every request is
// issued first so the TCP/TLS handshake and time-to-first-byte complete; only
// then does the clock start and all bodies stream concurrently. This keeps the
// timed window free of connection setup and lets the combined streams fill a
// high-latency pipe a single connection cannot. ok is false when no stream
// established a body (e.g. a real outage).
func download(ctx context.Context, c *http.Client, url string, total, streams int) (float64, bool) {
	bodies := make([]io.ReadCloser, 0, streams)
	for _, p := range splitBytes(total, streams) {
		if p <= 0 {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+strconv.Itoa(p), nil)
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		bodies = append(bodies, resp.Body)
	}
	if len(bodies) == 0 {
		return 0, false
	}

	var sum atomic.Uint64
	var wg sync.WaitGroup
	start := time.Now()
	for _, b := range bodies {
		wg.Add(1)
		go func(body io.ReadCloser) {
			defer wg.Done()
			n, _ := io.Copy(io.Discard, body)
			body.Close()
			sum.Add(uint64(n))
		}(b)
	}
	wg.Wait()
	return mbit(sum.Load(), time.Since(start)), true
}

// upload runs `streams` parallel POSTs totalling `total` random bytes and returns
// the aggregate rate. Best-effort: failed streams simply contribute no bytes. The
// handshake cannot be excluded here (the body must be sent as the request runs),
// but multi-stream still avoids a single connection's window limit.
func upload(ctx context.Context, c *http.Client, url string, total, streams int) float64 {
	var sum atomic.Uint64
	var wg sync.WaitGroup
	start := time.Now()
	for _, p := range splitBytes(total, streams) {
		if p <= 0 {
			continue
		}
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := io.LimitReader(rand.Reader, int64(n))
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
			req.ContentLength = int64(n)
			resp, err := c.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			sum.Add(uint64(n))
		}(p)
	}
	wg.Wait()
	return mbit(sum.Load(), time.Since(start))
}
