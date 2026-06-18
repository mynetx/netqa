package trace

import "testing"

func TestParse(t *testing.T) {
	raw := `traceroute to 1.1.1.1 (1.1.1.1), 20 hops max, 52 byte packets
 1  192.168.178.1  2.345 ms
 2  10.0.0.1  12.300 ms
 3  * * *
 4  41.223.0.5  88.7 ms`
	hops := parse(raw)
	if len(hops) != 4 {
		t.Fatalf("got %d hops want 4: %+v", len(hops), hops)
	}
	if hops[0].Number != 1 || hops[0].Host != "192.168.178.1" || hops[0].RTTms != 2.345 {
		t.Errorf("hop1 = %+v", hops[0])
	}
	if hops[2].Host != "*" || hops[2].RTTms != 0 {
		t.Errorf("unreachable hop = %+v want host=* rtt=0", hops[2])
	}
	if hops[3].Host != "41.223.0.5" || hops[3].RTTms != 88.7 {
		t.Errorf("hop4 = %+v", hops[3])
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
