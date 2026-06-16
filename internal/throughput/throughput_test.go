package throughput

import (
	"testing"
	"time"
)

func TestParseInterfaceBytes(t *testing.T) {
	out := `Name       Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
en0        1500  <Link#14>   de:ad:be:ef:00:14 44930855     0 41826874767 34106505     0 18078617858     0
en0        1500  192.168.178   192.168.178.128 44930855     - 41826874767 34106505     - 18078617858     -
`
	in, outB, ok := parseInterfaceBytes(out)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if in != 41826874767 {
		t.Fatalf("inBytes = %d want 41826874767", in)
	}
	if outB != 18078617858 {
		t.Fatalf("outBytes = %d want 18078617858", outB)
	}
}

func TestParseInterfaceBytesNoLink(t *testing.T) {
	if _, _, ok := parseInterfaceBytes("garbage\nno link row here"); ok {
		t.Fatal("expected ok=false when no <Link row")
	}
}

func TestMbit(t *testing.T) {
	// 5,000,000 bytes in 1s = 40 Mbit.
	if got := mbit(5_000_000, time.Second); got != 40 {
		t.Fatalf("mbit = %v want 40", got)
	}
	if got := mbit(100, 0); got != 0 {
		t.Fatalf("zero-duration must be 0, got %v", got)
	}
}

func TestSaturatingSub(t *testing.T) {
	// Counter reset/wrap must not produce a huge bogus delta.
	if got := saturatingSub(5, 10); got != 0 {
		t.Fatalf("saturatingSub(5,10) = %d want 0", got)
	}
	if got := saturatingSub(10, 4); got != 6 {
		t.Fatalf("saturatingSub(10,4) = %d want 6", got)
	}
}
