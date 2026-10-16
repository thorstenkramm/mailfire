package mailbox

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, s := range []string{`"Doe, John" <john+tag@example.com>`, `"a@b"@example.com`, `"a b"@example.com`, "jörg@exämple.com"} {
		if _, err := Parse(s); err != nil {
			t.Errorf("Parse(%q): %v", s, err)
		}
	}
	for _, s := range []string{"bad", "tk@system42", "a@-example.com", "a@example-.com", "a@example..com", "a@example.com.", "a@exam_ple.com", "a@[127.0.0.1]", "a@" + strings.Repeat("a", 64) + ".com", "a@" + strings.Repeat("example.", 40) + "com", "a@\u200d.com"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded", s)
		}
	}
}
