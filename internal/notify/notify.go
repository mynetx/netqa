// Package notify sends macOS user notifications via osascript. Used for
// confirmed-outage and recovery alerts only.
package notify

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Notify posts a macOS notification. Errors are returned but typically ignored
// by callers — a failed notification must never disrupt collection.
func Notify(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := `display notification "` + escape(body) + `" with title "` + escape(title) + `"`
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
