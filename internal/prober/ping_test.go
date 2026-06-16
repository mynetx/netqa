package prober

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTCPPingerLocalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p := TCPPinger{Port: port, Timeout: time.Second}
	res := p.Ping(context.Background(), "127.0.0.1")
	if !res.Success {
		t.Fatal("expected successful TCP connect")
	}
	if res.RTT <= 0 {
		t.Fatalf("expected positive RTT, got %v", res.RTT)
	}
}

func TestTCPPingerClosedPortFails(t *testing.T) {
	// Reserve a port then close it so nothing listens.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()

	p := TCPPinger{Port: port, Timeout: 500 * time.Millisecond}
	res := p.Ping(context.Background(), "127.0.0.1")
	if res.Success {
		t.Fatal("expected failure dialing closed port")
	}
}
