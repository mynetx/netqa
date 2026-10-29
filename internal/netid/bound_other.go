//go:build !darwin

package netid

import "syscall"

// boundControl is a no-op off macOS: the request follows the default route.
func boundControl(string) func(network, address string, c syscall.RawConn) error { return nil }
