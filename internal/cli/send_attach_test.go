package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// Minimal real-world file headers so content-type sniffing produces the
// types Mailgun and mail clients expect.
var (
	pdfBytes = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("pdf-body "), 32)...)
	pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xab}, 256)...)
	txtBytes = []byte("notes with plain text\n")
)

// writeAttachment writes name with content into dir and returns its path.
func writeAttachment(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSendAttachmentsReadmeExample mirrors the README example: two
// attachments (a PDF and a PNG) on a plain-text send. The mock request
// must carry exactly two "attachment" parts, in flag order, with the
// files' base names, sniffed content types, and exact bytes.
func TestSendAttachmentsReadmeExample(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	pdf := writeAttachment(t, dir, "doc1.pdf", pdfBytes)
	png := writeAttachment(t, dir, "image.png", pngBytes)

	code, _, stderr := runMock(t, m, "the files\n",
		"-c", cfg, "--to", "user@example.com", "--subject", "The files",
		"--attach", pdf, "--attach", png)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}

	req := m.LastRequest(t)
	if req.ParseErr != nil {
		t.Fatalf("mock could not parse request body: %v", req.ParseErr)
	}
	if len(req.Files) != 2 {
		t.Fatalf("attachment parts = %d, want 2", len(req.Files))
	}
	want := []testutil.RecordedFile{
		{FieldName: "attachment", Filename: "doc1.pdf", ContentType: "application/pdf", Content: pdfBytes},
		{FieldName: "attachment", Filename: "image.png", ContentType: "image/png", Content: pngBytes},
	}
	for i, w := range want {
		got := req.Files[i]
		if got.FieldName != w.FieldName {
			t.Errorf("file %d: field = %q, want %q", i, got.FieldName, w.FieldName)
		}
		if got.Filename != w.Filename {
			t.Errorf("file %d: filename = %q, want %q", i, got.Filename, w.Filename)
		}
		if got.ContentType != w.ContentType {
			t.Errorf("file %d: content type = %q, want %q", i, got.ContentType, w.ContentType)
		}
		if !bytes.Equal(got.Content, w.Content) {
			t.Errorf("file %d: content = %d bytes %q..., want exact file bytes", i, len(got.Content), got.Content[:16])
		}
	}
	// The body still lands in the text part.
	if got := req.Field("text"); got != "the files\n" {
		t.Errorf("text = %q, want %q", got, "the files\\n")
	}
}

// TestSendAttachmentFormats sends one attachment together with every body
// format (default --txt and each explicit format flag), plus a --file
// body. The attachment part must be identical regardless of format, and
// the body parts must match the format.
func TestSendAttachmentFormats(t *testing.T) {
	dir := t.TempDir()
	att := writeAttachment(t, dir, "notes.txt", txtBytes)

	for _, tc := range []struct {
		name     string
		flags    []string // format flags
		body     string   // stdin body
		wantText string
		wantHTML bool
	}{
		{"default txt", nil, "plain body\n", "plain body\n", false},
		{"txt", []string{"--txt"}, "plain body\n", "plain body\n", false},
		{"md", []string{"--md"}, "# Headline\n", "Headline\n", true},
		{"html", []string{"--html"}, "<p>fragment</p>\n", "fragment", true},
		{"html-raw", []string{"--html-raw"}, "<html><body><p>raw</p></body></html>", "raw", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg := writeConfig(t, "from = \"ops@example.com\"\n")
			args := append([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s",
				"--attach", att}, tc.flags...)
			code, _, stderr := runMock(t, m, tc.body, args...)
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
			}
			req := m.LastRequest(t)
			if len(req.Files) != 1 {
				t.Fatalf("attachment parts = %d, want 1", len(req.Files))
			}
			got := req.Files[0]
			if got.FieldName != "attachment" || got.Filename != "notes.txt" {
				t.Errorf("part = %q/%q, want attachment/notes.txt", got.FieldName, got.Filename)
			}
			if !strings.HasPrefix(got.ContentType, "text/plain") {
				t.Errorf("content type = %q, want text/plain", got.ContentType)
			}
			if !bytes.Equal(got.Content, txtBytes) {
				t.Errorf("content = %q, want %q", got.Content, txtBytes)
			}
			if text := req.Field("text"); !strings.Contains(text, tc.wantText) {
				t.Errorf("text = %q, want it to contain %q", text, tc.wantText)
			}
			if hasHTML := req.Field("html") != ""; hasHTML != tc.wantHTML {
				t.Errorf("html part present = %v, want %v", hasHTML, tc.wantHTML)
			}
		})
	}

	t.Run("with --file body", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		cfg := writeConfig(t, "from = \"ops@example.com\"\n")
		bodyFile := writeAttachment(t, dir, "body.txt", []byte("from a file\n"))
		code, _, stderr := runMock(t, m, "stdin is ignored",
			"-c", cfg, "--to", "a@example.com", "--subject", "s",
			"--file", bodyFile, "--attach", att)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
		}
		req := m.LastRequest(t)
		if got := req.Field("text"); got != "from a file\n" {
			t.Errorf("text = %q, want %q", got, "from a file\\n")
		}
		if len(req.Files) != 1 || req.Files[0].Filename != "notes.txt" {
			t.Errorf("files = %+v, want one attachment notes.txt", req.Files)
		}
	})
}

