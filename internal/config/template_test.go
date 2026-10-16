package config

import (
	"strings"
	"testing"
)

func TestLoadTemplate(t *testing.T) {
	isolate(t)
	for _, tc := range []struct{ line, want string }{
		{"", "card"},
		{`template = ""` + "\n", "card"},
		{`template = "plain-serif"` + "\n", "plain-serif"},
		{`template = " Card-Serif "` + "\n", "card-serif"},
	} {
		path := writeConfig(t, t.TempDir(), minimalTOML+tc.line)
		if got := loadOK(t, path).Template; got != tc.want {
			t.Errorf("%q: Template = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestLoadTemplateInvalid(t *testing.T) {
	isolate(t)
	path := writeConfig(t, t.TempDir(), minimalTOML+`template = "base"`+"\n")
	msg := loadErr(t, path)
	want := `line 8: sending.template: invalid value "base": must be one of "card", "card-serif", "plain", or "plain-serif"`
	if !strings.Contains(msg, want) {
		t.Errorf("error = %q, want it to contain %q", msg, want)
	}
}
