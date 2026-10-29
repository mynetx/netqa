package netid

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// boundControl pins a socket to the named interface (IP_BOUND_IF), so a request
// leaves through the physical link even while a VPN owns the default route.
func boundControl(iface string) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		ifi, err := net.InterfaceByName(iface)
		if err != nil {
			return err
		}
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifi.Index)
		}); err != nil {
			return err
		}
		return serr
	}
}
