package markdown

import (
	"strings"
	"sync"
	"testing"
)

// noteSource is the README example body (mailfire --md).
const noteSource = `# This is an Email

It is written in markdown. It's delivered as HTML.

[mailfire](https://github.com/thorstenkramm/mailfire)
`

// TestHTMLNote renders the README example. The expected fragment is
// identical to the body the render package's note.<template>.html
// goldens carry.
func TestHTMLNote(t *testing.T) {
	want := `<h1>This is an Email</h1>
<p>It is written in markdown. It&rsquo;s delivered as HTML.</p>
<p><a href="https://github.com/thorstenkramm/mailfire">mailfire</a></p>
`
	if got := HTML(noteSource); got != want {
		t.Errorf("HTML(note) = %q, want %q", got, want)
	}
}

// TestHTMLConstructs covers every Markdown construct the base template
// styles: headings, emphasis, links, lists, code, blockquotes, tables,
// strikethrough and autolinks.
func TestHTMLConstructs(t *testing.T) {
	src := `# H1

## H2

Some **bold**, *italic* and ` + "`code`" + `, plus ~~gone~~.

- one
- two

1. first
2. second

` + "```go" + `
fmt.Println()
` + "```" + `

> quoted

| A | B |
|---|---|
| 1 | 2 |

A [link](https://example.com/page) and https://bare.example.

---
`
	got := HTML(src)
	for _, want := range []string{
		"<h1>H1</h1>",
		"<h2>H2</h2>",
		"<strong>bold</strong>",
		"<em>italic</em>",
		"<code>code</code>",
		"<del>gone</del>",
		"<ul>\n<li>one</li>\n<li>two</li>\n</ul>",
		"<ol>\n<li>first</li>\n<li>second</li>\n</ol>",
		`<pre><code class="language-go">fmt.Println()`,
		"<blockquote>\n<p>quoted</p>\n</blockquote>",
		"<table>",
		"<th>A</th>",
		"<td>1</td>",
		`<a href="https://example.com/page">link</a>`,
		`<a href="https://bare.example">https://bare.example</a>`, // linkify
		"<hr>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML output missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("HTML output does not end with a newline: %q", got)
	}
}

// TestTextNote derives the plain-text alternative of the README example:
// no Markdown markers, the link readable as "text <url>", and typographer
// entities resolved to real characters (no "&rsquo;").
func TestTextNote(t *testing.T) {
	want := `This is an Email

It is written in markdown. It’s delivered as HTML.

mailfire <https://github.com/thorstenkramm/mailfire>
`
	if got := Text(noteSource); got != want {
		t.Errorf("Text(note) = %q, want %q", got, want)
	}
}

// TestTextConstructs verifies the plain-text derivation keeps content and
// layout while stripping all Markdown markers.
func TestTextConstructs(t *testing.T) {
	src := `# Title

Some **bold**, *italic*, ` + "`code`" + ` and ~~gone~~.

- one
- two
  - nested

3. three
4. four

` + "```go" + `
fmt.Println("hi")
` + "```" + `

> quoted
> lines

| A | B |
|---|---|
| 1 | 2 |

A [link](https://example.com/page) and <https://bare.example/?a=1&b=2> and <ops@example.com>.

![logo alt](logo.png)

---

<div>raw html</div>
`
	got := Text(src)

	// Content survives.
	for _, want := range []string{
		"Title",
		"Some bold, italic, code and gone.",
		"- one\n- two\n  - nested",
		"3. three\n4. four",
		`fmt.Println("hi")`,
		"> quoted\n> lines",
		"A\tB\n1\t2",
		"link <https://example.com/page>",
		"https://bare.example/?a=1&b=2",
		"ops@example.com",
		"logo alt",
		strings.Repeat("—", 20),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text output missing %q:\n%s", want, got)
		}
	}

	// No Markdown markers survive.
	for _, marker := range []string{"#", "**", "~~", "`", "](", "![", "<div>", "<table", "| ---", "<pre"} {
		if strings.Contains(got, marker) {
			t.Errorf("text output contains Markdown marker %q:\n%s", marker, got)
		}
	}

	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Errorf("text output must end with exactly one newline: %q", got)
	}
}

