//go:build !windows

package lifetime

// Non-Windows lifecycle behavior still requires platform acceptance testing.
func Guard() error { return nil }
