package cli

import (
	"bytes"
	"path/filepath"
	"testing"
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