// TestTextCodeBlocks covers indented code blocks and fenced blocks
// without a language tag.
func TestTextCodeBlocks(t *testing.T) {
	src := "before\n\n    indented <code>\n\n```\nfenced\n```\n"
	got := Text(src)
	for _, want := range []string{"indented <code>", "fenced"} {
		if !strings.Contains(got, want) {
			t.Errorf("text output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "```") {
		t.Errorf("text output contains fence markers:\n%s", got)
	}
}

// TestTextCodeBlockNoTrailingNewline covers an unterminated fence at
// EOF: the last line has no newline in the source and still ends the
// output line.
func TestTextCodeBlockNoTrailingNewline(t *testing.T) {
	if got := Text("```\nno newline at eof"); got != "no newline at eof\n" {
		t.Errorf("Text = %q, want %q", got, "no newline at eof\n")
	}
}

// TestTextLinks covers the link variants: a link whose text is the URL
// stays bare, an empty-text link renders just its destination.
func TestTextLinks(t *testing.T) {
	src := "[https://example.com](https://example.com)\n\n[](https://empty.example)\n"
	got := Text(src)
	if !strings.Contains(got, "https://example.com\n") {
		t.Errorf("bare link missing:\n%s", got)
	}
	if strings.Contains(got, "https://example.com <https://example.com>") {
		t.Errorf("URL-only link rendered redundantly:\n%s", got)
	}
	if !strings.Contains(got, "https://empty.example") {
		t.Errorf("empty-text link destination missing:\n%s", got)
	}
}

// TestTextEscapes covers backslash escapes and entity references, which
// must be resolved in the text part except inside code spans, where they
// are literal.
func TestTextEscapes(t *testing.T) {
	src := "a \\*b\\* &amp; &copy; &#169; `x &lt; y` [l](https://e.example/?a=1&amp;b=2)\n"
	want := "a *b* & © © x &lt; y l <https://e.example/?a=1&b=2>\n"
	if got := Text(src); got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

// TestCRLF verifies CRLF input yields LF-only output in both parts.
func TestCRLF(t *testing.T) {
	src := "# H\r\n\r\ntext\r\n\r\n```\r\ncode\r\n```\r\n"
	if got := HTML(src); strings.Contains(got, "\r") {
		t.Errorf("HTML contains CR: %q", got)
	}
	if got, want := Text(src), "H\n\ntext\n\ncode\n"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

// TestUnsafeInput pins goldmark's safe mode: raw HTML is omitted from the
// HTML part and javascript: links lose their target in both parts.
func TestUnsafeInput(t *testing.T) {
	src := "<script>alert(1)</script>\n\nhi <img src=x onerror=alert(1)> [x](javascript:alert(1))\n"
	got := HTML(src)
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("HTML contains %q: %s", bad, got)
		}
	}
	if got, want := Text(src), "hi  x\n"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

// TestTextEmpty keeps even degenerate input well-formed: one newline,
// no crash.
func TestTextEmpty(t *testing.T) {
	if got := Text(""); got != "\n" {
		t.Errorf("Text(\"\") = %q, want %q", got, "\n")
	}
}

// TestConcurrent exercises the shared goldmark instance from goroutines;
// run with -race to catch parser/renderer state leaks.
func TestConcurrent(t *testing.T) {
	wantHTML := HTML(noteSource)
	wantText := Text(noteSource)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := HTML(noteSource); got != wantHTML {
				t.Error("concurrent HTML render differs")
			}
			if got := Text(noteSource); got != wantText {
				t.Error("concurrent text render differs")
			}
		}()
	}
	wg.Wait()
}
