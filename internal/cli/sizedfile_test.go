package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// An attachment that changes size while it is read must fail the read —
// and through io.Copy too, which would bypass Read via *os.File's WriteTo
// if sizedFile exposed one.
func TestSizedFileDetectsChangesWhileReading(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 100)
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path string)
		want   string // error substring; "" means success
	}{
		{"unchanged", func(*testing.T, string) {}, ""},
		{"shrank", func(t *testing.T, path string) {
			if err := os.Truncate(path, 10); err != nil {
				t.Fatal(err)
			}
		}, "file shrank while being read (10 of 1000 bytes)"},
		{"grew", func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer testutil.Check(t, f.Close)
			if _, err := f.WriteString("appended"); err != nil {
				t.Fatal(err)
			}
		}, "file grew while being read (1000 bytes when opened)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.log")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := openSized("attachment", path)
			if err != nil {
				t.Fatal(err)
			}
			defer testutil.Check(t, s.Close)
			tc.change(t, path)
			var got bytes.Buffer
			_, err = io.Copy(&got, s)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unchanged file: %v", err)
			case tc.want == "" && !bytes.Equal(got.Bytes(), content):
				t.Fatalf("content = %d bytes, want %d", got.Len(), len(content))
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
