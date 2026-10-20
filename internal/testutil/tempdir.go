package testutil

import (
	"fmt"
	"os"
	"testing"
)

// Check calls fn — a Close, an Abort — and reports its error to t. It is for
// defer and t.Cleanup: defer testutil.Check(t, f.Close).
func Check(t testing.TB, fn func() error) {
	t.Helper()
	if err := fn(); err != nil {
		t.Error(err)
	}
}

// RunWithPrivateTempDir runs the tests of m with the temporary directory
// (TMPDIR; TMP and TEMP on Windows) pointing at a fresh directory that is
// removed afterwards. Provider tests call SendMessage, which leaves the
// message spool to its caller; this keeps those files out of the system
// temporary directory. Use it from TestMain: os.Exit(RunWithPrivateTempDir(m)).
func RunWithPrivateTempDir(m *testing.M) int {
	dir, err := os.MkdirTemp("", "mailfire-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: creating private temp dir: %v\n", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "testutil: removing private temp dir: %v\n", err)
		}
	}()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(name, dir); err != nil {
			fmt.Fprintf(os.Stderr, "testutil: setting %s: %v\n", name, err)
			return 1
		}
	}
	return m.Run()
}
