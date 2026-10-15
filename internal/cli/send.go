package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/htmltext"
	"github.com/thorstenkramm/mailfire/internal/inliner"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/markdown"
	"github.com/thorstenkramm/mailfire/internal/render"
)

// send composes a message (plain text, or multipart/alternative for
// --md, --html and --html-raw) from the parsed flags and the body read
// from stdin or --file, posts it via the Mailgun client, and maps the
// outcome to an exit code per PRD §8.
func send(cfg *config.Config, opts *SendOptions, rc *runContext, stdout, stderr io.Writer) int {
	msg, err := compose(cfg, opts, rc.stdin)
	if err != nil {
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitUsage
	}
	// Attachment and inline-image files stay open until the message is
	// encoded.
	defer closeFiles(msg.Attachments)
	defer closeFiles(msg.Inline)

	client, err := mailgun.New(cfg.APIKey, cfg.Domain, cfg.Region, rc.clientOpts...)
	if err != nil {
		// Cannot happen: config.Load already validated these fields.
		fmt.Fprintf(stderr, "mailfire: %v\n", err)
		return ExitConfig
	}

	resp, err := client.SendMessage(context.Background(), msg)
	if err != nil {
		return reportSendError(err, stderr)
	}
	fmt.Fprintln(stdout, resp.ID)
	return ExitOK
}

// compose builds the Mailgun message from flags and config. All failures
// are usage errors (exit 1): bad addresses, a missing sender, an
// unreadable body, or a body whose CSS cannot be inlined. Inlining is
// deterministic, so it fails the send instead of silently falling back
// to an uninlined (differently rendered) mail.
func compose(cfg *config.Config, opts *SendOptions, stdin io.Reader) (*mailgun.Message, error) {
	from := opts.From
	if from == "" {
		from = cfg.From
	}
	if from == "" {
		return nil, fmt.Errorf("no sender: set sending.from in the config file or pass --from")
	}
	fromAddr, err := parseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("invalid sender %q: %v", from, err)
	}

	to, err := parseAddresses("--to", opts.To)
	if err != nil {
		return nil, err
	}
	cc, err := parseAddresses("--cc", opts.Cc)
	if err != nil {
		return nil, err
	}
	bcc, err := parseAddresses("--bcc", opts.Bcc)
	if err != nil {
		return nil, err
	}

	// A line break in a header value is never legitimate and could be
	// used for header injection; refuse it rather than rely on Mailgun.
	if strings.ContainsAny(opts.Subject, "\r\n") {
		return nil, errors.New("invalid --subject: must not contain line breaks")
	}

	body, err := readBody(opts.File, stdin)
	if err != nil {
		return nil, err
	}
	// Mailgun requires a text or html part and answers an empty body with
	// a 400; fail locally with a clear message instead.
	if body == "" {
		return nil, errors.New("empty message body: pipe the body on stdin or use --file")
	}

	msg := &mailgun.Message{
		From:     fromAddr,
		To:       to,
		Cc:       cc,
		Bcc:      bcc,
		Subject:  opts.Subject,
		TestMode: opts.Test,
	}
	switch opts.Format {
	case "md":
		// Multipart/alternative (FR-1): the Markdown rendered into the
		// base template as the HTML part, plus a plain-text part derived
		// from the same source. The template's CSS is inlined after
		// rendering (FR-7). Local images become inline parts referenced
		// via cid: (Decision Q4); the cid: rewrite happens on the
		// Markdown fragment before inlining, and the inliner only
		// touches style attributes, so the cid: srcs survive.
		cids, inline, err := inlineImages(body, opts.File)
		if err != nil {
			return nil, err
		}
		fragment := markdown.HTML(body)
		if cids != nil {
			fragment = markdown.HTMLWithImages(body, func(dest string) string {
				return cids[dest]
			})
		}
		html, err := inliner.Inline(render.Base(render.Data{
			Subject: opts.Subject,
			Body:    render.HTML(fragment),
		}))
		if err != nil {
			closeFiles(inline)
			return nil, err
		}
		msg.HTML = html
		msg.Text = markdown.Text(body)
		msg.Inline = inline
	case "html":
		// The fragment is trusted markup and goes into the base
		// template as is — inline styles and colors survive (the
		// template sets text color once on .content and inherits, and
		// existing style attributes win over inlined rules). The
		// template's CSS is inlined after rendering (FR-7).
		html, err := inliner.Inline(render.Base(render.Data{
			Subject: opts.Subject,
			Body:    render.HTML(body),
		}))
		if err != nil {
			return nil, err
		}
		msg.HTML = html
		msg.Text = htmltext.Text(body)
	case "html-raw":
		// A complete document, sent byte-identical: no template, no
		// inlining, no additions. Only the text part is derived.
		msg.HTML = body
		msg.Text = htmltext.Text(body)
	default:
		msg.Text = body
	}
	// Local image srcs in --html and --html-raw bodies are left
	// untouched: cid: rewriting is scoped to --md (task 10). --html-raw
	// must be sent byte-identical, so rewriting is impossible there by
	// definition, and both formats are trusted markup whose author can
	// attach files explicitly with --attach.

	// Opened last, so no earlier failure has to close them; from here on
	// the caller owns the handles.
	attachments, err := openAttachments(opts.Attach)
	if err != nil {
		return nil, err
	}
	msg.Attachments = attachments
	return msg, nil
}

