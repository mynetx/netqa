// Package trace captures a network path with the system traceroute when an
// outage is confirmed, so the recorded evidence shows where the break is — at
// the ISP edge or beyond it — rather than just that the internet was down.
package trace

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mynetx/netqa/internal/model"
)

// Run executes a numeric ICMP traceroute to target and returns the raw output
// plus the parsed hops. The raw text is the stored evidence; parsing is
// best-effort. A non-empty raw output is returned even when traceroute exits
// non-zero (it often does mid-outage), so the path captured up to the break is
// kept.
func Run(ctx context.Context, target string) (string, []model.TracerouteHop, error) {
	// Worst case -m 20 hops x -q 3 probes x -w 1s ~= 60s on a dead tail, so allow
	// 75s; the capture runs in a background goroutine, so a long wait is harmless.
	cctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	// -I: ICMP probes. QCell's network silently drops the default UDP probes
	// (every hop past the router shows "*"), but passes ICMP, which reveals the
	// full path including the QCell-domestic hops and the submarine-cable jump to
	// Europe — so the evidence shows where a break actually is. ICMP traceroute is
	// unprivileged on macOS, so no sudo is needed.
	// -n: numeric (no DNS, faster). -w 1: 1s wait per probe. -q 3: three probes per
	// hop so a transient queue spike is separable from the steady-state baseline.
	// -m 20: cap hops so a long dead tail doesn't stall the capture.
	out, err := exec.CommandContext(cctx, "traceroute", "-I", "-n", "-w", "1", "-q", "3", "-m", "20", target).CombinedOutput()
	raw := string(out)
	if raw == "" && err != nil {
		return "", nil, err
	}
	return raw, parse(raw), nil
}

// parse extracts hops from traceroute output. Each hop line starts with a hop
// number; an unreachable hop is "*". The header line ("traceroute to ...") and
// any blank lines are skipped.
func parse(raw string) []model.TracerouteHop {
	var hops []model.TracerouteHop
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		num, err := strconv.Atoi(f[0])
		if err != nil {
			continue // header or noise
		}
		h := model.TracerouteHop{Number: num, Host: f[1]}
		if f[1] != "*" {
			// With -q 3 each hop carries several "<rtt> ms" probes. Keep the
			// minimum: the least-queued probe is the truest path latency, so a
			// transient buffer spike (e.g. 772ms next to a 75ms baseline) does
			// not pollute the stored RTT. The raw text still holds all probes.
			found := false
			for i := 2; i < len(f); i++ {
				if f[i] == "ms" {
					if v, e := strconv.ParseFloat(f[i-1], 64); e == nil {
						if !found || v < h.RTTms {
							h.RTTms = v
							found = true
						}
					}
				}
			}
		}
		hops = append(hops, h)
	}
	return hops
}
