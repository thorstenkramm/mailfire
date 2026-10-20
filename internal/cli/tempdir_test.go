package cli

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

func TestSendUsesConfiguredTempDir(t *testing.T) {
	parent := t.TempDir()
	tempDir := filepath.Join(parent, "mailfire-temp") // created by the send preflight
	cfg := writeConfig(t, "from = \"ops@example.com\"\ntemp_dir = "+quoteTOML(tempDir)+"\n")
	m := testutil.NewMockMailgun(t)
	seen := false
	m.SendHandler = func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(tempDir)
		if err != nil {
			t.Errorf("reading temp_dir during send: %v", err)
		} else {
			for _, entry := range entries {
				seen = seen || strings.HasPrefix(entry.Name(), "mailfire-message-")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, `{"id":"<temp-dir@mg.example.com>","message":"Queued. Thank you."}`); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}
	code, _, stderr := runMock(t, m, "body", "-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !seen {
		t.Error("no message spool appeared in configured temp_dir during send")
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "mailfire-message-") {
			t.Errorf("spool left behind: %s", entry.Name())
		}
	}
}

func TestConfiguredTempDirStopsSendAndRunBeforeWork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-parent", "temp")
	cfg := writeConfig(t, "from = \"ops@example.com\"\ntemp_dir = "+quoteTOML(missing)+"\n")
	m := testutil.NewMockMailgun(t)

	code, _, stderr := runMock(t, m, "body", "-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitConfig || !strings.Contains(stderr, "sending.temp_dir") {
		t.Errorf("send exit=%d stderr=%q, want config error naming temp_dir", code, stderr)
	}
	marker := filepath.Join(t.TempDir(), "job-ran")
	var stdout, runStderr bytes.Buffer
	code = Run(append([]string{"run"}, append(runArgs(cfg, "--preview", "-"), helperJob(t, "touch", marker)...)...),
		&stdout, &runStderr, testInfo, WithBaseURL(m.URL()))
	if code != ExitConfig || !strings.Contains(runStderr.String(), "sending.temp_dir") {
		t.Errorf("run exit=%d stderr=%q, want config error naming temp_dir", code, runStderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("run started the job despite invalid temp_dir: %v", err)
	}
}

func quoteTOML(s string) string { return strconv.Quote(s) }
