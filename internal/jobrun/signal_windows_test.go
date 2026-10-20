//go:build windows

package jobrun

// ignoreTerm is a no-op: Windows jobs receive no SIGTERM.
func ignoreTerm() {}
