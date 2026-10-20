//go:build windows

package locale

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsUserLocale returns the user's Windows display locale, e.g.
// "de-DE". Empty when it cannot be determined.
func windowsUserLocale() string {
	// kernel32!GetUserDefaultLocaleName, LOCALE_NAME_MAX_LENGTH = 85.
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	buf := make([]uint16, 85)
	n, _, err := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		_ = err
		return ""
	}
	return windows.UTF16ToString(buf[:n-1])
}
