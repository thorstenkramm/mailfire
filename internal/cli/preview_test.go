package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

const previewTemplate = `<!DOCTYPE html><html><head><title>{{html .Subject}}</title>` +
	`<style>p { color: #123456; }</style></head><body><span>{{html .Preheader}}</span>{{.Body}}<footer>{{.Footer}}</footer></body></html>`

// isolatePreview leaves no config file to discover: the working
// directory and HOME are empty temporary directories.
func isolatePreview(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
		t.Setenv("PROGRAMDATA", dir)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runPreviewMock runs Run with a mock Mailgun base URL, so any request a
// preview made by mistake would be recorded.
func runPreviewMock(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	m := testutil.NewMockMailgun(t)
	code, stdout, stderr := runMock(t, m, stdin, args...)
	if n := len(m.Requests()); n != 0 {
		t.Errorf("preview sent %d request(s) to Mailgun", n)
	}
	return code, stdout, stderr
}

// TestPreviewWithoutConfig: no config file, credentials, recipients,
// sender, or subject are needed, and only the status line reaches stdout.
func TestPreviewWithoutConfig(t *testing.T) {
	dir := isolatePreview(t)
	out := filepath.Join(dir, "report.html")
	code, stdout, stderr := runPreviewMock(t, "# Hello\n", "--md", "--preview", out)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if stdout != "preview written to "+out+"\n" || stderr != "" {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
	if html := readFile(t, out); !strings.Contains(html, "Hello</h1>") || !strings.Contains(html, cardMarker) {
		t.Errorf("preview is not the rendered card template:\n%s", html)
	}
}

// TestPreviewStdout: "-" writes exactly the rendered HTML to stdout.
func TestPreviewStdout(t *testing.T) {
	isolatePreview(t)
	code, stdout, stderr := runPreviewMock(t, "", "--html", "-m", "<p>hi</p>", "--preview", "-")
	if code != ExitOK || stderr != "" {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "<!DOCTYPE html>") || !strings.HasSuffix(strings.TrimSpace(stdout), "</html>") {
		t.Errorf("stdout is not only the HTML document:\n%s", stdout)
	}
}

// TestPreviewMatchesSend: with a custom template, preheader, and footer,
// the preview is byte-identical to the html part a send posts.
func TestPreviewMatchesSend(t *testing.T) {
	dir := t.TempDir()
	tmpl := writeFile(t, filepath.Join(dir, "custom.html"), previewTemplate)
	footer := writeFile(t, filepath.Join(dir, "footer.html"), "<p>footer</p>")
	body := writeFile(t, filepath.Join(dir, "body.md"), "Text with **bold** and ![r](https://example.com/r.png)\r\n")
	flags := []string{"--md", "--file", body, "--subject", "S & T", "--preheader", "pre",
		"--footer", footer, "--template-file", tmpl}

	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, stderr := runMock(t, m, "", append([]string{"-c", cfg, "--to", "a@example.com"}, flags...)...)
	if code != ExitOK {
		t.Fatalf("send exit = %d; stderr:\n%s", code, stderr)
	}
	sent := m.LastRequest(t).Field("html")

	isolatePreview(t)
	code, stdout, stderr := runPreviewMock(t, "", append(flags, "--preview", "-")...)
	if code != ExitOK {
		t.Fatalf("preview exit = %d; stderr:\n%s", code, stderr)
	}
	if stdout != sent {
		t.Errorf("preview differs from the sent html part\npreview:\n%s\nsent:\n%s", stdout, sent)
	}
	for _, want := range []string{`<p style="color:#123456">footer</p>`, "<span>pre</span>", "S &amp; T", `src="https://example.com/r.png"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("preview missing %q:\n%s", want, stdout)
		}
	}
}

// TestPreviewInlineImages: local Markdown images are embedded as data:
// URLs (one portable file, no cid: references); remote images stay.
// Apart from the src, the preview equals the sent html part.
func TestPreviewInlineImages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "logo.png"), string(pngBytes))
	writeFile(t, filepath.Join(dir, "chart.svg"), `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	body := writeFile(t, filepath.Join(dir, "body.md"),
		"![logo](logo.png) ![chart](chart.svg) ![again](logo.png) ![r](https://example.com/r.png)\n")

	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, stderr := runMock(t, m, "", "-c", cfg, "--to", "a@example.com", "--subject", "s", "--md", "--file", body)
	if code != ExitOK {
		t.Fatalf("send exit = %d; stderr:\n%s", code, stderr)
	}
	req := m.LastRequest(t)
	want := req.Field("html")
	for _, f := range inlineFiles(req) {
		typ := "image/png"
		if strings.HasSuffix(f.Filename, ".svg") {
			typ = "image/svg+xml"
		}
		want = strings.ReplaceAll(want, `src="cid:`+f.Filename+`"`,
			`src="data:`+typ+`;base64,`+base64.StdEncoding.EncodeToString(f.Content)+`"`)
	}

	isolatePreview(t)
	code, stdout, stderr := runPreviewMock(t, "", "--md", "--file", body, "--subject", "s", "--preview", "-")
	if code != ExitOK {
		t.Fatalf("preview exit = %d; stderr:\n%s", code, stderr)
	}
	if stdout != want {
		t.Errorf("preview:\n%s\nwant:\n%s", stdout, want)
	}
	if strings.Contains(stdout, "cid:") || strings.Contains(stdout, "logo.png") {
		t.Errorf("preview references cid: parts or local files:\n%s", stdout)
	}
	if n := strings.Count(stdout, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes)); n != 2 {
		t.Errorf("png embedded %d times, want 2", n)
	}
	if !strings.Contains(stdout, `src="https://example.com/r.png"`) {
		t.Error("remote image changed")
	}
}

// TestPreviewMissingImage fails locally with the resolved path.
func TestPreviewMissingImage(t *testing.T) {
	isolatePreview(t)
	code, _, stderr := runPreviewMock(t, "![x](missing.png)\n", "--md", "--preview", "-")
	if code != ExitUsage || !strings.Contains(stderr, "missing.png") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

// TestPreviewText: a --txt body is escaped inside <pre>.
func TestPreviewText(t *testing.T) {
	isolatePreview(t)
	code, stdout, stderr := runPreviewMock(t, "a <b> & c\r\nline 2\n", "--subject", "<s>", "--preview", "-")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"<title>&lt;s&gt;</title>", "<pre>a &lt;b&gt; &amp; c\nline 2\n</pre>"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("preview missing %q:\n%s", want, stdout)
		}
	}
}

// TestPreviewHTMLRaw: the document is written byte-identical, CRLF and
// local image srcs included.
func TestPreviewHTMLRaw(t *testing.T) {
	dir := isolatePreview(t)
	raw := "<html>\r\n<body><img src=\"logo.png\"><p>x &amp; y</p></body></html>"
	body := writeFile(t, filepath.Join(dir, "raw.html"), raw)
	out := filepath.Join(dir, "out.html")
	code, _, stderr := runPreviewMock(t, "", "--html-raw", "--file", body, "--preview", out)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if got := readFile(t, out); got != raw {
		t.Errorf("preview = %q, want %q", got, raw)
	}
}

// TestPreviewConfigTemplate: the template selection comes from the config
// file, which needs no Mailgun settings, and from the environment.
func TestPreviewConfigTemplate(t *testing.T) {
	dir := isolatePreview(t)
	cfg := writeFile(t, filepath.Join(dir, "mailfire.conf"), "[sending]\ntemplate = \"plain\"\n")
	code, stdout, stderr := runPreviewMock(t, "hi\n", "-c", cfg, "--md", "--preview", "-")
	if code != ExitOK || !strings.Contains(stdout, plainMarker) {
		t.Fatalf("exit = %d; stderr:\n%s\nstdout:\n%s", code, stderr, stdout)
	}

	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "card-serif")
	code, stdout, stderr = runPreviewMock(t, "hi\n", "-c", cfg, "--md", "--preview", "-")
	if code != ExitOK || !strings.Contains(stdout, serifMarker) || !strings.Contains(stdout, cardMarker) {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}

	code, _, stderr = runPreviewMock(t, "hi\n", "-c", filepath.Join(dir, "missing.conf"), "--preview", "-")
	if code != ExitConfig || !strings.Contains(stderr, "no such file") {
		t.Errorf("missing -c: exit = %d, stderr = %q", code, stderr)
	}
	writeFile(t, cfg, "[sending]\ntemplate = \"nope\"\n")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "")
	code, _, stderr = runPreviewMock(t, "hi\n", "-c", cfg, "--md", "--preview", "-")
	if code != ExitConfig || !strings.Contains(stderr, "sending.template") {
		t.Errorf("invalid template: exit = %d, stderr = %q", code, stderr)
	}
}

