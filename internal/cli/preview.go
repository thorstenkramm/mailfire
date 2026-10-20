package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/fsutil"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// Local preview (task 29): send --preview <path> renders the HTML body
// exactly as a send would, but writes it to a file (or stdout for "-")
// instead of contacting Mailgun. No credentials, recipients, sender or
// subject are needed, and nothing is sent, deferred, deduplicated or
// logged. The only preview-specific step is image handling: local
// Markdown images, which a send attaches as cid: parts, are embedded as
// data: URLs so the preview is one portable file.

// checkPreviewFlags rejects options that only affect sending: a preview
// would silently ignore them. --json is rejected too: a preview writes
// HTML, not a command result.
func checkPreviewFlags(opts *SendOptions) error {
	if opts.Preview == "" {
		return nil
	}
	if opts.JSON {
		return errors.New("--preview cannot be combined with --json: a preview writes HTML, not a command result")
	}
	for _, c := range []struct {
		set  bool
		flag string
	}{
		{len(opts.Attach) > 0, "--attach"},
		{opts.Test, "--test"},
		{opts.NoDefer, "--no-defer"},
		{opts.Timeout > 0, "--timeout"},
		{opts.DedupKey != "", "--dedup-key"},
		{opts.Cooldown > 0, "--cooldown"},
	} {
		if c.set {
			return fmt.Errorf("--preview cannot be combined with %s: a preview renders the body locally and sends nothing", c.flag)
		}
	}
	return nil
}

// runPreview renders the message and publishes it to opts.Preview. All
// failures except configuration errors are usage errors (exit 1).
func runPreview(opts *SendOptions, rc *runContext, stdout, stderr io.Writer) int {
	cfg, err := config.LoadRender(opts.Config, stderr, config.Overrides{
		Template: opts.Template != "" || opts.TemplateFile != "",
	})
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}
	doc, inputs, err := renderPreview(cfg, opts, rc.stdin)
	if err != nil {
		printfOutput(stderr, "mailfire: preview: %v\n", err)
		return ExitUsage
	}
	if opts.Preview == "-" {
		if _, err := io.WriteString(stdout, doc); err != nil {
			printfOutput(stderr, "mailfire: preview: writing to stdout: %v\n", err)
			return ExitUsage
		}
		return ExitOK
	}
	path, err := writePreview(opts.Preview, doc, inputs)
	if err != nil {
		printfOutput(stderr, "mailfire: preview: %v\n", err)
		return ExitUsage
	}
	printfOutput(stdout, "preview written to %s\n", path)
	return ExitOK
}

// previewInput is a file a preview was rendered from. The output must not
// replace it. info identifies standard input, which has no path; other
// inputs are identified by stat'ing path when the output is written.
type previewInput struct {
	path string
	info fs.FileInfo
}

// renderPreview validates the optional headers, renders the body through
// the sending pipeline and returns the preview document plus the input
// files it read.
func renderPreview(cfg *config.Config, opts *SendOptions, stdin io.Reader) (string, []previewInput, error) {
	return renderPreviewPrepared(cfg, opts, stdin, nil)
}

func renderPreviewPrepared(cfg *config.Config, opts *SendOptions, stdin io.Reader, rendering *preparedRendering) (string, []previewInput, error) {
	if opts.From != "" {
		if _, err := parseAddress(opts.From); err != nil {
			return "", nil, fmt.Errorf("invalid sender %q: %w", opts.From, err)
		}
	}
	for _, r := range []struct {
		flag  string
		addrs []string
	}{{"--to", opts.To}, {"--cc", opts.Cc}, {"--bcc", opts.Bcc}} {
		if _, err := parseAddresses(r.flag, r.addrs); err != nil {
			return "", nil, err
		}
	}
	if err := checkSubject(opts.Subject); err != nil {
		return "", nil, err
	}

	var inputs []previewInput
	if cfg.Path != "" {
		inputs = append(inputs, previewInput{path: cfg.Path})
	}
	if opts.File != "" {
		inputs = append(inputs, previewInput{path: config.ExpandPath(opts.File)})
	} else if in, ok := stdinInput(opts, stdin); ok {
		inputs = append(inputs, in)
	}
	if opts.Footer != "" {
		inputs = append(inputs, previewInput{path: config.ExpandPath(opts.Footer)})
	}
	if _, file := templateSelection(cfg, opts); file != "" && (opts.Format == "md" || opts.Format == "html") {
		inputs = append(inputs, previewInput{path: config.ExpandPath(file)})
	}

	body, err := readMessageBody(opts, stdin)
	if err != nil {
		return "", nil, err
	}
	if body == "" {
		return "", nil, errors.New("mail body missing: use stdin, --file or --message")
	}
	if rendering == nil {
		rendering, err = prepareRendering(cfg, opts)
		if err != nil {
			return "", nil, err
		}
	}
	doc, _, inline, err := rendering.renderMessage(opts, body)
	if err != nil {
		return "", nil, err
	}
	defer closeFiles(inline)
	switch opts.Format {
	case "txt":
		return textPreview(opts.Subject, body), inputs, nil
	case "html-raw":
		return doc, inputs, nil
	}
	for _, f := range inline {
		path, url, err := dataURL(f)
		if err != nil {
			return "", nil, err
		}
		inputs = append(inputs, previewInput{path: path})
		doc = strings.ReplaceAll(doc, `src="cid:`+f.Name+`"`, `src="`+url+`"`)
	}
	return doc, inputs, nil
}

