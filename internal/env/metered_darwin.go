//go:build darwin

package env

/*
#cgo LDFLAGS: -framework Network
#include <Network/Network.h>
#include <dispatch/dispatch.h>

// pathCost reads the system default path's cost flags from Network.framework:
// expensive (cellular, Personal Hotspot) and constrained (Low Data Mode, which
// the user can switch on per Wi-Fi network). Returns 0 if no path update
// arrived within the timeout. The handler only writes __block locals, so a late
// update after the timeout is harmless.
static int pathCost(int *expensive, int *constrained) {
	__block int got = 0, exp = 0, con = 0;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	dispatch_queue_t q = dispatch_queue_create("netqa.pathcost", DISPATCH_QUEUE_SERIAL);
	nw_path_monitor_t m = nw_path_monitor_create();
	nw_path_monitor_set_queue(m, q);
	nw_path_monitor_set_update_handler(m, ^(nw_path_t p) {
		if (got) return;
		exp = nw_path_is_expensive(p);
		con = nw_path_is_constrained(p);
		got = 1;
		dispatch_semaphore_signal(sem);
	});
	nw_path_monitor_start(m);
	dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
	nw_path_monitor_cancel(m);
	dispatch_sync(q, ^{}); // drain the queue so the handler is no longer running
	int ok = got;
	*expensive = exp;
	*constrained = con;
	nw_release(m);
	dispatch_release(q);
	dispatch_release(sem);
	return ok;
}
*/
import "C"

// Metered reports whether macOS considers the current internet path costly: an
// expensive interface (cellular, iPhone/iPad Personal Hotspot) or Low Data Mode.
// These are the same signals macOS itself uses to hold back background traffic,
// and a VPN path inherits them from the physical interface underneath. ok is
// false if the system did not answer in time.
func Metered() (metered, ok bool) {
	var exp, con C.int
	if C.pathCost(&exp, &con) == 0 {
		return false, false
	}
	return exp != 0 || con != 0, true
}
