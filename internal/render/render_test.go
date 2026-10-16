package render

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// noteBody is the README example body (mailfire --md), as produced by the
// Markdown renderer, with its trailing newline.
const noteBody = `<h1>This is an Email</h1>
<p>It is written in markdown. It&rsquo;s delivered as HTML.</p>
<p><a href="https://github.com/thorstenkramm/mailfire">mailfire</a></p>
`

// TestBaseNote renders the README example in every template and compares
// it against the reference renderings (testdata/note.<template>.html).
// It covers: default Lang, no preheader, no footer.
func TestBaseNote(t *testing.T) {
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			got := Base(Data{Template: name, Subject: "Have a nice day", Body: noteBody})
			testutil.GoldenString(t, "note."+name+".html", got)
		})
	}
}

// TestBaseReport renders a full report body with preheader and footer in
// every template and compares it against the reference renderings
// (testdata/report.<template>.html).
func TestBaseReport(t *testing.T) {
	body, err := os.ReadFile("testdata/report.body.html")
	if err != nil {
		t.Fatalf("reading report body: %v", err)
	}
	for _, name := range Names {
		t.Run(name, func(t *testing.T) {
			got := Base(Data{
				Template:  name,
				Subject:   "Nightly backup finished",
				Preheader: "All five hosts were backed up; files01 needed a retry.",
				Body:      HTML(body),
				Footer:    `Sent automatically by the nightly backup job on ops01. <a href="https://example.com/backups/settings">Change who gets this report</a>.`,
			})
			testutil.GoldenString(t, "report."+name+".html", got)
		})
	}
}

// TestBaseDefaultTemplate: an empty Template renders DefaultTemplate.
func TestBaseDefaultTemplate(t *testing.T) {
	d := Data{Subject: "s", Body: "<p>hi</p>\n"}
	got := Base(d)
	d.Template = DefaultTemplate
	if want := Base(d); got != want {
		t.Error("empty Template does not render DefaultTemplate")
	}
	if DefaultTemplate != "card" {
		t.Errorf("DefaultTemplate = %q, want card", DefaultTemplate)
	}
}

func TestValid(t *testing.T) {
	for _, name := range Names {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false", name)
		}
	}
	for _, name := range []string{"", "base", "Card", "card-sans"} {
		if Valid(name) {
			t.Errorf("Valid(%q) = true", name)
		}
	}
	if len(Names) != len(looks) {
		t.Errorf("Names lists %d templates, looks defines %d", len(Names), len(looks))
	}
}

func TestBaseUnknownTemplatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("unknown template did not panic")
		}
	}()
	Base(Data{Template: "nope", Subject: "s"})
}

func TestBaseEscapesSubject(t *testing.T) {
	got := Base(Data{Subject: `Fish & "Chips" <3`, Body: "<p>hi</p>\n"})
	escaped := `Fish &amp; &#34;Chips&#34; &lt;3`
	for _, want := range []string{
		"<title>" + escaped + "</title>",
		`aria-label="` + escaped + `"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestBaseEscapesPreheader(t *testing.T) {
	got := Base(Data{
		Subject:   "s",
		Preheader: `2 < 3 & "fine"`,
		Body:      "<p>hi</p>\n",
	})
	if !strings.Contains(got, `2 &lt; 3 &amp; &#34;fine&#34;`) {
		t.Error("preheader is not HTML-escaped")
	}
}

func TestBaseInsertsBodyAndFooterAsIs(t *testing.T) {
	body := HTML("<p>a &amp; b — already <strong>HTML</strong></p>\n")
	footer := HTML(`<a href="https://example.com/?a=1&b=2">link & more</a>`)
	got := Base(Data{Subject: "s", Body: body, Footer: footer})
	if !strings.Contains(got, string(body)) {
		t.Error("body was escaped or altered")
	}
	if !strings.Contains(got, string(footer)) {
		t.Error("footer was escaped or altered")
	}
}

func TestBaseLang(t *testing.T) {
	def := Base(Data{Subject: "s", Body: "<p>hi</p>\n"})
	if n := strings.Count(def, `lang="en"`); n != 2 {
		t.Errorf("default lang: found %d occurrences of lang=\"en\", want 2", n)
	}
	de := Base(Data{Lang: "de", Subject: "s", Body: "<p>hi</p>\n"})
	if n := strings.Count(de, `lang="de"`); n != 2 {
		t.Errorf("explicit lang: found %d occurrences of lang=\"de\", want 2", n)
	}
	if strings.Contains(de, `lang="en"`) {
		t.Error("explicit lang: output still contains lang=\"en\"")
	}
}

func TestBaseEscapesLang(t *testing.T) {
	got := Base(Data{Lang: `x" onload="y`, Subject: "s", Body: "<p>hi</p>\n"})
	if n := strings.Count(got, `lang="x&#34; onload=&#34;y"`); n != 2 {
		t.Errorf("found %d escaped lang attributes, want 2", n)
	}
}

// TestBaseConcurrent renders in parallel; run with -race to verify the
// shared parsed template carries no mutable state.
func TestBaseConcurrent(t *testing.T) {
	want := Base(Data{Subject: "s", Body: "<p>hi</p>\n"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := Base(Data{Subject: "s", Body: "<p>hi</p>\n"}); got != want {
				t.Error("concurrent render differs")
			}
		}()
	}
	wg.Wait()
}

func TestBasePreheaderAbsent(t *testing.T) {
	got := Base(Data{Subject: "s", Body: "<p>hi</p>\n"})
	if strings.Contains(got, "display:none;max-height:0") {
		t.Error("hidden preheader block rendered although Preheader is empty")
	}
}

func TestBaseFooterAbsent(t *testing.T) {
	for _, name := range Names {
		got := Base(Data{Template: name, Subject: "s", Body: "<p>hi</p>\n"})
		if strings.Contains(got, `class="footer-text"`) {
			t.Errorf("%s: footer block rendered although Footer is empty", name)
		}
	}
}

// TestBaseKeepsMSOConditionals is the regression guard for the choice of
// text/template: html/template would strip these comments and break
// Outlook rendering.
func TestBaseKeepsMSOConditionals(t *testing.T) {
	for name, width := range map[string]string{"card": "600", "plain": "640"} {
		got := Base(Data{Template: name, Subject: "s", Body: "<p>hi</p>\n"})
		for _, want := range []string{
			"<!--[if mso]>",
			"<!--[if mso]><table role=\"presentation\" width=\"" + width + "\"",
			"<!--[if mso]></td></tr></table><![endif]-->",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: output missing Outlook conditional %q", name, want)
			}
		}
	}
}

// TestBaseAlertStyles: every template styles all five alert types, in
// light and dark mode.
func TestBaseAlertStyles(t *testing.T) {
	for _, name := range Names {
		got := Base(Data{Template: name, Subject: "s", Body: "<p>hi</p>\n"})
		for _, kind := range []string{"note", "tip", "important", "warning", "caution"} {
			if n := strings.Count(got, ".content table.alert-"+kind+" td.alert-body p.alert-title"); n != 2 {
				t.Errorf("%s: %s alert title styled %d times, want 2 (light + dark)", name, kind, n)
			}
		}
	}
}
