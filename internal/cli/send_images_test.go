package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// pngBytes and writeAttachment are shared with send_attach_test.go.

// inlineFiles returns the recorded request's file parts with field name
// "inline".
func inlineFiles(req testutil.RecordedRequest) []testutil.RecordedFile {
	var out []testutil.RecordedFile
	for _, f := range req.Files {
		if f.FieldName == "inline" {
			out = append(out, f)
		}
	}
	return out
}

// TestSendMarkdownInlineImage: a local image in --md input (stdin body,
// cwd-relative path) is sent as an inline part whose filename is the
// content ID, and the html part references it via cid: — after CSS
// inlining, which must not mangle the cid: src.
func TestSendMarkdownInlineImage(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	body := "# Hi\n\n![the logo](logo.png)\n"
	code, _, stderr := runMock(t, m, body,
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}

	req := m.LastRequest(t)
	inline := inlineFiles(req)
	if len(inline) != 1 {
		t.Fatalf("got %d inline parts, want 1 (files: %+v)", len(inline), req.Files)
	}
	f := inline[0]
	if !strings.HasPrefix(f.Filename, "img-") || !strings.HasSuffix(f.Filename, ".png") {
		t.Errorf("inline filename = %q, want img-<hash>.png", f.Filename)
	}
	if !bytes.Equal(f.Content, pngBytes) {
		t.Errorf("inline content = %q, want the image bytes", f.Content)
	}

	// The html references the inline part via cid: and preserves the alt
	// text. The base template carries a <style> block, so the html went
	// through the inliner: the cid: src survived it untouched.
	html := req.Field("html")
	if want := `src="cid:` + f.Filename + `"`; !strings.Contains(html, want) {
		t.Errorf("html missing %q:\n%s", want, html)
	}
	if !strings.Contains(html, `alt="the logo"`) {
		t.Errorf("html missing alt text:\n%s", html)
	}
	// The text part keeps the alt text as the image's readable form.
	if !strings.Contains(req.Field("text"), "the logo") {
		t.Errorf("text part missing alt text:\n%s", req.Field("text"))
	}
}

// TestSendMarkdownInlineImageFromFile: with --file, a relative image
// path is resolved against the body file's directory, not the cwd.
func TestSendMarkdownInlineImageFromFile(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "chart.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	bodyFile := filepath.Join(dir, "body.md")
	if err := os.WriteFile(bodyFile, []byte("![chart](assets/chart.png)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Run from a different directory: resolution must still find the
	// image next to the body file.
	t.Chdir(t.TempDir())

	code, _, stderr := runMock(t, m, "",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md", "--file", bodyFile)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	inline := inlineFiles(m.LastRequest(t))
	if len(inline) != 1 || !bytes.Equal(inline[0].Content, pngBytes) {
		t.Fatalf("inline parts = %+v, want one part with the image bytes", inline)
	}
}

// TestSendMarkdownInlineImageTwice: repeated references to the same
// image (identical or differently spelled destinations) produce a single
// inline part, and every reference uses the same cid:.
func TestSendMarkdownInlineImageTwice(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	body := "![a](logo.png)\n\n![b](./logo.png)\n\n![c](logo.png)\n"
	code, _, stderr := runMock(t, m, body,
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}

	req := m.LastRequest(t)
	inline := inlineFiles(req)
	if len(inline) != 1 {
		t.Fatalf("got %d inline parts, want 1 (files: %+v)", len(inline), req.Files)
	}
	html := req.Field("html")
	if n := strings.Count(html, `src="cid:`+inline[0].Filename+`"`); n != 3 {
		t.Errorf("html references cid: %d times, want 3:\n%s", n, html)
	}
}

// TestSendMarkdownInlineImageMissing: a missing image file fails the
// send with exit 1 and the resolved path in the message; nothing is sent.
func TestSendMarkdownInlineImageMissing(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	t.Chdir(dir)
	missing := filepath.Join(dir, "nope.png")

	code, _, stderr := runMock(t, m, "![x](nope.png)\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, missing) {
		t.Errorf("stderr missing image path %q:\n%s", missing, stderr)
	}
	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0", len(m.Requests()))
	}
}

// TestSendMarkdownInlineImageNotRegular: an image path that is a
// directory fails like a bad attachment (same regular-file rules).
func TestSendMarkdownInlineImageNotRegular(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runMock(t, m, "![x](subdir)\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "is a directory") {
		t.Errorf("stderr missing directory error:\n%s", stderr)
	}
}

// TestSendMarkdownRemoteImageUntouched: http(s) image URLs are neither
// attached nor rewritten (Decision Q4).
func TestSendMarkdownRemoteImageUntouched(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	body := "![remote](https://example.com/logo.png)\n"
	code, _, stderr := runMock(t, m, body,
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if n := len(inlineFiles(req)); n != 0 {
		t.Errorf("got %d inline parts, want 0 for a remote image", n)
	}
	if want := `src="https://example.com/logo.png"`; !strings.Contains(req.Field("html"), want) {
		t.Errorf("html missing untouched remote src %q", want)
	}
}

// TestSendMarkdownInlineImageTilde: a ~ path in the image destination is
// expanded against HOME.
func TestSendMarkdownInlineImageTilde(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "logo.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runMock(t, m, "![logo](~/logo.png)\n",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if n := len(inlineFiles(m.LastRequest(t))); n != 1 {
		t.Errorf("got %d inline parts, want 1", n)
	}
}

// TestSendMarkdownInlineImageEncodedPath: image destinations are URLs,
// so percent-encoding, the <...> form and backslash escapes are decoded
// before the file lookup — all three spellings find "my_image 1.png"
// and share one inline part. data: and file: images are never read.
func TestSendMarkdownInlineImageEncodedPath(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "my_image 1.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	body := "![a](my_image%201.png)\n\n![b](<my_image 1.png>)\n\n![c](<my\\_image 1.png>)\n\n" +
		"![d](data:image/png;base64,iVBORw0K)\n\n![e](file:///etc/passwd)\n"
	code, _, stderr := runMock(t, m, body,
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--md")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	inline := inlineFiles(req)
	if len(inline) != 1 || !bytes.Equal(inline[0].Content, pngBytes) {
		t.Fatalf("inline parts = %+v, want one part with the image bytes", inline)
	}
	html := req.Field("html")
	if n := strings.Count(html, `src="cid:`+inline[0].Filename+`"`); n != 3 {
		t.Errorf("html references cid: %d times, want 3:\n%s", n, html)
	}
	if !strings.Contains(html, `src="data:image/png;base64,iVBORw0K"`) {
		t.Errorf("data: image not left untouched:\n%s", html)
	}
}

func TestContentIDExtension(t *testing.T) {
	for path, ext := range map[string]string{
		"/x/logo.png":  ".png",
		"/x/logo.JPEG": ".JPEG",
		"/x/logo":      "",
		"/x/a.p g":     "",
		"/x/a.pñg":     "",
	} {
		id := contentID(path)
		if !strings.HasPrefix(id, "img-") || len(id) != len("img-")+16+len(ext) || !strings.HasSuffix(id, ext) {
			t.Errorf("contentID(%q) = %q, want img-<16 hex>%s", path, id, ext)
		}
	}
}
