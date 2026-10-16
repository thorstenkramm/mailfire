// Package render renders one of the embedded email templates
// (templates/card.html, templates/plain.html) around an HTML message body.
//
// There are four templates, two layouts each in a sans-serif and a serif
// variant: "card" (the default), "card-serif", "plain" and "plain-serif".
// The layout fixes the frame, the variant the fonts, colors and the look of
// GFM alerts (see look).
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

// DefaultTemplate is used when neither the config nor --template names
// one.
const DefaultTemplate = "card"

// Names lists the template names in documentation order.
var Names = []string{"card", "card-serif", "plain", "plain-serif"}

// Valid reports whether name is a known template.
func Valid(name string) bool {
	_, ok := looks[name]
	return ok
}

// tmpl is parsed once at init. The templates define the escaping
// contract: {{html .Lang}}, {{html .Subject}} and {{html .Preheader}}
// escape plain text, {{.Body}} and {{.Footer}} insert HTML as is.
// text/template has no html func, so it is registered here.
var tmpl = template.Must(template.New("").
	Funcs(template.FuncMap{"html": html.EscapeString}).
	ParseFS(templates.FS, "*.html"))

// HTML is a fragment of trusted markup inserted into the template as is.
// It is a distinct type so plain text cannot be passed as Body or Footer
// by accident: a string variable needs an explicit HTML(...) conversion,
// which marks the spot where the caller vouches for the content. Never
// convert user-controlled plain text; escape it with html.EscapeString
// first.
type HTML string

// Data are the fields the templates expect.
type Data struct {
	// Template is the template name (see Names). Empty selects
	// DefaultTemplate.
	Template string
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

// Base renders the template d.Template with d and returns the complete
// HTML document. An unknown template name is a programming error (callers
// validate names with Valid) and panics.
//
// There is no error return: the templates parse at package init
// (template.Must) and rendering writes to a bytes.Buffer, which cannot
// fail, so a render-time error is impossible.
func Base(d Data) string {
	name := d.Template
	if name == "" {
		name = DefaultTemplate
	}
	lk, ok := looks[name]
	if !ok {
		panic("render: unknown template " + name)
	}
	var buf bytes.Buffer
	// With these fixed templates and data types, an execution failure is a
	// programming error. Do not silently return a partially rendered email.
	if err := tmpl.ExecuteTemplate(&buf, lk.layout+".html", view{Data: d, S: lk}); err != nil {
		panic(err)
	}
	return buf.String()
}

// view is what the templates see: the message data plus the look.
type view struct {
	Data
	S look
}

// look holds everything that differs between the templates sharing a
// layout. Values are CSS, inserted into the templates as is.
type look struct {
	layout string // "card" or "plain": the layout file

	Font, MsoFont        string // font stacks; MsoFont is for Outlook, which uses the first font only
	Size, LineHeight     string // body text
	H1Size, H1LineHeight string
	H1Weight             string
	HeadingWeight        string // h2..h6
	Text, Muted          string // body and footer text colors
	Link, LinkDark       string // link colors, light and dark mode
	Page, DarkPage       string // page background, light and dark mode
	Radius               string // card corner radius
	MobileGutter         string // page padding on small screens

	AlertBorder  string // width of the alert's left border
	AlertPadding string
	AlertTint    bool   // tinted alert background
	AlertTitle   string // extra declarations for the alert title
	Alerts       []alertColors
}

// alertColors are the colors of one GFM alert type, following GitHub's.
type alertColors struct {
	Kind                string
	Border, Title, Tint string
	DarkTitle, DarkTint string
}

// Font stacks. Each starts with fonts preinstalled on macOS/iOS, Windows,
// Android and common Linux desktops; Arial and Times New Roman are only
// the last-resort fallbacks.
const (
	sansStack  = "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', 'Noto Sans', 'Liberation Sans', Arial, sans-serif"
	serifStack = "Charter, 'Bitstream Charter', 'Sitka Text', Cambria, Georgia, 'Times New Roman', serif"
	sansMso    = "'Segoe UI', Arial, sans-serif"
	serifMso   = "Cambria, Georgia, serif"
)

var alertPalette = []alertColors{
	{Kind: "note", Border: "#0969da", Title: "#0969da", Tint: "#eef5fd", DarkTitle: "#4493f8", DarkTint: "#12233a"},
	{Kind: "tip", Border: "#1a7f37", Title: "#1a7f37", Tint: "#edf7f0", DarkTitle: "#3fb950", DarkTint: "#12281a"},
	{Kind: "important", Border: "#8250df", Title: "#8250df", Tint: "#f4f0fd", DarkTitle: "#ab7df8", DarkTint: "#241a38"},
	{Kind: "warning", Border: "#9a6700", Title: "#9a6700", Tint: "#fbf5e6", DarkTitle: "#d29922", DarkTint: "#2e2510"},
	{Kind: "caution", Border: "#d1242f", Title: "#c21f2a", Tint: "#fdf0f0", DarkTitle: "#f85149", DarkTint: "#331719"},
}

func sans(lk look) look {
	lk.Font, lk.MsoFont = sansStack, sansMso
	lk.Size, lk.LineHeight = "16px", "26px"
	lk.H1Size, lk.H1LineHeight, lk.H1Weight, lk.HeadingWeight = "26px", "33px", "600", "600"
	lk.Text, lk.Muted = "#1f2328", "#5b616b"
	lk.Link, lk.LinkDark = "#2b4c7e", "#8ab4f8"
	lk.Alerts = alertPalette
	return lk
}

func serif(lk look) look {
	lk.Font, lk.MsoFont = serifStack, serifMso
	lk.Size, lk.LineHeight = "17px", "28px"
	lk.H1Size, lk.H1LineHeight, lk.H1Weight, lk.HeadingWeight = "30px", "37px", "normal", "bold"
	lk.Text, lk.Muted = "#24252a", "#62656d"
	lk.Link, lk.LinkDark = "#7a2e1f", "#f0a58a"
	lk.Alerts = alertPalette
	return lk
}

var looks = map[string]look{
	"card": sans(look{
		layout: "card", Page: "#edeef0", DarkPage: "#111214", Radius: "6px", MobileGutter: "24px 8px 32px 8px",
		AlertBorder: "4px", AlertPadding: "12px 16px", AlertTint: true,
		AlertTitle: "font-size: 15px; font-weight: 600;",
	}),
	"card-serif": serif(look{
		layout: "card", Page: "#eceae6", DarkPage: "#111214", Radius: "0", MobileGutter: "24px 8px 32px 8px",
		AlertBorder: "3px", AlertPadding: "2px 0 2px 18px",
		AlertTitle: "font-size: 12px; letter-spacing: 0.14em; text-transform: uppercase;",
	}),
	"plain": sans(look{
		layout: "plain", Page: "#ffffff", DarkPage: "#16171a", MobileGutter: "28px 18px 36px",
		AlertBorder: "4px", AlertPadding: "4px 0 4px 16px",
		AlertTitle: "font-weight: 600;",
	}),
	"plain-serif": serif(look{
		layout: "plain", Page: "#ffffff", DarkPage: "#16171a", MobileGutter: "28px 18px 36px",
		AlertBorder: "2px", AlertPadding: "2px 0 2px 18px",
		AlertTitle: "font-style: italic;",
	}),
}
