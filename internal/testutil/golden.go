package testutil

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden is registered only in test binaries importing testutil:
// `go test ./internal/<pkg> -update` rewrites that package's goldens.
// Do not pass -update to `go test ./...`: packages that do not import
// testutil reject the unknown flag.
var updateGolden = flag.Bool("update", false, "rewrite golden files with actual output")

// Golden compares got with the golden file testdata/<name> (relative to
// the calling test's package directory). With -update the file is
// rewritten first. Goldens are compared byte-for-byte; .gitattributes
// marks *.golden and testdata/** -text so Windows checkouts keep their
// line endings.
func Golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("golden: creating %s: %v", dir, err)
			}
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden: updating %s: %v", path, err)
		}
		t.Logf("golden: updated %s", path)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: reading %s: %v (run 'go test -update' to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match golden file %s (run 'go test -update' to update)\n--- want ---\n%s\n--- got ---\n%s",
			path, want, got)
	}
}

// GoldenString is Golden for string output.
func GoldenString(t *testing.T, name, got string) {
	t.Helper()
	Golden(t, name, []byte(got))
}
