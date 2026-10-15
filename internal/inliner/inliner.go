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
// Inline therefore wraps go-premailer, working on the parsed document
// only (no string surgery on the serialized HTML): before inlining, every
// <style> element is duplicated — the original is marked
// data-premailer="ignore" (go-premailer neither reads nor removes ignored
// elements) and the unmarked clone is what go-premailer consumes. After
// the transform, every <style> element that was not in the input — the
// regenerated leftover block, wherever go-premailer put it, and any clone
// it did not consume — is removed, and the marker attribute is taken off
// the originals again, leaving them byte-identical in the output.
//
// Known limitation: go-premailer does not count pseudo-classes when
// ranking selectors, so ".content blockquote p" outranks
// ".content blockquote > :last-child" and the last paragraph in a
// blockquote or list item keeps its 10px/6px bottom margin. This is
// cosmetic (a little extra whitespace) and would need an upstream fix or
// a template change to avoid.
package inliner

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/vanng822/go-premailer/premailer"
	"golang.org/x/net/html"
)

// transform runs go-premailer on d, mutating it in place. A variable so
// tests can simulate a go-premailer that behaves differently.
var transform = func(d *goquery.Document) error {
	_, err := premailer.NewPremailer(d, premailer.NewOptions()).Transform()
	return err
}

// Inline inlines the CSS of doc's <style> elements into style attributes
// and returns the resulting document. The original <style> blocks stay in
// place byte-identical. A document without <style> elements is returned
// unchanged.
//
// <style> elements go-premailer skips by design are left alone: those with
// a media attribute other than "all", and those the author already marked
// data-premailer="ignore".
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
	styles := d.Find("style")
	if styles.Length() == 0 {
		// Nothing to inline: return the input untouched instead of a
		// normalized re-serialization.
		return doc, nil
	}

	original := make(map[*html.Node]bool, styles.Length())
	var marked []*goquery.Selection
	styles.Each(func(_ int, s *goquery.Selection) {
		original[s.Get(0)] = true
		if media, ok := s.Attr("media"); ok && media != "all" {
			return
		}
		if v, ok := s.Attr("data-premailer"); ok && v == "ignore" {
			return
		}
		// Keep the original: mark it as ignored and let go-premailer
		// consume an unmarked clone instead.
		clone := s.Clone()
		s.SetAttr("data-premailer", "ignore")
		s.AfterSelection(clone)
		marked = append(marked, s)
	})

	if err := transform(d); err != nil {
		return "", fmt.Errorf("inlining CSS: %w", err)
	}

	// Every <style> element not in the input was created by the wrapper
	// or by go-premailer: drop it.
	d.Find("style").Each(func(_ int, s *goquery.Selection) {
		if !original[s.Get(0)] {
			s.Remove()
		}
	})
	for _, s := range marked {
		// Fail rather than silently send a mail without its <style>
		// block (dark mode, small screens).
		if !attached(s.Get(0), d.Get(0)) {
			return "", errors.New("inlining CSS: a <style> block was lost")
		}
		s.RemoveAttr("data-premailer")
	}

	out, err := d.Html()
	if err != nil {
		return "", fmt.Errorf("serializing HTML: %w", err)
	}
	return out, nil
}

// attached reports whether n is still part of the tree rooted at root.
func attached(n, root *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if n == root {
			return true
		}
	}
	return false
}