// TestSendAttachmentTildeExpansion expands a leading ~ in --attach paths
// and keeps a filename containing spaces and non-ASCII characters intact.
func TestSendAttachmentTildeExpansion(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAttachment(t, home, "grüße von mir.txt", txtBytes)

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s",
		"--attach", "~/grüße von mir.txt")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if req.ParseErr != nil {
		t.Fatalf("mock could not parse request body: %v", req.ParseErr)
	}
	if len(req.Files) != 1 {
		t.Fatalf("attachment parts = %d, want 1", len(req.Files))
	}
	if got := req.Files[0].Filename; got != "grüße von mir.txt" {
		t.Errorf("filename = %q, want %q", got, "grüße von mir.txt")
	}
	if !bytes.Equal(req.Files[0].Content, txtBytes) {
		t.Errorf("content = %q, want %q", req.Files[0].Content, txtBytes)
	}
}

// TestSendAttachmentErrors: a missing file, a directory, and an
// unreadable file are usage errors (exit 1) naming the path, and nothing
// is sent.
func TestSendAttachmentErrors(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")

	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.pdf")
	unreadable := writeAttachment(t, dir, "unreadable.pdf", pdfBytes)
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadable, 0o600); err != nil {
			t.Error(err)
		}
	})

	for _, tc := range []struct {
		name string
		path string
		want []string // stderr fragments
	}{
		{"missing", missing, []string{"attachment", missing}},
		{"directory", dir, []string{"attachment", dir, "directory"}},
		{"unreadable", unreadable, []string{"attachment", unreadable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" {
				if _, err := os.Open(tc.path); err == nil {
					t.Skip("running with read access to 0000 files (e.g. as root)")
				}
			}
			code, _, stderr := runMock(t, m, "body",
				"-c", cfg, "--to", "a@example.com", "--subject", "s",
				"--attach", tc.path)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			for _, want := range tc.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
		})
	}
	if len(m.Requests()) != 0 {
		t.Errorf("mock received %d requests, want 0", len(m.Requests()))
	}
}

// TestSendAttachmentSpecialPaths: symlinks are followed (a link to a
// directory is refused as a directory, a link to a file sends the file
// under the link's name); devices and empty paths are refused; a failing
// later --attach closes files opened for earlier ones.
func TestSendAttachmentSpecialPaths(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	target := writeAttachment(t, dir, "target.txt", txtBytes)
	dirLink := filepath.Join(dir, "dirlink")
	fileLink := filepath.Join(dir, "filelink.txt")
	if err := os.Symlink(sub, dirLink); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Symlink(target, fileLink); err != nil {
		t.Fatal(err)
	}

	t.Run("symlink to file", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		cfg := writeConfig(t, "from = \"ops@example.com\"\n")
		code, _, stderr := runMock(t, m, "body",
			"-c", cfg, "--to", "a@example.com", "--subject", "s", "--attach", fileLink)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
		}
		req := m.LastRequest(t)
		if len(req.Files) != 1 || req.Files[0].Filename != "filelink.txt" ||
			!bytes.Equal(req.Files[0].Content, txtBytes) {
			t.Errorf("files = %+v, want filelink.txt with target bytes", req.Files)
		}
	})

	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"symlink to directory", []string{dirLink}, []string{dirLink, "is a directory"}},
		{"trailing slash", []string{sub + "/"}, []string{"is a directory"}},
		{"device", []string{os.DevNull}, []string{os.DevNull, "not a regular file"}},
		{"empty path", []string{""}, []string{"attachment: empty path"}},
		{"second fails", []string{target, filepath.Join(dir, "nope")}, []string{"nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			cfg := writeConfig(t, "from = \"ops@example.com\"\n")
			args := []string{"-c", cfg, "--to", "a@example.com", "--subject", "s"}
			for _, p := range tc.paths {
				args = append(args, "--attach", p)
			}
			code, _, stderr := runMock(t, m, "body", args...)
			if code != ExitUsage {
				t.Errorf("exit = %d, want %d", code, ExitUsage)
			}
			for _, want := range tc.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
			if n := len(m.Requests()); n != 0 {
				t.Errorf("mock received %d requests, want 0", n)
			}
		})
	}
}

// TestOpenAttachmentsLifecycle checks the file handle lifecycle
// directly: closeFiles closes every handle; duplicates get their own.
func TestOpenAttachmentsLifecycle(t *testing.T) {
	dir := t.TempDir()
	ok := writeAttachment(t, dir, "ok.txt", txtBytes)

	files, err := openAttachments([]string{ok})
	if err != nil || len(files) != 1 {
		t.Fatalf("openAttachments = %v, %v", files, err)
	}
	f, isFile := files[0].Reader.(*os.File)
	if !isFile {
		t.Fatalf("reader type = %T, want *os.File", files[0].Reader)
	}
	closeFiles(files)
	if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("read after closeFiles: err = %v, want os.ErrClosed", err)
	}

	// Duplicates are allowed and get independent handles.
	files, err = openAttachments([]string{ok, ok})
	if err != nil || len(files) != 2 || files[0].Reader == files[1].Reader {
		t.Fatalf("duplicate attach: files = %v, err = %v", files, err)
	}
	closeFiles(files)
}
