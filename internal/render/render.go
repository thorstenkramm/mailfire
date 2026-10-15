// Package render renders the embedded base template
// (templates/base.html) around an HTML message body.
//
// Rendering uses text/template, never html/template: html/template strips
// HTML comments, which would remove the <!--[if mso]> blocks Outlook
// needs. Escaping is therefore done explicitly and only where user
// controlled plain text lands in markup: the template's html func escapes
// Lang, Subject and Preheader, while Body and Footer are trusted HTML
// (type HTML) passed through unescaped by design.
package render

import (
	"bytes"
	"html"
	"text/template"

	"github.com/thorstenkramm/mailfire/templates"
)

// base is parsed once at init. The template defines the escaping contract:
// {{html .Lang}}, {{html .Subject}} and {{html .Preheader}} escape plain
// text, {{.Body}}
// and {{.Footer}} insert HTML as is. text/template has no html func, so it
// is registered here.
var base = template.Must(template.New("base.html").
	Funcs(template.FuncMap{"html": html.EscapeString}).
	ParseFS(templates.FS, "base.html"))

// HTML is a fragment of trusted markup inserted into the template as is.
// It is a distinct type so plain text cannot be passed as Body or Footer
// by accident: a string variable needs an explicit HTML(...) conversion,
// which marks the spot where the caller vouches for the content. Never
// convert user-controlled plain text; escape it with html.EscapeString
// first.
type HTML string

// Data are the fields templates/base.html expects.
type Data struct {
	// Lang is the message language tag, e.g. "de", escaped by the
	// template. Empty defaults to "en".
	Lang string
	// Subject is plain text, escaped by the template.
	Subject string
	// Preheader is optional plain text shown as inbox preview, escaped by
	// the template. The hidden preview block is omitted when empty.
	Preheader string
	// Body is HTML from the Markdown renderer or --html input. It is
	// inserted as is and must never be escaped.
	Body HTML
	// Footer is optional HTML, inserted as is. The footer block is omitted
	// when empty.
	Footer HTML
}

// Base renders the base template with d and returns the complete HTML
// document.
//
// There is no error return: the template parses at package init
// (template.Must) and rendering writes to a bytes.Buffer, which cannot
// fail, so a render-time error is impossible.
func Base(d Data) string {
	var buf bytes.Buffer
	// Execute can only fail on writer errors, and bytes.Buffer never
	// fails; parse errors were already caught at init by template.Must.
	_ = base.Execute(&buf, d)
	return buf.String()
}
