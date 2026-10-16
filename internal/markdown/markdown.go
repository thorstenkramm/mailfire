// Package markdown converts Markdown message bodies (--md) to the two
// parts of a multipart/alternative message: an HTML fragment for the
// base template's Body and a plain-text alternative derived from the
// same source.
//
// Rendering uses goldmark with CommonMark plus GFM-style tables,
// strikethrough and autolinks (extension.Linkify), and the typographer
// extension for smart punctuation — the design preview
// (tmp/preview/note.html) renders the README example with “It&rsquo;s”.
//
// goldmark runs in its default safe mode (no html.WithUnsafe): raw HTML in
// the Markdown source is replaced by "<!-- raw HTML omitted -->" and links
// with dangerous schemes (javascript:, vbscript:, file:, non-image data:)
// lose their href. Markdown input therefore never injects markup into the
// base template; callers wanting raw HTML use --html or --html-raw.
// Input is normalized to LF line endings before parsing.
package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// md is the shared renderer. goldmark instances are safe for concurrent
// use, and parsing produces a fresh AST per call.
var md = goldmark.New(goldmark.WithExtensions(
	extension.Table,
	extension.Strikethrough,
	extension.Linkify,
	extension.Typographer,
))

// HTML renders the Markdown source src to an HTML fragment, ready to be
// wrapped by render.Base. The fragment ends with a newline.
//
// There is no error return: conversion writes to a bytes.Buffer, which
// cannot fail (same reasoning as render.Base).
func HTML(src string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(normalize(src)), &buf); err != nil {
		panic(err) // Built-in renderers only return writer errors; bytes.Buffer cannot fail.
	}
	return buf.String()
}

// Text derives the plain-text alternative part from the Markdown source:
// formatting markers are stripped, links stay readable as "text <url>",
// and lists, quotes and tables keep a simple text layout. The result
// ends with exactly one newline.
func Text(src string) string {
	src = normalize(src)
	doc := md.Parser().Parse(text.NewReader([]byte(src)))
	w := &writer{src: []byte(src)}
	w.blocks(doc)
	return strings.TrimRight(w.buf.String(), "\n") + "\n"
}

// normalize converts CRLF line endings to LF. goldmark keeps the \r in
// raw segments (code blocks), which would leak into both parts.
func normalize(src string) string {
	return strings.ReplaceAll(src, "\r\n", "\n")
}

// unescape resolves backslash escapes and entity references in a source
// segment exactly as goldmark's HTML writer does, then decodes the
// resulting HTML back to plain text.
func unescape(b []byte) string {
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	gmhtml.DefaultWriter.Write(bw, b)
	if err := bw.Flush(); err != nil {
		panic(err) // The underlying bytes.Buffer cannot fail.
	}
	return html.UnescapeString(buf.String())
}

// writer renders an AST as plain text into buf. In compact mode (tight
// lists) blocks are separated by a single newline instead of a blank
// line.
type writer struct {
	src     []byte
	buf     bytes.Buffer
	compact bool
}

// blocks renders the block children of n, separating blocks with a
// blank line.
func (w *writer) blocks(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.blank()
		w.block(c)
	}
}

// blank pads the buffer to end in a blank line. It does nothing at the
// start of the output.
func (w *writer) blank() {
	if w.buf.Len() == 0 {
		return
	}
	if !bytes.HasSuffix(w.buf.Bytes(), []byte("\n")) {
		w.buf.WriteByte('\n')
	}
	if w.compact {
		return
	}
	if !bytes.HasSuffix(w.buf.Bytes(), []byte("\n\n")) {
		w.buf.WriteByte('\n')
	}
}