// TestPreviewNoSideEffects: with logging, deferral, and deduplication
// configured, a preview creates no log, queue, or state entries.
func TestPreviewNoSideEffects(t *testing.T) {
	dir := isolatePreview(t)
	logfile := filepath.Join(dir, "mailfire.log")
	deferDir := filepath.Join(dir, "deferred")
	stateDir := filepath.Join(dir, "state")
	cfg := writeConfig(t, "from = \"ops@example.com\"\n\n[logging]\nlogfile = \""+filepath.ToSlash(logfile)+
		"\"\nloglevel = \"debug\"\n\n[defer]\ndefer_dir = \""+filepath.ToSlash(deferDir)+
		"\"\n\n[dedup]\nstate_dir = \""+filepath.ToSlash(stateDir)+"\"\n")
	code, _, stderr := runPreviewMock(t, "hi\n", "-c", cfg, "--to", "a@example.com", "--subject", "s",
		"--preview", filepath.Join(dir, "out.html"))
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, p := range []string{logfile, deferDir, stateDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a preview (err = %v)", p, err)
		}
	}
}

func TestPreviewRejectsSendOptions(t *testing.T) {
	dir := isolatePreview(t)
	attach := writeFile(t, filepath.Join(dir, "a.txt"), "a")
	out := filepath.Join(dir, "out.html")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--attach", attach}, "--preview cannot be combined with --attach"},
		{[]string{"--test"}, "--preview cannot be combined with --test"},
		{[]string{"--no-defer"}, "--preview cannot be combined with --no-defer"},
		{[]string{"--timeout", "5s"}, "--preview cannot be combined with --timeout"},
		{[]string{"--dedup-key", "k"}, "--preview cannot be combined with --dedup-key"},
		{[]string{"--txt", "--footer", attach}, "--footer requires --md or --html"},
	} {
		args := append([]string{"-m", "hi", "--preview", out}, tc.args...)
		code, stdout, stderr := runPreviewMock(t, "", args...)
		if code != ExitUsage || !strings.Contains(stderr, tc.want) || stdout != "" {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q, want %q", tc.args, code, stdout, stderr, tc.want)
		}
	}
	code, _, stderr := runPreviewMock(t, "", "-m", "hi", "--preview=")
	if code != ExitUsage || !strings.Contains(stderr, "--preview: empty path") {
		t.Errorf("empty --preview: exit = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("rejected preview wrote %s", out)
	}
}

