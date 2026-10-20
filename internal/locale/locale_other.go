//go:build !windows

package locale

// windowsUserLocale is unused off Windows; FromEnvironment reads LC_ALL,
// LC_TIME, and LANG there.
func windowsUserLocale() string { return "" }
