//go:build !darwin

package env

// Metered is unsupported off macOS; links are treated as unmetered.
func Metered() (metered, ok bool) { return false, false }
