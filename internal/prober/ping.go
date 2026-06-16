package prober

import (
	"context"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// protocolICMP is the IANA protocol number for ICMP, used when parsing replies.
const protocolICMP = 1

// Pinger probes a single target host and reports reachability + RTT.
type Pinger interface {
	Ping(ctx context.Context, host string) Result
}

// ICMPPinger sends unprivileged ICMP echo requests over a UDP socket
// ("udp4" datagram-oriented ICMP), which macOS permits without root.
type ICMPPinger struct {
	Timeout time.Duration
}

// Ping sends one ICMP echo to host and waits for the reply.
func (p ICMPPinger) Ping(ctx context.Context, host string) Result {
	to := p.Timeout
	if to == 0 {
		to = 2 * time.Second
	}
	res := Result{TS: time.Now(), Target: host}

	ipAddr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return res
	}

	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return res
	}
	defer conn.Close()

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: []byte("netqa")},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return res
	}

	start := time.Now()
	deadline := start.Add(to)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.WriteTo(wb, &net.UDPAddr{IP: ipAddr.IP}); err != nil {
		return res
	}

	rb := make([]byte, 1500)
	n, _, err := conn.ReadFrom(rb)
	if err != nil {
		return res
	}
	rm, err := icmp.ParseMessage(protocolICMP, rb[:n])
	if err != nil {
		return res
	}
	if rm.Type == ipv4.ICMPTypeEchoReply {
		res.Success = true
		res.RTT = time.Since(start)
	}
	return res
}

// TCPPinger is a fallback that measures a TCP connect to host:port. Useful where
// ICMP is filtered; a successful 443 connect proves reachability.
type TCPPinger struct {
	Port    string
	Timeout time.Duration
}

// Ping dials host:Port and records connect latency.
func (p TCPPinger) Ping(ctx context.Context, host string) Result {
	port := p.Port
	if port == "" {
		port = "443"
	}
	to := p.Timeout
	if to == 0 {
		to = 2 * time.Second
	}
	res := Result{TS: time.Now(), Target: host}

	d := net.Dialer{Timeout: to}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	start := time.Now()
	conn, err := d.DialContext(cctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return res
	}
	conn.Close()
	res.Success = true
	res.RTT = time.Since(start)
	return res
}
