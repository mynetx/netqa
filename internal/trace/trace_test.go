package trace

import "testing"

func TestParse(t *testing.T) {
	// -q 3 output: several "<rtt> ms" probes per hop. parse must keep the minimum.
	raw := `traceroute to 1.1.1.1 (1.1.1.1), 20 hops max, 52 byte packets
 1  192.168.178.1  2.345 ms  2.100 ms  2.500 ms
 2  10.0.0.1  12.300 ms  11.900 ms  12.100 ms
 3  * * *
 4  195.22.210.248  772.365 ms  75.900 ms  76.100 ms`
	hops := parse(raw)
	if len(hops) != 4 {
		t.Fatalf("got %d hops want 4: %+v", len(hops), hops)
	}
	if hops[0].Number != 1 || hops[0].Host != "192.168.178.1" || hops[0].RTTms != 2.100 {
		t.Errorf("hop1 = %+v want min RTT 2.100", hops[0])
	}
	if hops[2].Host != "*" || hops[2].RTTms != 0 {
		t.Errorf("unreachable hop = %+v want host=* rtt=0", hops[2])
	}
	// The 772ms first probe is a transient spike; min (75.9) is the baseline.
	if hops[3].Host != "195.22.210.248" || hops[3].RTTms != 75.9 {
		t.Errorf("hop4 = %+v want min RTT 75.9 (spike discarded)", hops[3])
	}
}

func TestParseEmpty(t *testing.T) {
	if hops := parse(""); hops != nil {
		t.Fatalf("empty input -> %v want nil", hops)
	}
	if hops := parse("garbage with no hop numbers\nstill nothing"); hops != nil {
		t.Fatalf("noise -> %v want nil", hops)
	}
}
