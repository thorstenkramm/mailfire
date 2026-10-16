package inliner

import (
	"errors"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/thorstenkramm/mailfire/internal/render"
)

// templateStyleBlock extracts the rendered document's main
// (non-conditional) <style> block, verbatim. The template files contain
// template actions, so the block is taken from the render output.
func templateStyleBlock(t *testing.T, doc string) string {
	t.Helper()
	// The main block is the <style> element starting the :root rule; the
	// other <style> element is the <!--[if mso]> conditional.
	start := strings.Index(doc, "<style>\n  :root")
	if start < 0 {
		t.Fatal("main <style> block not found")
	}
	rest := doc[start:]
	end := strings.Index(rest, "</style>")
	if end < 0 {
		t.Fatal("main <style> block has no closing </style>")
	}
	return rest[:end+len("</style>")]
}

// TestInlineBaseTemplate inlines a rendered base-template document and
// checks both halves of the FR-7 contract: content elements carry inlined
// style attributes, and the original <style> block survives byte-identical
// with the dark-mode and small-screen media queries untouched.
func TestInlineBaseTemplate(t *testing.T) {
	doc := render.Base(render.Data{
		Template: "plain-serif",
		Subject:  "Hi",
		Body:     `<h1>Hello</h1>` + "\n" + `<p>Para with <a href="https://example.com">link</a></p>` + "\n",
	})
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}

	for _, want := range []string{
		// inlined styles on content elements
		`<h1 style="margin:0 0 20px;font-size:30px;line-height:37px;font-weight:normal;letter-spacing:-0.01em">Hello</h1>`,
		`<p style="margin:0 0 18px">`,
		`<td class="content" style=`,
		`<a href="https://example.com" style="color:#7a2e1f;text-decoration:underline;text-underline-offset:2px">`,
		// the original <style> block survives, dark mode intact
		`  @media (prefers-color-scheme: dark) {`,
		`    .content a { color: #f0a58a !important; }`,
		`  @media screen and (max-width: 600px) {`,
		// Outlook conditionals survive
		`<!--[if mso]>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inlined output missing %q", want)
		}
	}

	// The original <style> block is byte-identical, not re-serialized.
	if !strings.Contains(out, templateStyleBlock(t, doc)) {
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

// TestInlineAuthorIgnoredStyle: a <style data-premailer="ignore"> written
// by the author (e.g. in an --html fragment) is honored — not inlined —
// and the attribute is kept on it and on any other element carrying it.
func TestInlineAuthorIgnoredStyle(t *testing.T) {
	doc := `<!DOCTYPE html>
<html><head><style>p { color: red; }</style></head>
<body><style data-premailer="ignore">p { margin: 0; }</style><p data-premailer="ignore">hi</p></body>
</html>`
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	for _, want := range []string{
		`<style>p { color: red; }</style>`,
		`<style data-premailer="ignore">p { margin: 0; }</style>`,
		`<p data-premailer="ignore" style="color:red">hi</p>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "<style"); n != 2 {
		t.Errorf("found %d <style> blocks, want 2:\n%s", n, out)
	}
}

// TestInlineAdversarialMarkup: uppercase tags, several style blocks,
// leftover-producing rules in each, marker-like text in attributes and
// comments, and malformed markup neither corrupt the document nor leave
// regenerated or duplicated <style> blocks behind.
func TestInlineAdversarialMarkup(t *testing.T) {
	doc := `<html><head><STYLE TYPE="text/css">P { color: red } @media (max-width: 1px) { P { color: blue } }</STYLE>
<style>a { color: green } a:hover { color: lime }</style></head>
<body><p title='<!--mailfire-head-end--> data-premailer="ignore"'>a</div></table>
<!--mailfire-head-end--><a>b</a>
<style>em { font-style: normal } :root { --x: 1 }</style><em>c</em></body></html>`
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	for _, want := range []string{
		`<style type="text/css">P { color: red } @media (max-width: 1px) { P { color: blue } }</style>`,
		`<style>a { color: green } a:hover { color: lime }</style>`,
		`<style>em { font-style: normal } :root { --x: 1 }</style>`,
		`<p title="&lt;!--mailfire-head-end--&gt; data-premailer=&#34;ignore&#34;" style="color:red">a`,
		`<!--mailfire-head-end--><a style="color:green">b</a>`,
		`<em style="font-style:normal">c</em>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "<style"); n != 3 {
		t.Errorf("found %d <style> blocks, want the 3 originals:\n%s", n, out)
	}
	if strings.Contains(out, "!important") {
		t.Errorf("regenerated leftover block survived:\n%s", out)
	}
}

// withTransform swaps the go-premailer call for the duration of a test.
func withTransform(t *testing.T, f func(*goquery.Document) error) {
	t.Helper()
	orig := transform
	transform = f
	t.Cleanup(func() { transform = orig })
}

// TestInlineLeftoverPlacementIndependent: if go-premailer put its
// regenerated leftover block somewhere else (here: the body) or left the
// consumed clones in place, they are still removed.
func TestInlineLeftoverPlacementIndependent(t *testing.T) {
	withTransform(t, func(d *goquery.Document) error {
		d.Find("body").PrependHtml(`<style type="text/css">p { color: blue !important; }</style>`)
		d.Find("p").SetAttr("style", "color:red")
		return nil
	})
	doc := `<!DOCTYPE html>
<html><head><style>p { color: red; } @media x { p { color: blue; } }</style></head>
<body><p>hi</p></body>
</html>`
	out, err := Inline(doc)
	if err != nil {
		t.Fatalf("Inline: %v", err)
	}
	if n := strings.Count(out, "<style"); n != 1 {
		t.Errorf("found %d <style> blocks, want only the original:\n%s", n, out)
	}
	if !strings.Contains(out, `<style>p { color: red; } @media x { p { color: blue; } }</style>`) ||
		strings.Contains(out, "data-premailer") {
		t.Errorf("original <style> block not restored verbatim:\n%s", out)
	}
}

// TestInlineStyleBlockLost: if go-premailer ever removed the ignored
// originals, Inline fails instead of returning a mail without its
// <style> block.
func TestInlineStyleBlockLost(t *testing.T) {
	withTransform(t, func(d *goquery.Document) error {
		d.Find("style").Remove()
		return nil
	})
	_, err := Inline(`<html><head><style>p { color: red; }</style></head><body><p>hi</p></body></html>`)
	if err == nil || !strings.Contains(err.Error(), "<style> block was lost") {
		t.Errorf("err = %v, want lost <style> block error", err)
	}
}

// TestInlineTransformError: a go-premailer failure is returned wrapped.
func TestInlineTransformError(t *testing.T) {
	boom := errors.New("boom")
	withTransform(t, func(*goquery.Document) error { return boom })
	_, err := Inline(`<html><head><style>p { color: red; }</style></head><body><p>hi</p></body></html>`)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "inlining CSS") {
		t.Errorf("err = %v, want wrapped transform error", err)
	}
}
