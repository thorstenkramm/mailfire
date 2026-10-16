//go:build unix

package cli

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// TestSendAttachmentFIFO: a named pipe is refused before it is opened;
// opening it would block until a writer appears.
func TestSendAttachmentFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	type result struct {
		code   int
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, _, stderr := runMock(t, m, "body",
			"-c", cfg, "--to", "a@example.com", "--subject", "s", "--attach", fifo)
		done <- result{code, stderr}
	}()
	select {
	case r := <-done:
		if r.code != ExitUsage {
			t.Errorf("exit = %d, want %d", r.code, ExitUsage)
		}
		if !strings.Contains(r.stderr, "not a regular file") {
			t.Errorf("stderr missing %q:\n%s", "not a regular file", r.stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send blocked on the FIFO")
	}
}

func TestBodyFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "body-pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readBody(fifo, unreadableInput{}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("body read blocked on FIFO")
	}
}

// TestSendFooterFIFO: a named pipe as --footer is refused like an
// attachment instead of blocking the send.
func TestSendFooterFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	type result struct {
		code   int
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, _, stderr := runMock(t, m, "body",
			"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md", "--footer", fifo)
		done <- result{code, stderr}
	}()
	select {
	case r := <-done:
		if r.code != ExitUsage {
			t.Errorf("exit = %d, want %d", r.code, ExitUsage)
		}
		if !strings.Contains(r.stderr, "footer "+fifo+": not a regular file") {
			t.Errorf("stderr missing footer FIFO error:\n%s", r.stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send blocked on the FIFO")
	}
}
