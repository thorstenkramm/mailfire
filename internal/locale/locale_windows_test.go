//go:build windows

package locale

import "testing"

// The Windows user locale is read through GetUserDefaultLocaleName.
func TestWindowsUserLocale(t *testing.T) {
	name := windowsUserLocale()
	if name == "" {
		t.Fatal("windowsUserLocale() is empty")
	}
	t.Logf("user locale: %s", name)
	if _, ok := Normalize(name); !ok {
		t.Logf("%s is not supported by monday; run falls back to en_US", name)
	}
	// LANG is ignored on Windows: the user locale decides.
	l, ok := FromEnvironment(func(string) string { return "xx_YY" })
	if want, wantOK := Normalize(name); l != want || ok != wantOK {
		t.Errorf("FromEnvironment = %q, %v; want the user locale %q, %v", l, ok, want, wantOK)
	}
}
