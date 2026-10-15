package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// IsLocalImage reports whether an image destination refers to a local
// file rather than a remote URL: anything without a URL scheme is local,
// so relative paths, absolute paths and ~ paths are local, while
// http:// and https:// URLs (left untouched per Decision Q4), other
// schemes (cid:, data:, ...) and protocol-relative URLs (//host/x.png)
// are not.
//
// Two edge cases are deliberately classified as not-local: a Windows
// drive-letter path (C:\img.png) parses as the scheme "c" — Windows
// support is task 18 — and a plain filename containing a colon
// (img:2.png) parses as a scheme too. Neither is attempted as a file,
// so they render unchanged instead of failing the send.
func IsLocalImage(dest string) bool {
	if strings.HasPrefix(dest, "//") {
		return false
	}
	colon := strings.IndexByte(dest, ':')
	if colon <= 0 || !isAlpha(dest[0]) {
		return true
	}
	for i := 1; i < colon; i++ {
		if c := dest[i]; !isAlphaNum(c) && c != '+' && c != '-' && c != '.' {
			return true // not a valid scheme: a local path containing ':'
		}
	}
	return false
}

func isAlpha(c byte) bool    { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlphaNum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' }

// LocalImages returns the destinations of the images in src that refer
// to local files (IsLocalImage), deduplicated and in order of first
// appearance.
func LocalImages(src string) []string {
	var dests []string
	seen := map[string]bool{}
	walkImages(normalize(src), func(img *ast.Image) {
		if dest := string(img.Destination); IsLocalImage(dest) && !seen[dest] {
			seen[dest] = true
			dests = append(dests, dest)
		}
	})
	return dests
}

// HTMLWithImages renders src like HTML, but rewrites the destination of
// every local image (IsLocalImage) through replace; remote images are
// never passed to replace and render unchanged. replace is only called
// for destinations LocalImages would list, so it can be a plain map
// lookup.
//
// cid: replacements survive the renderer byte-identically: goldmark's
// URL filter drops only javascript:, vbscript:, file: and non-image
// data: destinations, and its URL escaping keeps ':' and '@'.
func HTMLWithImages(src string, replace func(dest string) string) string {
	src = normalize(src)
	doc := md.Parser().Parse(text.NewReader([]byte(src)))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering && IsLocalImage(string(img.Destination)) {
			img.Destination = []byte(replace(string(img.Destination)))
		}
		return ast.WalkContinue, nil
	})
	var buf bytes.Buffer
	_ = md.Renderer().Render(&buf, []byte(src), doc)
	return buf.String()
}

// walkImages parses src and calls f for every image node.
func walkImages(src string, f func(*ast.Image)) {
	doc := md.Parser().Parse(text.NewReader([]byte(src)))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			f(img)
		}
		return ast.WalkContinue, nil
	})
}
