package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSyncClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSyncClose(f, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "data" {
		t.Fatalf("content = %q, err = %v", got, err)
	}
	if err := f.Close(); err == nil {
		t.Error("file still open after WriteSyncClose")
	}
}

func TestWriteSyncCloseWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) // read-only: Write fails
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSyncClose(f, []byte("data")); err == nil {
		t.Fatal("write to a read-only file succeeded")
	}
	if err := f.Close(); err == nil {
		t.Error("file still open after a failed WriteSyncClose")
	}
}
