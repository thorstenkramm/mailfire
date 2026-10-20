//go:build windows

package jobrun

import (
	"testing"

	"golang.org/x/sys/windows"
)

func watchDescendant(t *testing.T, pid int) func() {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		state, err := windows.WaitForSingleObject(handle, 0)
		if err != nil {
			t.Error(err)
		}
		if state == uint32(windows.WAIT_TIMEOUT) {
			if err := windows.TerminateProcess(handle, 1); err != nil {
				t.Error(err)
			}
		}
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	})
	return func() {
		t.Helper()
		state, err := windows.WaitForSingleObject(handle, 1000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("descendant %d survived termination: state=%d error=%v", pid, state, err)
		}
	}
}
