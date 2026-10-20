package spool

import (
	"io"
	"os"
	"testing"
)

func TestFileLifecycle(t *testing.T) {
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
	f, err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if string(got) != "payload" {
		t.Fatalf("body = %q", got)
	}
	if err := s.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat after Abort = %v", err)
	}
}
