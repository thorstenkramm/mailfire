// Package templates holds the email templates shipped in the binary.
//
// The files live in this top-level directory so they are easy to edit and
// preview; embed patterns cannot reference files outside their own
// directory, so the embedding happens here.
package templates

import "embed"

// FS contains the email templates: the layouts card.html and plain.html
// and their shared parts in head.html, plus the job report content
// template job-report.md of "mailfire run". Render them with
// text/template, never html/template — html/template strips the
// <!--[if mso]> comments Outlook needs.
//
//go:embed card.html plain.html head.html job-report.md
var FS embed.FS