// A preview writes HTML, not a command result: --json is a usage error
// for file and stdout output alike, reported as a JSON document.
func TestPreviewRejectsJSON(t *testing.T) {
	dir := isolatePreview(t)
	out := filepath.Join(dir, "out.html")
	for _, target := range []string{out, "-"} {
		code, stdout, stderr := runPreviewMock(t, "", "--md", "-m", "# hi", "--preview", target, "--json")
		if code != ExitUsage || !strings.Contains(stderr, "--preview cannot be combined with --json") {
			t.Errorf("--preview %s --json: exit = %d, stderr = %q", target, code, stderr)
		}
		if strings.Contains(stdout, "<html") || !strings.Contains(stdout, `"code":"usage_error"`) {
			t.Errorf("--preview %s --json: stdout = %q, want a JSON usage error", target, stdout)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("rejected preview wrote %s", out)
	}
}

func TestPreviewInvalidInput(t *testing.T) {
	isolatePreview(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--to", "not-an-address"}, "invalid --to address"},
		{[]string{"--from", "nope"}, "invalid sender"},
		{[]string{"--subject", "a\nb"}, "must not contain line breaks"},
		{[]string{"-m", ""}, "mail body missing"},
	} {
		args := append([]string{"--preview", "-"}, tc.args...)
		code, stdout, stderr := runPreviewMock(t, "body\n", args...)
		if code != ExitUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", tc.args, code, stdout, stderr)
		}
	}
}

// TestPreviewOverwrite: an existing output is replaced and keeps its
// permissions; no temporary files remain.
func TestPreviewOverwrite(t *testing.T) {
	dir := isolatePreview(t)
	out := writeFile(t, filepath.Join(dir, "out.html"), "old")
	if err := os.Chmod(out, 0o640); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runPreviewMock(t, "new\n", "--preview", out)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if got := readFile(t, out); !strings.Contains(got, "<pre>new\n</pre>") {
		t.Errorf("output not replaced:\n%s", got)
	}
	if fi, err := os.Stat(out); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o640) {
		t.Errorf("mode = %v, err = %v; want 0640", fi.Mode().Perm(), err)
	}
	assertOnlyFiles(t, dir, "out.html")
}

