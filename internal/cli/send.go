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
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/htmltext"
	"github.com/thorstenkramm/mailfire/internal/inliner"
	"github.com/thorstenkramm/mailfire/internal/logging"
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

	// The logfile is opened before any send attempt (FR-3): an unusable
	// path is a configuration error and aborts the run.
	var logger *logging.Logger
	if cfg.Logfile != "" {
		logger, err = logging.Open(cfg.Logfile, cfg.LogLevel, cfg.LogMaxSize)
		if err != nil {
			fmt.Fprintf(stderr, "mailfire: %v\n", err)
			return ExitConfig
		}
		defer logger.Close()
	}

	resp, err := client.SendMessage(context.Background(), msg)
	if logger != nil {
		logAttempt(logger, msg, opts.Test, resp, err, stderr)
	}
	if err != nil {
		return reportSendError(cfg, opts, msg, logger, err, stderr)
	}
	if opts.Test {
		// Test mode (FR-1, Decision Q5): Mailgun accepted and validated
		// the request but delivers nothing — say so explicitly, so the
		// message id is not mistaken for proof of delivery.
		fmt.Fprintf(stdout, "accepted in test mode, message id %s (validated only, nothing was delivered)\n", resp.ID)
	} else {
		fmt.Fprintln(stdout, resp.ID)
	}
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

	// --preheader and --footer only reach here with --md or --html
	// (checkFlags). The footer is trusted HTML read from a file and
	// inserted into the template as is (FR-1, FR-7).
	var footer render.HTML
	if opts.Footer != "" {
		footer, err = readFooter(opts.Footer)
		if err != nil {
			return nil, err
		}
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
			Subject:   opts.Subject,
			Preheader: opts.Preheader,
			Body:      render.HTML(fragment),
			Footer:    footer,
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
			Subject:   opts.Subject,
			Preheader: opts.Preheader,
			Body:      render.HTML(body),
			Footer:    footer,
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
// File.Reader is consumed once and holds an *os.File, so the Message
// cannot be re-encoded after a send attempt. Deferral (task 14) therefore
// persists the encoded request body (Message.Encoded), never the Message
// itself (review 09).
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
// regular files; kind ("attachment", "image", "footer") prefixes the
// errors.
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

// readFooter reads the --footer HTML fragment (~ expanded). The content
// is trusted markup: it is inserted into the base template as is
// (render.HTML), never escaped. Like attachments and images, only
// regular files are accepted (a FIFO would block the send); a missing,
// unreadable or non-regular file fails with the resolved path.
func readFooter(p string) (render.HTML, error) {
	f, err := openRegular("footer", p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("footer %s: %v", f.Name(), err)
	}
	return render.HTML(data), nil
}

// logAttempt writes exactly one log line for the attempt when logging is
// enabled (at the error level, successful sends are skipped by the logger
// itself). Message ID, status code, and response body come from the
// Mailgun client and are already redacted there, so the API key can never
// reach the logfile. On failure there is usually no message ID; it is
// logged as "". A logging failure is reported on stderr but never changes
// the exit code: the send outcome stays authoritative.
func logAttempt(logger *logging.Logger, msg *mailgun.Message, test bool,
	resp *mailgun.SendResponse, sendErr error, stderr io.Writer) {
	a := logging.Attempt{
		Time:    time.Now(),
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Size:    int64(msg.Size),
		Status:  0,
		Success: sendErr == nil,
		Test:    test,
	}
	if sendErr == nil {
		a.MessageID = resp.ID
		a.Status = resp.StatusCode
		a.Response = resp.RawBody
	} else if merr, ok := mailgun.As(sendErr); ok {
		a.Status = merr.StatusCode
		a.Response = merr.Message // redacted by the client
	} else {
		a.Response = sendErr.Error()
	}
	if err := logger.Log(a); err != nil {
		fmt.Fprintf(stderr, "mailfire: %v\n", err)
	}
}

// reportSendError maps a client failure to an exit code per PRD §8. A
// deferrable failure defers the message when deferral applies (FR-4): the
// exact encoded request is stored in the defer_dir and the exit code is
// 4. Test sends are never deferred (design decision, review 12): --test
// validates the request rather than delivering it, and a stored body
// would carry o:testmode=yes into a resend (FR-1, Decision Q5).
func reportSendError(cfg *config.Config, opts *SendOptions, msg *mailgun.Message,
	logger *logging.Logger, sendErr error, stderr io.Writer) int {
	fmt.Fprintf(stderr, "mailfire: send: %v\n", sendErr)
	switch {
	case mailgun.IsAuth(sendErr):
		return ExitAuth
	case !mailgun.IsDeferrable(sendErr):
		// Permanent failure (other 4xx, encoding error, cancellation):
		// retrying cannot succeed, so nothing is ever stored.
		return ExitSendFail
	case opts.Test:
		fmt.Fprintf(stderr, "mailfire: send: --test sends are never deferred (test mode only validates the request); the message was NOT saved\n")
		return ExitSendFail
	case opts.NoDefer:
		fmt.Fprintf(stderr, "mailfire: send: not deferring (--no-defer); the message was NOT saved\n")
		return ExitSendFail
	case !cfg.Defer:
		fmt.Fprintf(stderr, "mailfire: send: not deferring (defer = false in %s); the message was NOT saved\n", cfg.Path)
		return ExitSendFail
	}
	return deferMessage(cfg, msg, logger, stderr)
}

// deferMessage persists the encoded request in the defer_dir and enforces
// max_size, dropping the oldest deferred messages (FR-4); each drop is
// reported on stderr and logged when logging is enabled. When the message
// cannot be stored, the send degrades to a plain failure (exit 5) — the
// exit code must not promise a retry that will never happen.
func deferMessage(cfg *config.Config, msg *mailgun.Message, logger *logging.Logger, stderr io.Writer) int {
	if msg.TestMode {
		// Cannot happen: reportSendError never defers --test sends. Guard
		// anyway — a stored body would carry o:testmode=yes into resend.
		fmt.Fprintf(stderr, "mailfire: defer: test-mode sends are never deferred; the message was NOT saved\n")
		return ExitSendFail
	}
	if len(msg.Encoded) == 0 || msg.EncodedType == "" {
		// Cannot happen: a deferrable failure implies the request was
		// encoded (encoding errors are permanent). Guard anyway —
		// storing nothing would silently lose the mail.
		fmt.Fprintf(stderr, "mailfire: defer: no encoded request to store; the message was NOT saved and is lost\n")
		return ExitSendFail
	}
	store := &deferral.Store{Dir: cfg.DeferDir, MaxSize: cfg.DeferMaxSize}
	store.OnDrop = func(meta deferral.Metadata) {
		fmt.Fprintf(stderr, "mailfire: defer: dropped %s (to %s, subject %q, %d bytes): defer_dir exceeds max_size\n",
			meta.ID, strings.Join(meta.To, ", "), meta.Subject, meta.Size)
		if logger != nil {
			d := logging.Drop{Time: time.Now(), ID: meta.ID, To: meta.To, Subject: meta.Subject, Size: meta.Size}
			if err := logger.LogDrop(d); err != nil {
				fmt.Fprintf(stderr, "mailfire: %v\n", err)
			}
		}
	}
	meta, err := store.Save(msg.To, msg.Subject, msg.Encoded, msg.EncodedType)
	if err != nil {
		if meta.ID == "" {
			fmt.Fprintf(stderr, "mailfire: defer: %v; the message was NOT saved and is lost\n", err)
			return ExitSendFail
		}
		// The message is stored; only the max_size cleanup failed.
		fmt.Fprintf(stderr, "mailfire: defer: %v\n", err)
	}
	fmt.Fprintf(stderr, "mailfire: defer: message saved as %s in %s; retry with: mailfire resend\n", meta.ID, cfg.DeferDir)
	return ExitDeferred
}
