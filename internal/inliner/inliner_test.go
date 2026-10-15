package inliner

import (
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/render"
	"github.com/thorstenkramm/mailfire/templates"
)

// templateStyleBlock extracts the base template's (non-conditional)
// <style> block, verbatim, from the embedded template source.
func templateStyleBlock(t *testing.T) string {
	t.Helper()
	src, err := templates.FS.ReadFile("base.html")
	if err != nil {
		t.Fatalf("reading base template: %v", err)
	}
	// The main block is the <style> element starting the :root rule; the
	// other <style> occurrences in the source are the header comment and
	// the <!--[if mso]> conditional.
	start := strings.Index(string(src), "<style>\n  :root")
	if start < 0 {
		t.Fatal("base template main <style> block not found")
	}
	rest := string(src)[start:]
	end := strings.Index(rest, "</style>")
	if end < 0 {
		t.Fatal("base template has no closing </style>")
	}
	return rest[:end+len("</style>")]
}

// TestInlineBaseTemplate inlines a rendered base-template document and
// checks both halves of the FR-7 contract: content elements carry inlined
// style attributes, and the original <style> block survives byte-identical
// with the dark-mode and small-screen media queries untouched.
func TestInlineBaseTemplate(t *testing.T) {
	doc := render.Base(render.Data{
		Subject: "Hi",
		Body:    `<h1>Hello</h1>` + "\n" + `<p>Para with <a href="https://example.com">link</a></p>` + "\n",
	})
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}

	for _, want := range []string{
		// inlined styles on content elements
		`<h1 style="font-weight:bold;margin:0 0 20px;font-size:30px;line-height:37px;letter-spacing:-0.01em">Hello</h1>`,
		`<p style="margin:0 0 18px">`,
		`<td class="content" style=`,
		`<a href="https://example.com" style="color:#b23b12;text-decoration:underline;text-underline-offset:2px">`,
		// the original <style> block survives, dark mode intact
		`  @media (prefers-color-scheme: dark) {`,
		`    .content a { color: #ff8c5a !important; }`,
		`  @media screen and (max-width: 600px) {`,
		// Outlook conditionals survive
		`<!--[if mso]>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inlined output missing %q", want)
		}
	}

	// The original <style> block is byte-identical, not re-serialized.
	if !strings.Contains(out, templateStyleBlock(t)) {
		t.Error("inlined output does not contain the original <style> block verbatim")
	}

	// No inliner artifacts and no duplicated style blocks: the mso
	// conditional block plus the template block, nothing else.
	for _, bad := range []string{"data-premailer", "mailfire-head-end"} {
		if strings.Contains(out, bad) {
			t.Errorf("inlined output contains artifact %q", bad)
		}
	}
	if n := strings.Count(out, "<style"); n != 2 {
		t.Errorf("found %d <style> blocks, want 2 (mso conditional + template)", n)
	}
}

// TestInlineNoStyleBlock returns a document without <style> elements
// unchanged, byte-identical.
func TestInlineNoStyleBlock(t *testing.T) {
	doc := "<!DOCTYPE html>\n<html><body><p>no styles here &amp; none needed</p></body></html>\n"
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	if out != doc {
		t.Errorf("output not byte-identical:\ngot  %q\nwant %q", out, doc)
	}
}

// TestInlineBodyStyleBlock: a <style> element inside the body (e.g. from
// an --html fragment) is inlined as well and also survives in place.
func TestInlineBodyStyleBlock(t *testing.T) {
	doc := `<!DOCTYPE html>
<html><head><title>s</title></head>
<body><style>.note { color: green; }</style><p class="note">hi</p></body>
</html>`
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	if !strings.Contains(out, `<p class="note" style="color:green">hi</p>`) {
		t.Errorf("body style not inlined:\n%s", out)
	}
	if n := strings.Count(out, "<style"); n != 1 {
		t.Errorf("found %d <style> blocks, want 1 (the original)", n)
	}
	if !strings.Contains(out, `<style>.note { color: green; }</style>`) {
		t.Errorf("original body <style> block not kept verbatim:\n%s", out)
	}
}

// TestInlineMediaStyleUntouched: <style media="print"> is neither inlined
// nor duplicated — go-premailer skips media-specific blocks entirely.
func TestInlineMediaStyleUntouched(t *testing.T) {
	doc := `<!DOCTYPE html>
<html><head><style media="print">p { color: black; }</style></head>
<body><p>hi</p></body>
</html>`
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	if strings.Contains(out, `<p style=`) {
		t.Errorf("media-specific rule was inlined:\n%s", out)
	}
	if n := strings.Count(out, `<style media="print">`); n != 1 {
		t.Errorf("found %d print style blocks, want exactly 1:\n%s", n, out)
	}
}

// TestInlineUserStyleAttrWins: declarations from an element's own style
// attribute take precedence over inlined stylesheet rules (the --html
// inheritance contract).
func TestInlineUserStyleAttrWins(t *testing.T) {
	doc := `<!DOCTYPE html>
<html><head><style>p { color: red; margin: 0; }</style></head>
<body><p style="color: blue;">hi</p></body>
</html>`
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	// The element's own declarations are parsed, normalized and appended
	// after the stylesheet rules, so they win the cascade.
	if !strings.Contains(out, `<p style="margin:0;color:blue">hi</p>`) {
		t.Errorf("element style attribute lost or overridden:\n%s", out)
	}
}