// openAttachments resolves the --attach paths into message attachments.
// A leading ~ is expanded; the attachment's filename is the path's base
// name. Content type is left empty: the Mailgun client sniffs it from
// the first bytes of the part. Files are opened for streaming (the
// client io.Copies them into the request) and must be closed by the
// caller via closeFiles once the message is encoded. On error, any
// files opened so far are closed.
//
// All files are open at once, so the number of --attach flags is bounded
// by the open-file limit (ulimit -n, 256 by default on macOS); exceeding
// it fails with "too many open files" naming the path. Mailgun's 25 MB
// message limit makes that far beyond any realistic use.
//
// TODO(review 09): File.Reader is consumed once and holds an *os.File.
// Deferral (task 14) must persist the attachment bytes (or paths, if
// re-reading them on resend is acceptable), not the Message itself.
func openAttachments(paths []string) ([]mailgun.File, error) {
	files := make([]mailgun.File, 0, len(paths))
	for _, p := range paths {
		f, err := openAttachment(p)
		if err != nil {
			closeFiles(files)
			return nil, err
		}
		files = append(files, mailgun.File{Name: filepath.Base(f.Name()), Reader: f})
	}
	return files, nil
}

// openAttachment opens one --attach path (~ expanded, symlinks followed)
// and accepts only regular files. The Stat before Open refuses FIFOs
// early, because opening one blocks until a writer appears; the Stat on
// the open handle is the authoritative check against the path changing
// in between.
func openAttachment(p string) (*os.File, error) {
	return openRegular("attachment", p)
}

// openImage opens one inline image path; the rules are exactly those of
// attachments, only the error prefix differs.
func openImage(p string) (*os.File, error) {
	return openRegular("image", p)
}

// openRegular opens path (~ expanded) for streaming and accepts only
// regular files; kind ("attachment", "image") prefixes the errors.
func openRegular(kind, p string) (*os.File, error) {
	if p == "" {
		return nil, fmt.Errorf("%s: empty path", kind)
	}
	path := config.ExpandPath(p)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v", kind, path, err)
	}
	if err := checkRegular(kind, path, fi); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v", kind, path, err)
	}
	fi, err = f.Stat()
	if err == nil {
		err = checkRegular(kind, path, fi)
	} else {
		err = fmt.Errorf("%s %s: %v", kind, path, err)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// checkRegular refuses directories, devices, FIFOs and sockets.
func checkRegular(kind, path string, fi os.FileInfo) error {
	switch {
	case fi.IsDir():
		return fmt.Errorf("%s %s: is a directory, not a file", kind, path)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%s %s: not a regular file", kind, path)
	}
	return nil
}

// closeFiles closes every attachment reader that is an io.Closer. The
// message has been encoded (or abandoned) by the time this runs.
func closeFiles(files []mailgun.File) {
	for _, f := range files {
		if c, ok := f.Reader.(io.Closer); ok {
			c.Close()
		}
	}
}

// parseAddress validates one address and returns its normalized form:
// "user@example.com" stays as-is, `"Doe, John" <john@example.com>` keeps
// its (quoted) display name.
func parseAddress(s string) (string, error) {
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", err
	}
	if addr.Name == "" {
		return addr.Address, nil
	}
	return addr.String(), nil
}

// parseAddresses validates a repeatable recipient flag. Commas are not
// treated as separators (Decision Q1), so each flag value is exactly one
// address.
func parseAddresses(flagName string, values []string) ([]string, error) {
	addrs := make([]string, 0, len(values))
	for _, v := range values {
		addr, err := parseAddress(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s address %q: %v", flagName, v, err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

// readBody reads the message body from file (~ is expanded) or, when file
// is empty, from stdin. --file wins: stdin is not read at all then.
//
// TODO(review 04): when stdin is an interactive terminal, ReadAll waits
// for Ctrl-D without a prompt. Detecting a TTY (and erroring or hinting)
// is a UX decision beyond this task's scope.
func readBody(file string, stdin io.Reader) (string, error) {
	if file == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading body from stdin: %v", err)
		}
		return string(data), nil
	}
	path := config.ExpandPath(file)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading body file %s: %v", path, err)
	}
	return string(data), nil
}

// reportSendError maps a client failure to an exit code per PRD §8.
// Deferral is not implemented yet, so a deferrable failure exits 4 with an
// explicit note instead of storing the message.
func reportSendError(err error, stderr io.Writer) int {
	switch {
	case mailgun.IsAuth(err):
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitAuth
	case mailgun.IsDeferrable(err):
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		fmt.Fprintf(stderr, "mailfire: send: deferral is not implemented yet; the message was NOT saved and is lost\n")
		return ExitDeferred
	default:
		fmt.Fprintf(stderr, "mailfire: send: %v\n", err)
		return ExitSendFail
	}
}
