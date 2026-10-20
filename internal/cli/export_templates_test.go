package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

func TestExportTemplatesCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"export-templates", "--help"}, ExitOK},
		{[]string{"export-templates", "--typo"}, ExitUsage},
		{[]string{"export-templates"}, ExitUsage},
		{[]string{"export-templates", "--", filepath.Join(t.TempDir(), "--templates")}, ExitOK},
		{[]string{"export-templates", dir}, ExitOK},
		{[]string{"export-templates", dir}, ExitUsage},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(tc.args, &stdout, &stderr, BuildInfo{}); code != tc.code {
			t.Errorf("%v: exit %d, want %d: %s", tc.args, code, tc.code, stderr.String())
		}
	}
}

// The exported job report template is usable with run --report-template.
func TestExportedReportTemplateRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"export-templates", dir}, &stdout, &stderr, BuildInfo{}); code != ExitOK {
		t.Fatalf("export: exit %d: %s", code, stderr.String())
	}
	tmpl := filepath.Join(dir, "job-report.md")
	if _, err := os.Stat(tmpl); err != nil {
		t.Fatalf("job-report.md not exported: %v", err)
	}
	// An existing job-report.md alone blocks a second export.
	only := filepath.Join(t.TempDir(), "only")
	if err := os.MkdirAll(only, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(only, "job-report.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"export-templates", only}, &stdout, &stderr, BuildInfo{}); code != ExitUsage {
		t.Errorf("export over an existing job-report.md: exit %d, want %d", code, ExitUsage)
	}
	if data, _ := os.ReadFile(filepath.Join(only, "job-report.md")); string(data) != "mine" {
		t.Error("export replaced an existing job-report.md")
	}

	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, errOut := runRun(t, m, append(runArgs(cfg, "--report-template", tmpl), helperJob(t, "echo", "x")...)...)
	if code != ExitOK {
		t.Fatalf("run with the exported template: exit %d: %s", code, errOut)
	}
	if !strings.Contains(m.LastRequest(t).Field("text"), "nightly succeeded") {
		t.Errorf("text part:\n%s", m.LastRequest(t).Field("text"))
	}
}