// block renders one block-level node, ending its output with a newline.
func (w *writer) block(n ast.Node) {
	switch n := n.(type) {
	case *ast.Heading:
		w.inline(n)
		w.buf.WriteByte('\n')
	case *ast.Paragraph, *ast.TextBlock:
		w.inline(n)
		w.buf.WriteByte('\n')
	case *ast.FencedCodeBlock:
		w.code(n.Lines())
	case *ast.CodeBlock:
		w.code(n.Lines())
	case *ast.Blockquote:
		inner := &writer{src: w.src}
		inner.blocks(n)
		for _, line := range strings.SplitAfter(strings.TrimRight(inner.buf.String(), "\n"), "\n") {
			w.buf.WriteString("> ")
			w.buf.WriteString(line)
		}
	case *ast.List:
		w.list(n)
	case *ast.ListItem:
		// Reached only for a malformed AST (lists contain their items);
		// render the content without a marker rather than dropping it.
		w.blocks(n)
	case *ast.HTMLBlock:
		// Raw HTML tags are noise in a text part; the HTML part carries
		// the real content.
	case *ast.ThematicBreak:
		w.buf.WriteString(strings.Repeat("—", 20))
		w.buf.WriteByte('\n')
	case *extast.Table:
		for row := n.FirstChild(); row != nil; row = row.NextSibling() {
			w.tableRow(row)
		}
	default:
		// Unknown block: keep whatever inline content it has.
		w.inline(n)
		w.buf.WriteByte('\n')
	}
}

// code writes the raw lines of a code block. The trailing-newline guard
// keeps the "every block ends with \n" invariant even for a final line
// without one (only possible at EOF, where Text would add it anyway).
func (w *writer) code(lines *text.Segments) {
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		w.buf.Write(seg.Value(w.src))
	}
	if !bytes.HasSuffix(w.buf.Bytes(), []byte("\n")) {
		w.buf.WriteByte('\n')
	}
}

// list writes a bullet or numbered list, indenting wrapped and nested
// content to the marker width.
func (w *writer) list(n *ast.List) {
	i := 0
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		i++
		marker := "- "
		if n.IsOrdered() {
			marker = fmt.Sprintf("%d. ", n.Start+i-1)
		}
		inner := &writer{src: w.src, compact: n.IsTight}
		inner.blocks(item)
		for j, line := range strings.Split(strings.TrimRight(inner.buf.String(), "\n"), "\n") {
			if j > 0 {
				w.buf.WriteString(strings.Repeat(" ", len(marker)))
			} else {
				w.buf.WriteString(marker)
			}
			w.buf.WriteString(line)
			w.buf.WriteByte('\n')
		}
	}
}

// tableRow writes one table row, cells separated by tabs. Pipes are
// deliberately not used: they read as leftover Markdown markers.
func (w *writer) tableRow(row ast.Node) {
	first := true
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		if !first {
			w.buf.WriteByte('\t')
		}
		first = false
		w.inline(cell)
	}
	w.buf.WriteByte('\n')
}

// inline writes the inline content of n with all formatting markers
// stripped.
func (w *writer) inline(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			// Raw text (code spans) is literal; everything else may
			// carry backslash escapes and entity references.
			if c.IsRaw() {
				w.buf.Write(c.Segment.Value(w.src))
			} else {
				w.buf.WriteString(unescape(c.Segment.Value(w.src)))
			}
			if c.HardLineBreak() || c.SoftLineBreak() {
				w.buf.WriteByte('\n')
			}
		case *ast.String:
			// String nodes come from the typographer extension and hold
			// HTML entities (&rsquo; and friends); unescape them for the
			// text part. Source text arrives as Text nodes instead.
			w.buf.WriteString(html.UnescapeString(string(c.Value)))
		case *ast.Link:
			w.link(c)
		case *ast.AutoLink:
			// Autolinks display as the URL itself; keep it bare.
			w.buf.Write(c.Label(w.src))
		case *ast.Image:
			// The alt text is the readable description; the image file
			// itself goes into the HTML part as an inline cid: part.
			w.inline(c)
		default:
			// Emphasis, strikethrough, code spans and friends: recurse
			// to keep the text without the markers. Inline raw HTML has
			// no children and is dropped.
			w.inline(c)
		}
	}
}

// link writes "text <url>" so the target survives without Markdown
// syntax. Links whose text already is the URL stay bare. Dangerous
// targets (javascript: and friends) are dropped, matching the HTML part.
func (w *writer) link(n *ast.Link) {
	inner := &writer{src: w.src}
	inner.inline(n)
	label := inner.buf.String()
	if gmhtml.IsDangerousURL(n.Destination) {
		w.buf.WriteString(label)
		return
	}
	dest := unescape(n.Destination)
	if label == "" || label == dest {
		w.buf.WriteString(dest)
		return
	}
	w.buf.WriteString(label)
	w.buf.WriteString(" <")
	w.buf.WriteString(dest)
	w.buf.WriteString(">")
}
