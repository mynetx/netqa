//go:build darwin

package env

import (
	"testing"
	"time"
)

// Smoke test against the real system: Network.framework must answer promptly
// (the value itself depends on the machine's current link).
func TestMeteredAnswers(t *testing.T) {
	start := time.Now()
	m, ok := Metered()
	if !ok {
		t.Fatal("Network.framework gave no path update")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Metered took %v", d)
	}
	t.Logf("metered=%v", m)
	for i := 0; i < 200; i++ { // no leak/crash across repeated calls
		Metered()
	}
}
