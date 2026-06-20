package throughput

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
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

func TestBytesForMbit(t *testing.T) {
	cases := []struct {
		mbit float64
		want int
	}{
		{0, fallbackDownBytes},  // unknown target -> fallback
		{-5, fallbackDownBytes}, // garbage -> fallback
		{2, minDownBytes},       // 2.5MB raw -> 3MB floor
		{40, 50_000_000},        // 50MB exact
		{100, maxDownBytes},     // 125MB raw -> 100MB cap
		{200, maxDownBytes},     // far over cap
	}
	for _, c := range cases {
		if got := BytesForMbit(c.mbit); got != c.want {
			t.Errorf("BytesForMbit(%v) = %d want %d", c.mbit, got, c.want)
		}
	}
}

func TestBytesForMbitUp(t *testing.T) {
	cases := []struct {
		mbit float64
		want int
	}{
		{0, fallbackUpBytes}, // unknown -> fallback
		{0.5, minUpBytes},    // 625k raw -> 1MB floor
		{10, 12_500_000},     // 12.5MB exact
		{40, maxUpBytes},     // 50MB raw -> 25MB cap
	}
	for _, c := range cases {
		if got := BytesForMbitUp(c.mbit); got != c.want {
			t.Errorf("BytesForMbitUp(%v) = %d want %d", c.mbit, got, c.want)
		}
	}
}

func TestSplitBytes(t *testing.T) {
	parts := splitBytes(10, 4) // 3,3,2,2
	if len(parts) != 4 {
		t.Fatalf("len = %d want 4", len(parts))
	}
	sum := 0
	for _, p := range parts {
		sum += p
	}
	if sum != 10 {
		t.Fatalf("sum = %d want 10 (parts %v)", sum, parts)
	}
}

func TestStreamsFor(t *testing.T) {
	// Below the cap, the requested count is kept.
	if got := streamsFor(4_000_000, 4); got != 4 {
		t.Errorf("streamsFor(4M,4) = %d want 4", got)
	}
	// 50M over 4 streams would be 12.5M each (over cap); needs more streams so
	// every share stays within the limit.
	got := streamsFor(50_000_000, 4)
	if per := 50_000_000 / got; per > cloudflareMaxBytes {
		t.Errorf("streamsFor(50M,4) = %d -> %d bytes/stream exceeds cap %d", got, per, cloudflareMaxBytes)
	}
}

// TestMeasureDownloadRejected proves a non-200 reply (like Cloudflare's 403 for
// an over-limit ?bytes=) is treated as a failed stream, not a silent zero.
func TestMeasureDownloadRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	rate, ok := download(context.Background(), srv.Client(), srv.URL+"/__down?bytes=", 8_000_000, 4)
	if ok || rate != 0 {
		t.Fatalf("rejected download must fail: rate=%v ok=%v", rate, ok)
	}
}

func TestMeasureMultiStream(t *testing.T) {
	var downServed, upReceived atomic.Int64
	var downStreams int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "__down"):
			atomic.AddInt32(&downStreams, 1)
			n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
			downServed.Add(int64(n))
			w.Write(make([]byte, n))
		case strings.Contains(r.URL.Path, "__up"):
			n, _ := io.Copy(io.Discard, r.Body)
			upReceived.Add(n)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	m := Measurer{
		Client:    srv.Client(),
		DownBytes: 4_000_000,
		UpBytes:   1_000_000,
		Streams:   4,
		DownURL:   srv.URL + "/__down?bytes=",
		UpURL:     srv.URL + "/__up",
	}
	res, err := m.Measure(context.Background())
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if got := downServed.Load(); got != 4_000_000 {
		t.Errorf("downloaded %d bytes across streams, want 4000000", got)
	}
	if downStreams != 4 {
		t.Errorf("download used %d streams, want 4", downStreams)
	}
	if got := upReceived.Load(); got != 1_000_000 {
		t.Errorf("uploaded %d bytes, want 1000000", got)
	}
	if res.DownMbit <= 0 || res.UpMbit <= 0 {
		t.Errorf("rates must be positive: down=%v up=%v", res.DownMbit, res.UpMbit)
	}
}

func TestMeasureDownloadFails(t *testing.T) {
	// Closed server -> every stream errors -> Measure reports failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	m := Measurer{DownBytes: 1_000_000, Streams: 2, DownURL: url + "/__down?bytes=", UpURL: url + "/__up"}
	if _, err := m.Measure(context.Background()); err == nil {
		t.Fatal("expected error when all download streams fail")
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
