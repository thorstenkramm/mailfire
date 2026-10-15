package cli

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/markdown"
)

// inlineImages resolves the local images referenced in a Markdown body
// into inline message parts referenced via cid: (FR-1, Decision Q4). It
// returns the destination → cid: mapping for markdown.HTMLWithImages
// and the inline files, or nil for both when the body has no local
// images. The caller owns the files and must close them (closeFiles)
// once the message is encoded.
//
// Image paths are resolved like the body itself: relative to the body
// file's directory when the body came from --file, relative to the
// working directory for a stdin body; a leading ~ is expanded either
// way. Files are held to the same rules as attachments (regular files
// only); a missing or unreadable image fails the send with the resolved
// path in the message. Destinations are decoded first
// (markdown.ImagePath), so "my%20image.png" finds "my image.png".
//
// Trust model: the Markdown body is authored by the user running
// mailfire, so any path it names is read and sent — including absolute
// paths and ../ escapes outside the body's directory, exactly like
// --attach. There is deliberately no confinement to the body directory.
//
// The inline part's filename doubles as the content ID because Mailgun
// derives the MIME Content-ID of an inline part from the filename
// (Content-Id: <filename>; Mailgun's docs reference inline files as
// cid:<filename>, e.g. cid:test.jpg), and the html part references it
// as cid:<filename>. RFC 2392 does not require an @domain in the
// content ID; mail clients match the cid: URL against Content-Id
// verbatim. The name is
// img-<hash><ext> with the hash of the resolved absolute path:
// deterministic, identical for repeated references to the same image
// (one inline part), collision-free across different images, and free
// of characters that URL-escaping in the rendered html would rewrite
// (a space would become %20 and break the cid match).
func inlineImages(body, bodyFile string) (map[string]string, []mailgun.File, error) {
	dests := markdown.LocalImages(body)
	if len(dests) == 0 {
		return nil, nil, nil
	}
	baseDir := "."
	if bodyFile != "" {
		baseDir = filepath.Dir(config.ExpandPath(bodyFile))
	}
	cids := make(map[string]string, len(dests))
	byPath := map[string]string{} // resolved absolute path → content ID
	var files []mailgun.File
	for _, dest := range dests {
		path := config.ExpandPath(markdown.ImagePath(dest))
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path // hashing the unresolved path: still deterministic here
		}
		cid, seen := byPath[abs]
		if !seen {
			// Open the resolved absolute path, so an error message says
			// where mailfire actually looked for the image.
			f, err := openImage(abs)
			if err != nil {
				closeFiles(files)
				return nil, nil, err
			}
			cid = contentID(abs)
			files = append(files, mailgun.File{Name: cid, Reader: f})
			byPath[abs] = cid
		}
		cids[dest] = "cid:" + cid
	}
	return cids, files, nil
}

// contentID derives the inline part name (and content ID) for an image:
// "img-" plus 16 hex digits of the SHA-256 of its absolute path, plus
// the file's extension so the format stays visible. An extension with
// anything but ASCII letters and digits ("x.p g", "x.pñg") is dropped:
// the html renderer would percent-escape it and break the cid: match.
func contentID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	ext := filepath.Ext(absPath)
	for _, c := range strings.TrimPrefix(ext, ".") {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			ext = ""
			break
		}
	}
	return fmt.Sprintf("img-%x%s", sum[:8], ext)
}
