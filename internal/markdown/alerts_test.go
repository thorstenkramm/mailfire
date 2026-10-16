package markdown

import (
	"strings"
	"testing"
)

const alertsSrc = `> [!NOTE]
> Useful *information*.
>
> Second paragraph.

> [!warning]
> Careful.

> Just a quote.

- > [!TIP]
  > nested, stays a quote

> [!IMPORTANT] not alone on the line
`

func TestHTMLAlerts(t *testing.T) {
	got := HTML(alertsSrc)
	want := `<table role="presentation" class="alert alert-note" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td class="alert-body">
<p class="alert-title">Note</p>
<p>Useful <em>information</em>.</p>
<p>Second paragraph.</p>
</td></tr></table>
<table role="presentation" class="alert alert-warning" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td class="alert-body">
<p class="alert-title">Warning</p>
<p>Careful.</p>
</td></tr></table>
<blockquote>
<p>Just a quote.</p>
</blockquote>
<ul>
<li>
<blockquote>
<p>[!TIP]
nested, stays a quote</p>
</blockquote>
</li>
</ul>
<blockquote>
<p>[!IMPORTANT] not alone on the line</p>
</blockquote>
`
	if got != want {
		t.Errorf("HTML mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestHTMLAlertKinds(t *testing.T) {
	for kind, title := range map[string]string{
		"NOTE": "Note", "TIP": "Tip", "IMPORTANT": "Important", "WARNING": "Warning", "CAUTION": "Caution",
	} {
		got := HTML("> [!" + kind + "]\n> text\n")
		cls := `class="alert alert-` + strings.ToLower(kind) + `"`
		if !strings.Contains(got, cls) || !strings.Contains(got, `<p class="alert-title">`+title+`</p>`) {
			t.Errorf("%s: unexpected HTML\n%s", kind, got)
		}
	}
}

// TestHTMLAlertMarkerOnly: an alert with only the marker line renders as
// an empty alert, like on GitHub.
func TestHTMLAlertMarkerOnly(t *testing.T) {
	got := HTML("> [!CAUTION]\n")
	want := `<table role="presentation" class="alert alert-caution" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td class="alert-body">
<p class="alert-title">Caution</p>
</td></tr></table>
`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTextAlerts(t *testing.T) {
	got := Text(alertsSrc + "\n> [!CAUTION]\n")
	// The blank line inside the NOTE quote keeps its "> " prefix,
	// trailing space included, like a plain blockquote.
	want := "> NOTE\n> Useful information.\n> \n> Second paragraph.\n\n" +
		"> WARNING\n> Careful.\n\n" +
		"> Just a quote.\n\n" +
		"- > [!TIP]\n  > nested, stays a quote\n\n" +
		"> [!IMPORTANT] not alone on the line\n\n" +
		"> CAUTION\n"
	if got != want {
		t.Errorf("Text mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}
