// Package templates holds the email templates shipped in the binary.
//
// The files live in this top-level directory so they are easy to edit and
// preview; embed patterns cannot reference files outside their own
// directory, so the embedding happens here.
package templates

import "embed"

// FS contains base.html, the email base template. Render it with
// text/template, never html/template — html/template strips the
// <!--[if mso]> comments Outlook needs.
//
//go:embed base.html
var FS embed.FS