// stdinInput identifies the body's standard input when it is redirected
// from a regular file (mailfire --preview body.txt <body.txt), so the
// output cannot replace it. Pipes and terminals cannot alias a file.
func stdinInput(opts *SendOptions, stdin io.Reader) (previewInput, bool) {
	if opts.MessageSet {
		return previewInput{}, false
	}
	st, ok := stdin.(interface{ Stat() (fs.FileInfo, error) })
	if !ok {
		return previewInput{}, false
	}
	fi, err := st.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return previewInput{}, false
	}
	return previewInput{path: "standard input", info: fi}, true
}

// textPreview shows a --txt body as escaped, preformatted text in a
// minimal HTML document.
func textPreview(subject, body string) string {
	return "<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n<title>" +
		html.EscapeString(subject) + "</title>\n</head>\n<body>\n<pre>" +
		html.EscapeString(body) + "</pre>\n</body>\n</html>\n"
}

// dataURL reads an inline image part and returns its file path and its
// content as a base64 data: URL. The media type comes from the file
// extension, or from the content when the extension is unknown.
func dataURL(f mailgun.File) (string, string, error) {
	file, ok := f.Reader.(interface {
		io.Reader
		Name() string
	})
	if !ok {
		return "", "", fmt.Errorf("image %s: not a file", f.Name) // cannot happen: inlineImages opens files
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", "", fmt.Errorf("image %s: %w", file.Name(), err)
	}
	typ := mime.TypeByExtension(filepath.Ext(file.Name()))
	if typ == "" {
		typ = http.DetectContentType(data)
	}
	if mt, _, err := mime.ParseMediaType(typ); err == nil {
		typ = mt
	}
	return file.Name(), "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// writePreview atomically replaces path (~ expanded; an existing symlink
// is followed to its target) with doc and returns the expanded path. An
// existing output keeps its permissions; a new one gets 0644. A dangling
// symlink is refused rather than replaced. The output must not be one of
// the inputs, also not through a symlink or hard link: the preview would
// destroy the source it was rendered from.
func writePreview(path, doc string, inputs []previewInput) (string, error) {
	out := config.ExpandPath(path)
	target := out
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("output %s: %w", out, err)
	} else if fi, lerr := os.Lstat(out); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("output %s: symbolic link to a missing file; remove it or choose another output path", out)
	}
	mode := fs.FileMode(0o644)
	fi, err := os.Stat(target)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("output %s: not a regular file", out)
		}
		for _, in := range inputs {
			ifi := in.info
			if ifi == nil {
				if ifi, err = os.Stat(in.path); err != nil {
					continue
				}
			}
			if os.SameFile(fi, ifi) {
				return "", fmt.Errorf("output %s is the input file %s; choose another output path", out, in.path)
			}
		}
		mode = fi.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("output %s: %w", out, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("output %s: %w", out, err)
	}
	if err = tmp.Chmod(mode); err == nil {
		err = fsutil.WriteSyncClose(tmp, []byte(doc))
	} else {
		err = errors.Join(err, tmp.Close())
	}
	if err == nil {
		err = os.Rename(tmp.Name(), target)
	}
	if err != nil {
		if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
		return "", fmt.Errorf("output %s: %w", out, err)
	}
	return out, nil
}
