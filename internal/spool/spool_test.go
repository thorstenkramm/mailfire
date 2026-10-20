package spool

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLifecycle(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, err := Create()
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	t.Cleanup(func() { _ = s.Abort() })
	if _, err := s.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	if s.Size() != 7 {
		t.Fatalf("Size = %d, want 7", s.Size())
	}
	var got bytes.Buffer
	if err := s.CopyTo(&got); err != nil {
		t.Fatal(err)
	}
	if got.String() != "payload" {
		t.Fatalf("body = %q", got.String())
	}
	if err := s.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat after Abort = %v", err)
	}
}

func TestRemoveStaleKeepsRecentAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := write("mailfire-message-1.spool", 2*staleAge)
	recent := write("mailfire-message-2.spool", time.Minute)
	foreign := write("other-1.spool", 2*staleAge)
	removeStale(dir, now)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale spool kept: %v", err)
	}
	for _, p := range []string{recent, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
}

func TestHTTPBodyRewinds(t *testing.T) {
	body, getBody := HTTPBody(bytes.NewReader([]byte("payload")), 7)
	if getBody == nil {
		t.Fatal("no GetBody for an io.ReaderAt")
	}
	for range 2 {
		got, err := io.ReadAll(body)
		if err != nil || string(got) != "payload" {
			t.Fatalf("body = %q, %v", got, err)
		}
		if body, err = getBody(); err != nil {
			t.Fatal(err)
		}
	}
	if _, getBody := HTTPBody(io.MultiReader(), 0); getBody != nil {
		t.Error("GetBody for a reader that cannot rewind")
	}
}
