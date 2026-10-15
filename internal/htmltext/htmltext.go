// Package htmltext derives the plain-text alternative part of a
// multipart/alternative message from an HTML body (--html, --html-raw).
//
// Tags and comments are removed, the content of script, style and title
// elements is dropped (the title duplicates the subject), block-level
// elements become line breaks, whitespace runs collapse except inside
// <pre>, and entity references are decoded. Tokenizing is done by
// golang.org/x/net/html, which follows the HTML5 tokenizer rules for
// attributes, comments and raw-text elements (a quote or '>' inside an
// attribute, "</script>" inside a comment, invalid UTF-8 — none of them
// can swallow the rest of the text). It never fails, so even malformed
// HTML yields its readable text.
package htmltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Text converts HTML to plain text. Paragraph-level blocks are separated
// by a blank line, simpler breaks (list items, table cells, <br>) by a
// single newline. The result ends with exactly one newline; empty or
// markup-only input yields "". A leading byte order mark is ignored.
func Text(src string) string {
	src = strings.TrimPrefix(src, "\ufeff")
	s := &scanner{}
	s.scan(strings.ReplaceAll(src, "\r\n", "\n"))
	return clean(s.b.String())
}

// dropped elements carry no visible text for a message body. The
// tokenizer returns their content as raw text, which is discarded.
var dropped = map[string]bool{
	"iframe": true, "noembed": true, "noframes": true, "noscript": true,
	"script": true, "style": true, "title": true,
}

// par elements are separated from the surrounding text by a blank line.
var par = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"div": true, "dl": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"header": true, "main": true, "nav": true, "ol": true, "p": true,
	"pre": true, "section": true, "table": true, "tr": true, "ul": true,
}

// brk elements force a single line break.
var brk = map[string]bool{
	"br": true, "dd": true, "dt": true, "hr": true, "li": true,
	"td": true, "th": true,
}

// scanner accumulates the text of an HTML document in b, collapsing
// whitespace as it goes. Block newlines are not written immediately but
// held pending: consecutive boundaries (a closing </p> right before an
// opening <ul>) collapse into the wider one instead of stacking, and
// whitespace between them is dropped.
type scanner struct {
	b     strings.Builder
	nl    int    // pending block newlines: 1 = line break, 2 = blank line
	space bool   // whitespace seen since the last word
	line  bool   // the current output line has text
	pre   int    // depth of open <pre> elements
	skip  string // dropped element whose content is being skipped
}

// scan tokenizes src, writing text (entities decoded) to s.b while
// skipping markup. Comments, doctypes and processing instructions
// produce no text.
func (s *scanner) scan(src string) {
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return // io.EOF; a strings.Reader cannot fail otherwise
		}
		if tt == html.TextToken {
			if s.skip == "" {
				s.text(string(z.Text()))
			}
			continue
		}
		name, _ := z.TagName()
		tag := string(name)
		switch {
		case s.skip != "":
			if tt == html.EndTagToken && tag == s.skip {
				s.skip = ""
			}
		case tt == html.StartTagToken && dropped[tag]:
			s.skip = tag
		case tt == html.StartTagToken || tt == html.SelfClosingTagToken:
			if tag == "pre" && tt == html.StartTagToken {
				s.pre++
			}
			s.newline(tag)
		case tt == html.EndTagToken:
			if tag == "pre" && s.pre > 0 {
				s.pre--
			}
			s.newline(tag)
		}
	}
}

// text appends decoded text t. Outside <pre>, whitespace runs (source
// newlines included, per HTML) collapse into one space, and whitespace
// at a line start or block boundary is dropped. Inside <pre>, t is kept
// verbatim.
func (s *scanner) text(t string) {
	if s.pre > 0 {
		if s.nl > 0 {
			// HTML ignores a newline directly after <pre>.
			t = strings.TrimPrefix(t, "\n")
		}
		if t == "" {
			return
		}
		s.flush()
		s.b.WriteString(t)
		s.line = !strings.HasSuffix(t, "\n")
		s.space = false
		return
	}
	for t != "" {
		r, n := utf8.DecodeRuneInString(t)
		if unicode.IsSpace(r) {
			s.space = true
			t = t[n:]
			continue
		}
		end := strings.IndexFunc(t, unicode.IsSpace)
		if end < 0 {
			end = len(t)
		}
		s.flush()
		if s.space && s.line {
			s.b.WriteByte(' ')
		}
		s.b.WriteString(t[:end])
		s.line, s.space = true, false
		t = t[end:]
	}
}

// flush writes the pending block newlines, unless nothing precedes them.
func (s *scanner) flush() {
	if s.nl == 0 {
		return
	}
	if s.b.Len() > 0 {
		s.b.WriteString(strings.Repeat("\n", s.nl))
	}
	s.nl, s.line, s.space = 0, false, false
}

// newline records the boundary strength of the block element name.
func (s *scanner) newline(name string) {
	switch {
	case par[name]:
		s.nl = max(s.nl, 2)
	case brk[name]:
		s.nl = max(s.nl, 1)
	}
}

// clean strips trailing whitespace from every line and leading and
// trailing blank lines from the text, which then ends with exactly one
// newline. Whitespace-only input yields "". Blank-line runs can only
// come from <pre> content and are kept.
func clean(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	s = strings.Trim(strings.Join(lines, "\n"), "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}