// TestPreviewFailurePreservesOutput: a render or output failure leaves
// an existing preview untouched and publishes no partial file.
func TestPreviewFailurePreservesOutput(t *testing.T) {
	dir := isolatePreview(t)
	out := writeFile(t, filepath.Join(dir, "out.html"), "previous preview")
	bad := writeFile(t, filepath.Join(dir, "bad.html"), "{{.Body")
	missingField := writeFile(t, filepath.Join(dir, "field.html"), "<p>{{.Body}}</p>{{.Nope}}")
	for _, args := range [][]string{
		{"--md", "--template-file", bad},
		{"--md", "--template-file", missingField},
		{"--md", "--footer", filepath.Join(dir, "missing-footer.html")},
		{"--md", "-m", "![x](missing.png)"},
	} {
		code, _, stderr := runPreviewMock(t, "hi\n", append(args, "--preview", out)...)
		if code != ExitUsage || stderr == "" {
			t.Errorf("%v: exit = %d, stderr = %q", args, code, stderr)
		}
		if got := readFile(t, out); got != "previous preview" {
			t.Errorf("%v: output changed to %q", args, got)
		}
	}
	assertOnlyFiles(t, dir, "bad.html", "field.html", "out.html")

	code, _, stderr := runPreviewMock(t, "hi\n", "--preview", filepath.Join(dir, "no-dir", "out.html"))
	if code != ExitUsage || !strings.Contains(stderr, "no-dir") {
		t.Errorf("missing directory: exit = %d, stderr = %q", code, stderr)
	}
	code, _, stderr = runPreviewMock(t, "hi\n", "--preview", dir)
	if code != ExitUsage || !strings.Contains(stderr, "not a regular file") {
		t.Errorf("directory output: exit = %d, stderr = %q", code, stderr)
	}
}

// TestPreviewRefusesInputAlias: the output may not be any input — body,
// footer, template, image, or config — directly or via a link.
func TestPreviewRefusesInputAlias(t *testing.T) {
	dir := isolatePreview(t)
	body := writeFile(t, filepath.Join(dir, "body.md"), "![logo](logo.png)\n")
	image := writeFile(t, filepath.Join(dir, "logo.png"), string(pngBytes))
	footer := writeFile(t, filepath.Join(dir, "footer.html"), "<p>f</p>")
	tmpl := writeFile(t, filepath.Join(dir, "custom.html"), previewTemplate)
	cfg := writeFile(t, filepath.Join(dir, "mailfire.conf"), "[sending]\ntemplate_file = \""+filepath.ToSlash(tmpl)+"\"\n")
	args := []string{"-c", cfg, "--md", "--file", body, "--footer", footer}

	// Sanity check: the inputs render fine to an unrelated output.
	if code, _, stderr := runPreviewMock(t, "", append(args, "--preview", filepath.Join(dir, "ok.html"))...); code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}

	inputs := []string{body, image, footer, tmpl, cfg}
	outputs := map[string]string{}
	for _, in := range inputs {
		outputs[in] = in
	}
	if runtime.GOOS != "windows" {
		for i, in := range inputs {
			link := filepath.Join(dir, "link"+string(rune('a'+i))+".html")
			if err := os.Symlink(in, link); err != nil {
				t.Fatal(err)
			}
			outputs[link] = in
		}
	}
	for i, in := range inputs {
		hard := filepath.Join(dir, "hard"+string(rune('a'+i))+".html")
		if err := os.Link(in, hard); err != nil {
			t.Skipf("hard links unsupported: %v", err)
		}
		outputs[hard] = in
	}

	before := map[string]string{}
	for _, in := range inputs {
		before[in] = readFile(t, in)
	}
	for out := range outputs {
		code, stdout, stderr := runPreviewMock(t, "", append(args, "--preview", out)...)
		if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "is the input file") {
			t.Errorf("--preview %s: exit = %d, stdout = %q, stderr = %q", out, code, stdout, stderr)
		}
	}
	for _, in := range inputs {
		if got := readFile(t, in); got != before[in] {
			t.Errorf("input %s changed", in)
		}
	}

	// The template from --template-file is an input as well.
	code, _, stderr := runPreviewMock(t, "hi\n", "--md", "--template-file", tmpl, "--preview", tmpl)
	if code != ExitUsage || !strings.Contains(stderr, "is the input file") {
		t.Errorf("--template-file alias: exit = %d, stderr = %q", code, stderr)
	}
}

