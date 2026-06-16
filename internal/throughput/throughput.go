// Package throughput runs idle-aware speed tests so the measured Mbit can be
// compared against the provider's advertised target. It refuses to run while the
// link is already busy with user traffic, so a test never fights real usage or
// self-induces a drop on a constrained shared line.
package throughput

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
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
)

// Measurer runs the actual speed test.
type Measurer struct {
	Client    *http.Client
	DownBytes int
	UpBytes   int
}

// Measure downloads then uploads a fixed payload and returns the rates.
func (m Measurer) Measure(ctx context.Context) (Result, error) {
	c := m.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	downBytes := m.DownBytes
	if downBytes == 0 {
		downBytes = 25_000_000 // ~25 MB: enough to gauge a 40 Mbit line, modest cost
	}
	upBytes := m.UpBytes
	if upBytes == 0 {
		upBytes = 5_000_000
	}

	var res Result

	// Download
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, downURL+strconv.Itoa(downBytes), nil)
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return res, err
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	res.DownMbit = mbit(uint64(n), time.Since(start))

	// Upload
	body := io.LimitReader(rand.Reader, int64(upBytes))
	ureq, _ := http.NewRequestWithContext(ctx, http.MethodPost, upURL, body)
	ureq.ContentLength = int64(upBytes)
	ustart := time.Now()
	uresp, err := c.Do(ureq)
	if err != nil {
		return res, nil // keep the download result even if upload fails
	}
	io.Copy(io.Discard, uresp.Body)
	uresp.Body.Close()
	res.UpMbit = mbit(uint64(upBytes), time.Since(ustart))

	return res, nil
}
