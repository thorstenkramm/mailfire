package cli

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// Jobs can modify (or remove) their own deployment files. Both a real send
// and a preview must use the inputs that passed preflight, even afterward.
func TestRunRetainsPreparedInputs(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(strconv.FormatBool(preview), func(t *testing.T) {
			dir := t.TempDir()
			layout, footer, report := filepath.Join(dir, "layout.html"), filepath.Join(dir, "footer.html"), filepath.Join(dir, "report.md")
			for path, contents := range map[string]string{
				layout: `<html><body>original layout {{.Body}} {{.Footer}}</body></html>`,
				footer: `<p>original footer</p>`,
				report: `original report: {{.Status}}`,
			} {
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := writeConfig(t, "from = \"ops@example.com\"\ntemplate_file = "+strconv.Quote(layout)+"\n")
			args := runArgs(cfg, "--footer", footer, "--report-template", report)
			if preview {
				args = append(args, "--preview", "-")
			}
			m := testutil.NewMockMailgun(t)
			code, stdout, stderr := runRun(t, m, append(args, helperJob(t, "remove-inputs", cfg, layout, footer, report)...)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			doc := stdout
			if !preview {
				doc = m.LastRequest(t).Field("html")
			}
			for _, want := range []string{"original layout", "original footer", "original report: success"} {
				if !strings.Contains(doc, want) {
					t.Errorf("report lacks %q: %s", want, doc)
				}
			}
		})
	}
}

func TestPreparationReleasesLoggerOnLocalFailure(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "run.log")
	cfgPath := writeConfig(t, "provider = \"gmail\"\nfrom = \"ops@example.com\"\n[logging]\nlogfile = "+strconv.Quote(log)+"\n[gmail]\ncredentials = "+strconv.Quote(dir)+"\n")
	cfg, err := config.Load(cfgPath, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRunDelivery(cfg, &SendOptions{}, &runContext{}); err == nil {
		t.Fatal("expected unreadable credentials")
	}
	// Windows cannot remove this file if preparation leaked its log handle.
	if err := os.Remove(log); err != nil {
		t.Fatalf("logger not released: %v", err)
	}
}

func TestSkippedRunReleasesPreparedLogger(t *testing.T) {
	log := filepath.Join(t.TempDir(), "run.log")
	cfg := writeConfig(t, "from = \"ops@example.com\"\n[logging]\nlogfile = "+strconv.Quote(log)+"\n")
	code, _, stderr := runRun(t, nil, append(runArgs(cfg, "--on", "failure"), helperJob(t, "echo", "ok")...)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if err := os.Remove(log); err != nil {
		t.Fatalf("logger not released after skipped report: %v", err)
	}
}

func TestPreparedGmailRefreshesOnlyAtDelivery(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(time.Hour))
	cfg, err := config.Load(e.cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts := &SendOptions{To: []string{"ops@example.com"}, Subject: "report", Format: "txt", Message: "job finished", MessageSet: true}
	rc := &runContext{}
	e.g.option()(rc)
	p, err := prepareRunDelivery(cfg, opts, rc)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	if tokens, sends := e.g.counts(); tokens != 0 || sends != 0 {
		t.Fatal("preparation contacted Gmail")
	}
	msg, err := compose(cfg, opts, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles(msg.Inline)
	defer closeFiles(msg.Attachments)
	// Model a long-running job: credentials expired, and the original
	// mutable config is no longer valid. Use the snapshot and refresh now.
	e.login(t, time.Now().Add(-time.Hour))
	cfg.GmailCredentials = filepath.Join(t.TempDir(), "missing.json")
	if code := p.sendMessage(rc, msg, &sendResult{}, io.Discard, io.Discard); code != ExitOK {
		t.Fatalf("send exit %d", code)
	}
	if tokens, sends := e.g.counts(); tokens != 1 || sends != 1 {
		t.Fatalf("tokens=%d sends=%d", tokens, sends)
	}
	if _, auths := e.g.recorded(); len(auths) != 1 || auths[0] != "Bearer fresh-access" {
		t.Fatalf("authorization=%v", auths)
	}
}

func TestRunPreflightDoesNotReserveNotificationOrSkipJob(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, "from = \"ops@example.com\"\n[dedup]\nstate_dir = "+strconv.Quote(filepath.Join(dir, "dedup"))+"\n[defer]\ndefer_dir = "+strconv.Quote(filepath.Join(dir, "deferred"))+"\n")
	m := testutil.NewMockMailgun(t)
	for i := 0; i < 2; i++ {
		marker := filepath.Join(dir, strconv.Itoa(i))
		code, stdout, stderr := runRun(t, m, append(runArgs(cfg, "--dedup-key", "nightly"), helperJob(t, "touch", marker)...)...)
		if code != ExitOK {
			t.Fatalf("run %d: exit %d: %s", i, code, stderr)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("job was skipped: %v", err)
		}
		if len(m.Requests()) != 1 {
			t.Fatalf("expected one delivery, got %d", len(m.Requests()))
		}
		if i == 1 && !strings.Contains(stdout, "skipped:") {
			t.Fatalf("second report was not deduplicated: %s", stdout)
		}
	}
}
