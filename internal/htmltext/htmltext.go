// Package htmltext derives the plain-text alternative part of a
// multipart/alternative message from an HTML body (--html, --html-raw).
//
// Tags and comments are removed, the content of script, style and title
// elements is dropped (the title duplicates the subject), block-level
// elements become line breaks, runs of whitespace are collapsed, and
// entity references are decoded with html.UnescapeString — the same
// entity-decoding machinery the markdown package's plain-text writer
// uses. The scanner is deliberately lenient: it never fails, so even
// malformed HTML yields its readable text.
package htmltext

import (
	"html"
	"strings"
)

// Text converts HTML to plain text. Paragraph-level blocks are separated
// by a blank line, simpler breaks (list items, table cells, <br>) by a
// single newline. The result ends with exactly one newline; empty or
// markup-only input yields "".
func Text(src string) string {
	s := &scanner{}
	s.scan(strings.ReplaceAll(src, "\r\n", "\n"))
	return clean(s.b.String())
}

// rawContent elements carry no visible text for a message body; their
// content up to the closing tag is dropped.
var rawContent = map[string]bool{"script": true, "style": true, "title": true}

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

// scanner accumulates the text of an HTML document in b. Block newlines
// are not written immediately but held pending: consecutive boundaries
// (a closing </p> right before an opening <ul>) collapse into the wider
// one instead of stacking, and whitespace-only text between them is
// dropped.
type scanner struct {
	b  strings.Builder
	nl int // pending block newlines: 1 = line break, 2 = blank line
}

// scan walks src, writing text (entities decoded) to s.b while skipping
// markup.
func (s *scanner) scan(src string) {
	for len(src) > 0 {
		i := strings.IndexByte(src, '<')
		if i < 0 {
			s.text(src)
			return
		}
		s.text(src[:i])
		src = src[i+1:] // after '<'
		switch {
		case strings.HasPrefix(src, "!--"):
			// Comment: skip to "-->" (an unterminated comment
			// consumes the rest of the input).
			end := strings.Index(src, "-->")
			if end < 0 {
				return
			}
			src = src[end+3:]
		case strings.HasPrefix(src, "!") || strings.HasPrefix(src, "?"):
			// Doctype or processing instruction.
			src = skipTag(src)
		case strings.HasPrefix(src, "/"):
			name, rest := tagName(src[1:])
			s.newline(name)
			src = skipTag(rest)
		default:
			name, rest := tagName(src)
			if name == "" {
				// Stray '<' not starting a tag: keep it as text.
				s.text("<")
				continue
			}
			src = skipTag(rest)
			if rawContent[name] {
				src = skipRaw(src, name)
			} else {
				s.newline(name)
			}
		}
	}
}

// text appends t with entity references decoded and source newlines
// treated as spaces (HTML semantics), flushing pending block newlines
// first. Whitespace-only text is dropped at a block boundary and kept
// (as spacing between inline elements) elsewhere.
func (s *scanner) text(t string) {
	t = html.UnescapeString(strings.ReplaceAll(t, "\n", " "))
	if strings.TrimSpace(t) == "" {
		if s.nl == 0 {
			s.b.WriteString(t)
		}
		return
	}
	if s.nl > 0 && s.b.Len() > 0 {
		s.b.WriteString(strings.Repeat("\n", s.nl))
	}
	s.nl = 0
	s.b.WriteString(t)
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

// tagName reads a tag name at the start of s (just after '<' or "</")
// and returns it lowercased, plus the rest of s. It returns "" when s
// does not start with an ASCII letter.
func tagName(s string) (name, rest string) {
	i := 0
	for i < len(s) && isNameChar(s[i]) {
		i++
	}
	return strings.ToLower(s[:i]), s[i:]
}

func isNameChar(c byte) bool {
	return c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// skipTag returns s advanced past the next '>', honoring quoted attribute
// values (a '>' inside quotes does not end the tag). An unterminated tag
// consumes the rest of the input.
func skipTag(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		switch {
		case quote != 0:
			if s[i] == quote {
				quote = 0
			}
		case s[i] == '"' || s[i] == '\'':
			quote = s[i]
		case s[i] == '>':
			return s[i+1:]
		}
	}
	return ""
}

// skipRaw returns s advanced past the closing tag of the raw-content
// element name; a missing closing tag consumes the rest of the input.
func skipRaw(s, name string) string {
	i := strings.Index(strings.ToLower(s), "</"+name)
	if i < 0 {
		return ""
	}
	return skipTag(s[i+2+len(name):])
}

// clean normalizes whitespace: leading/trailing and repeated inner
// whitespace per line collapses, blank-line runs collapse to one blank
// line, and the result ends with exactly one newline. Whitespace-only
// input yields "".
func clean(s string) string {
	var b strings.Builder
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank = true
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
			if blank {
				b.WriteByte('\n')
			}
		}
		blank = false
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "\n"
}
