package markdown

import (
	"bytes"
	"net/url"
	"runtime"
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
// On Windows a drive-letter path (C:\img.png, C:/img.png, or the
// drive-relative C:img.png) parses as the single-letter scheme "c"; it
// is classified as local there (see isLocalImage). On other platforms it
// keeps the scheme reading and is not local — a Windows path on a Unix
// box names no file anyway. A plain filename containing a colon
// (img:2.png) also parses as a scheme and is deliberately not local:
// it is never attempted as a file, so it renders unchanged instead of
// failing the send.
func IsLocalImage(dest string) bool {
	return isLocalImage(dest, runtime.GOOS == "windows")
}

// isLocalImage is IsLocalImage parametrized on the target platform, so
// the Windows drive-letter rule can be tested on any OS.
func isLocalImage(dest string, windows bool) bool {
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
	// No registered URL scheme has a single letter, so on Windows a
	// single-letter "scheme" is a drive letter — a local path.
	if windows && colon == 1 {
		return true
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

// ImagePath converts a local image destination as returned by
// LocalImages (raw Markdown source) into the file path it names:
// backslash escapes and entity references are resolved as goldmark
// does when rendering, then percent-encoding is decoded, since the
// destination is a URL ("my%20image.png" names "my image.png"). A
// destination that is not valid percent-encoding ("100%.png") is taken
// literally.
func ImagePath(dest string) string {
	p := unescape([]byte(dest))
	if dec, err := url.PathUnescape(p); err == nil {
		return dec
	}
	return p
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
	if err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering && IsLocalImage(string(img.Destination)) {
			img.Destination = []byte(replace(string(img.Destination)))
		}
		return ast.WalkContinue, nil
	}); err != nil {
		panic(err) // The visitor always returns nil.
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, []byte(src), doc); err != nil {
		panic(err) // Built-in renderers only return writer errors; bytes.Buffer cannot fail.
	}
	return buf.String()
}

// walkImages parses src and calls f for every image node.
func walkImages(src string, f func(*ast.Image)) {
	doc := md.Parser().Parse(text.NewReader([]byte(src)))
	if err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			f(img)
		}
		return ast.WalkContinue, nil
	}); err != nil {
		panic(err) // The visitor always returns nil.
	}
}
