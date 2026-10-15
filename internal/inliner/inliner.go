// Package inliner inlines the CSS of a rendered HTML mail document into
// element style attributes, so mail clients that strip <style> blocks
// still render the base template's typography (FR-7).
//
// Inlining is done with go-premailer (PRD §4). go-premailer removes every
// <style> element it reads and re-serializes the rules it could not
// inline (media queries, :root, ...) into a freshly generated <style>
// block, reformatting them and forcing !important. That is not acceptable
// here: the base template's <style> block must survive untouched, because
// the dark-mode and small-screen media queries cannot be inlined and
// clients that do support <style> need the original rules (see the header
// comment of templates/base.html).
//
// Inline therefore wraps go-premailer: before inlining, every <style>
// element is duplicated — the original is marked data-premailer="ignore"
// (go-premailer neither reads nor removes ignored elements) and the
// unmarked clone is what go-premailer consumes — and a marker comment is
// appended to <head>. After the transform, the marker and the regenerated
// leftover <style> block following it are cut out and the marker
// attributes are stripped, leaving the original <style> blocks
// byte-identical in the output.
package inliner

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/vanng822/go-premailer/premailer"
)

// headEnd marks the end of <head> while go-premailer runs: the leftover
// <style> block it generates is appended right after the marker, which
// makes it trivial to find and drop afterwards. It never appears in the
// returned document.
const headEnd = "<!--mailfire-head-end-->"

// Inline inlines the CSS of doc's <style> elements into style attributes
// and returns the resulting document. The original <style> blocks stay in
// place byte-identical. A document without <style> elements is returned
// unchanged.
//
// The output is normalized by the HTML parser go-premailer builds on:
// void elements are self-closed, tables gain <tbody>, entities may be
// re-encoded. Byte-identity is only guaranteed for documents without
// <style> elements.
func Inline(doc string) (string, error) {
	d, err := goquery.NewDocumentFromReader(strings.NewReader(doc))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}
	if d.Find("style").Length() == 0 {
		// Nothing to inline: return the input untouched instead of a
		// normalized re-serialization.
		return doc, nil
	}
	d.Find("style").Each(func(_ int, s *goquery.Selection) {
		// go-premailer skips style elements with a media attribute other
		// than "all" entirely (no inlining, no removal): leave them alone.
		if media, ok := s.Attr("media"); ok && media != "all" {
			return
		}
		// Keep the original: mark it as ignored and let go-premailer
		// consume an unmarked clone instead.
		s.SetAttr("data-premailer", "ignore")
		s.AfterSelection(s.Clone().RemoveAttr("data-premailer"))
	})
	d.Find("head").AppendHtml(headEnd)

	out, err := premailer.NewPremailer(d, premailer.NewOptions()).Transform()
	if err != nil {
		return "", fmt.Errorf("inlining CSS: %w", err)
	}
	// Drop the marker and the regenerated leftover <style> block between
	// it and </head>. Without a <head> go-premailer has nowhere to put
	// leftover rules, so there is nothing to cut.
	if i := strings.Index(out, headEnd); i >= 0 {
		rest := out[i+len(headEnd):]
		if j := strings.Index(rest, "</head>"); j >= 0 {
			out = out[:i] + rest[j:]
		}
	}
	return strings.ReplaceAll(out, ` data-premailer="ignore"`, ""), nil
}
