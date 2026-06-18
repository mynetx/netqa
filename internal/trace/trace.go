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

// Run executes a numeric, single-probe traceroute to target and returns the raw
// output plus the parsed hops. The raw text is the stored evidence; parsing is
// best-effort. A non-empty raw output is returned even when traceroute exits
// non-zero (it often does mid-outage), so the path captured up to the break is
// kept.
func Run(ctx context.Context, target string) (string, []model.TracerouteHop, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// -n: numeric (no DNS, faster). -w 1: 1s wait per probe. -q 1: one probe per
	// hop. -m 20: cap hops so a long dead tail doesn't stall the capture.
	out, err := exec.CommandContext(cctx, "traceroute", "-n", "-w", "1", "-q", "1", "-m", "20", target).CombinedOutput()
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
			// RTT is the token immediately before the "ms" unit.
			for i := 2; i < len(f); i++ {
				if f[i] == "ms" {
					if v, e := strconv.ParseFloat(f[i-1], 64); e == nil {
						h.RTTms = v
					}
					break
				}
			}
		}
		hops = append(hops, h)
	}
	return hops
}
