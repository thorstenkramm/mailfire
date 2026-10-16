package render

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "templates")
	if err := Export(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range Names {
		for _, optional := range []bool{false, true} {
			d := Data{Template: name, Subject: `A < B & "C"`, Body: "<p>Hello</p>"}
			if optional {
				d.Preheader = "Preview <&>"
				d.Footer = "<b>Footer</b>"
				d.Lang = "de"
			}
			got, err := File(filepath.Join(dir, name+".html"), d)
			if err != nil {
				t.Fatal(err)
			}
			if want := Base(d); got != want {
				t.Errorf("%s optional=%v: exported rendering differs from built-in", name, optional)
			}
		}
	}
	if err := Export(dir); err == nil {
		t.Fatal("export overwrote existing templates")
	}
}

func TestCustomErrors(t *testing.T) {
	dir := t.TempDir()
	for _, source := range []string{"{{", "{{.Unknown}}", `{{template "missing" .}}`} {
		path := filepath.Join(dir, "custom.html")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := File(path, Data{}); err == nil {
			t.Errorf("accepted invalid template %q", source)
		}
	}
	for _, path := range []string{dir, filepath.Join(dir, "missing")} {
		if _, err := File(path, Data{}); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
}