// TestPreviewFollowsOutputSymlink: a symlinked output that is not an
// input is followed, so the link keeps pointing at the new preview.
func TestPreviewFollowsOutputSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := isolatePreview(t)
	target := writeFile(t, filepath.Join(dir, "target.html"), "old")
	link := filepath.Join(dir, "link.html")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runPreviewMock(t, "new\n", "--preview", link); code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v, %v", fi, err)
	}
	if got := readFile(t, target); !strings.Contains(got, "new") {
		t.Errorf("target = %q", got)
	}
}

// assertOnlyFiles fails when dir holds anything but names (for example a
// leftover temporary file).
func assertOnlyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !equalStrings(got, names) {
		t.Errorf("files in %s = %v, want %v", dir, got, names)
	}
}

// TestPreviewRefusesStdinAlias: a body redirected from a regular file
// (--preview body.txt <body.txt) is an input too, also via links.
func TestPreviewRefusesStdinAlias(t *testing.T) {
	dir := isolatePreview(t)
	body := writeFile(t, filepath.Join(dir, "body.txt"), "hello\n")
	outputs := []string{body}
	if err := os.Link(body, filepath.Join(dir, "hard.html")); err == nil {
		outputs = append(outputs, filepath.Join(dir, "hard.html"))
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(body, filepath.Join(dir, "link.html")); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, filepath.Join(dir, "link.html"))
	}
	for _, out := range outputs {
		f, err := os.Open(body)
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := Run([]string{"--preview", out}, &stdout, &stderr, testInfo, WithStdin(f))
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if code != ExitUsage || !strings.Contains(stderr.String(), "is the input file standard input") {
			t.Errorf("--preview %s: exit = %d, stderr = %q", out, code, stderr.String())
		}
		if got := readFile(t, body); got != "hello\n" {
			t.Fatalf("--preview %s replaced the body with %q", out, got)
		}
	}

	// A redirected regular file that is not the output is fine.
	f, err := os.Open(body)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	other := writeFile(t, filepath.Join(dir, "other.html"), "old")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--preview", other}, &stdout, &stderr, testInfo, WithStdin(f)); code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr.String())
	}
	if got := readFile(t, other); !strings.Contains(got, "<pre>hello\n</pre>") {
		t.Errorf("output = %q", got)
	}
}

// TestPreviewRefusesDanglingSymlink: a symlink to a missing file is
// neither replaced nor followed; the link is left as it was.
func TestPreviewRefusesDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := isolatePreview(t)
	link := filepath.Join(dir, "link.html")
	missing := filepath.Join(dir, "missing.html")
	if err := os.Symlink(missing, link); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runPreviewMock(t, "hi\n", "--preview", link)
	if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "symbolic link to a missing file") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if dest, err := os.Readlink(link); err != nil || dest != missing {
		t.Errorf("link changed: %q, %v", dest, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Errorf("link target created: %v", err)
	}
	assertOnlyFiles(t, dir, "link.html")
}

// TestPreviewIgnoresUnrelatedEnv: only the template variables affect a
// preview; invalid send-only variables do not fail it.
func TestPreviewIgnoresUnrelatedEnv(t *testing.T) {
	isolatePreview(t)
	t.Setenv("MAILFIRE_DEFER_DEFER", "maybe")
	t.Setenv("MAILFIRE_MAILGUN_TIMEOUT", "soon")
	t.Setenv("MAILFIRE_SENDING_TEMPLATE", "plain")
	code, stdout, stderr := runPreviewMock(t, "hi\n", "--md", "--preview", "-")
	if code != ExitOK || !strings.Contains(stdout, plainMarker) {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	t.Setenv("MAILFIRE_SENDING_TEMPLATE_FILE", "x.html")
	code, _, stderr = runPreviewMock(t, "hi\n", "--md", "--preview", "-")
	if code != ExitConfig || !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("both template variables: exit = %d, stderr = %q", code, stderr)
	}
}
