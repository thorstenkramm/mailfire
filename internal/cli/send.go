package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/htmltext"
	"github.com/thorstenkramm/mailfire/internal/inliner"
	"github.com/thorstenkramm/mailfire/internal/logging"
	"github.com/thorstenkramm/mailfire/internal/mailbox"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/markdown"
	"github.com/thorstenkramm/mailfire/internal/message"
	"github.com/thorstenkramm/mailfire/internal/render"
	"github.com/thorstenkramm/mailfire/internal/spool"
)

// send composes a message (plain text, or multipart/alternative for
// --md, --html and --html-raw) from the parsed flags and the body read
// from stdin or --file, sends it through the configured provider, and
// maps the outcome to an exit code per PRD §8.
func send(cfg *config.Config, opts *SendOptions, rc *runContext, res *sendResult, stdout, stderr io.Writer) int {
	msg, err := compose(cfg, opts, rc.stdin)
	if err != nil {
		printfOutput(stderr, "mailfire: send: %v\n", err)
		res.setError(errUsage, err)
		return ExitUsage
	}
	defer closeFiles(msg.Attachments)
	defer closeFiles(msg.Inline)
	return sendMessage(cfg, opts, rc, msg, res, stdout, stderr)
}

// sendMessage sends an already composed message: the send subcommand's
// pipeline after composition, shared with run (task 33), which composes
// its report itself. The caller owns attachment and inline-image files
// and keeps them open until this returns.
func sendMessage(cfg *config.Config, opts *SendOptions, rc *runContext, msg *mailgun.Message,
	res *sendResult, stdout, stderr io.Writer) int {
	// SMTP and Gmail have no Mailgun-style validation-only mode: a "test"
	// would deliver real email, so it is refused before anything happens.
	if cfg.Provider != config.ProviderMailgun && opts.Test {
		err := errTestUnsupported(cfg.Provider)
		printfOutput(stderr, "mailfire: send: %v\n", err)
		res.setError(errUsage, err)
		return ExitUsage
	}

	// The logfile is opened before any send attempt (FR-3): an unusable
	// path is a configuration error and aborts the run.
	delivery, err := prepareDelivery(cfg, opts, rc)
	if err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
		res.setError(errConfig, err)
		return ExitConfig
	}
	defer closeReported(delivery, stderr)
	return delivery.sendMessage(rc, msg, res, stdout, stderr)
}

func (p *preparedDelivery) sendMessage(rc *runContext, msg *mailgun.Message, res *sendResult, stdout, stderr io.Writer) int {
	cfg, opts, logger := &p.cfg, &p.opts, p.logger
	// The provider spools the message to a temporary file; SIGINT/SIGTERM
	// end the process without running deferred calls, so they remove it
	// first. Deferred first, the handler stays until the spool is gone.
	defer spool.CleanupOnSignal()()

	// Notification deduplication (task 24) is opt-in via --dedup-key.
	// Test sends deliver nothing, so they neither check nor start the
	// cooldown.
	var n *notification
	if opts.DedupKey != "" && opts.Test {
		printfOutput(stderr, "mailfire: send: --test: notification deduplication not applied (test sends neither check nor start the cooldown)\n")
	} else if opts.DedupKey != "" {
		var code int
		var done bool
		if n, code, done = startNotification(cfg, opts, msg, rc, res, stdout, stderr); done {
			return code
		}
		defer n.close()
	}

	// The provider sets msg.Spool. releaseSpool removes it (after a
	// deferral it is already adopted, and removing finds nothing). It is
	// deferred before the send so a panic cleans up too, and called as soon
	// as the outcome is decided — before any output, because writing to a
	// closed stdout or stderr pipe kills the process with SIGPIPE, which no
	// deferred call survives.
	releaseSpool := sync.OnceFunc(func() {
		if msg.Spool == nil {
			return
		}
		if err := msg.Spool.Abort(); err != nil {
			printfOutput(stderr, "mailfire: removing message spool: %v\n", err)
		}
	})
	defer releaseSpool()
	resp, err, setupErr := p.provider.send(msg)
	if setupErr != nil || err == nil {
		releaseSpool() // nothing to defer: sent, or not sent at all
	}
	if setupErr != nil {
		// Nothing was sent: the provider is not usable as configured.
		printfOutput(stderr, "mailfire: send: %v\n", setupErr)
		res.setError(errConfig, setupErr)
		if n != nil {
			n.notDelivered()
		}
		return ExitConfig
	}
	if logger != nil {
		logAttempt(logger, msg, opts.Test, resp, err, stderr)
	}
	if err != nil {
		code := reportSendError(cfg, opts, msg, logger, n, err, res, stderr, releaseSpool)
		releaseSpool() // after a deferral: already adopted, nothing left to remove
		if n != nil {
			n.notDelivered()
		}
		return code
	}
	if n != nil {
		n.delivered()
	}
	res.MessageID = resp.ID
	res.Outcome = "sent"
	if opts.Test {
		res.Outcome = "accepted"
		// Test mode (FR-1, Decision Q5): Mailgun accepted and validated
		// the request but delivers nothing — say so explicitly, so the
		// message id is not mistaken for proof of delivery.
		printfOutput(stdout, "accepted in test mode, message id %s (validated only, nothing was delivered)\n", resp.ID)
	} else {
		printlnOutput(stdout, resp.ID)
	}
	return ExitOK
}

// compose builds the Mailgun message from flags and config. All failures
// are usage errors (exit 1): bad addresses, a missing sender, an
// unreadable body, or a body whose CSS cannot be inlined. Inlining is
// deterministic, so it fails the send instead of silently falling back
// to an uninlined (differently rendered) mail.
func compose(cfg *config.Config, opts *SendOptions, stdin io.Reader) (*mailgun.Message, error) {
	return composePrepared(cfg, opts, stdin, nil)
}

func composePrepared(cfg *config.Config, opts *SendOptions, stdin io.Reader, rendering *preparedRendering) (*mailgun.Message, error) {
	from := opts.From
	if from == "" {
		from = cfg.From
	}
	if from == "" {
		return nil, fmt.Errorf("no sender: set sending.from in the config file or MAILFIRE_SENDING_FROM, or pass --from")
	}
	fromAddr, err := parseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("invalid sender %q: %w", from, err)
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

	if err := checkSubject(opts.Subject); err != nil {
		return nil, err
	}

	body, err := readMessageBody(opts, stdin)
	if err != nil {
		return nil, err
	}
	if body == "" && len(opts.Attach) == 0 {
		return nil, errors.New("mail body missing: use stdin, --file or --message")
	}

	msg := &mailgun.Message{
		From:     fromAddr,
		To:       to,
		Cc:       cc,
		Bcc:      bcc,
		Subject:  opts.Subject,
		TestMode: opts.Test,
	}
	if rendering == nil {
		rendering, err = prepareRendering(cfg, opts)
		if err != nil {
			return nil, err
		}
	}
	msg.HTML, msg.Text, msg.Inline, err = rendering.renderMessage(opts, body)
	if err != nil {
		return nil, err
	}

	// Opened last, so no earlier failure has to close them; from here on
	// the caller owns the handles.
	attachments, err := openAttachments(opts.Attach)
	if err != nil {
		closeFiles(msg.Inline)
		return nil, err
	}
	msg.Attachments = attachments
	return msg, nil
}

// checkSubject refuses a subject with line breaks. A line break in a
// header value is never legitimate and could be used for header
// injection; refuse it rather than rely on Mailgun.
func checkSubject(subject string) error {
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("invalid --subject: must not contain line breaks")
	}
	return nil
}

// readMessageBody reads the body from --message, --file or stdin.
//
// Bodies piped on Windows or saved by Windows editors use CRLF line
// endings. The Markdown renderer and the HTML-to-text derivation
// normalize CRLF internally; normalize here too, so --txt and --html
// compose the same message no matter which platform produced the
// body. --html-raw is exempt: it is sent byte-identical (FR-1).
func readMessageBody(opts *SendOptions, stdin io.Reader) (string, error) {
	body := opts.Message
	if !opts.MessageSet {
		var err error
		if body, err = readBody(opts.File, stdin); err != nil {
			return "", err
		}
	}
	if opts.Format != "html-raw" {
		body = strings.ReplaceAll(body, "\r\n", "\n")
	}
	return body, nil
}

// templateSelection returns the effective template name and custom
// template file: --template and --template-file override the config's
// selection (default "card").
func templateSelection(cfg *config.Config, opts *SendOptions) (name, file string) {
	name, file = cfg.Template, cfg.TemplateFile
	if opts.Template != "" {
		name, file = opts.Template, ""
	}
	if opts.TemplateFile != "" {
		file = opts.TemplateFile
	}
	return name, file
}

// preparedRendering owns the layout and footer snapshot. Message-dependent
// fields (subject and body) are supplied only when the job has completed.
type preparedRendering struct {
	name   string
	layout *render.Custom
	footer render.HTML
}

func prepareRendering(cfg *config.Config, opts *SendOptions) (*preparedRendering, error) {
	// --preheader and --footer only reach here with --md or --html
	// (checkFlags). The footer is trusted HTML read from a file and
	// inserted into the template as is (FR-1, FR-7).
	p := &preparedRendering{}
	var err error
	if opts.Footer != "" {
		if p.footer, err = readFooter(opts.Footer); err != nil {
			return nil, err
		}
	}
	var file string
	p.name, file = templateSelection(cfg, opts)
	if file != "" && (opts.Format == "md" || opts.Format == "html") {
		p.layout, err = render.LoadFile(config.ExpandPath(file))
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *preparedRendering) renderHTML(subject, preheader, fragment string) (string, error) {
	d := render.Data{Template: p.name, Subject: subject, Preheader: preheader, Body: render.HTML(fragment), Footer: p.footer}
	var doc string
	if p.layout != nil {
		var err error
		if doc, err = p.layout.Render(d); err != nil {
			return "", err
		}
	} else {
		doc = render.Base(d)
	}
	return inliner.Inline(doc)
}

// renderMessage is shared by sending and previews. The caller owns the
// returned inline files and must close them after encoding or abandoning the
// message. CSS inlining errors fail rendering rather than changing the layout.
func (p *preparedRendering) renderMessage(opts *SendOptions, body string) (html, text string, inline []mailgun.File, err error) {
	renderHTML := func(fragment string) (string, error) {
		return p.renderHTML(opts.Subject, opts.Preheader, fragment)
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
		cids, files, err := inlineImages(body, opts.File)
		if err != nil {
			return "", "", nil, err
		}
		fragment := markdown.HTML(body)
		if cids != nil {
			fragment = markdown.HTMLWithImages(body, func(dest string) string {
				return cids[dest]
			})
		}
		html, err := renderHTML(fragment)
		if err != nil {
			closeFiles(files)
			return "", "", nil, err
		}
		return html, markdown.Text(body), files, nil
	case "html":
		// The fragment is trusted markup and goes into the base
		// template as is — inline styles and colors survive (the
		// template sets text color once on .content and inherits, and
		// existing style attributes win over inlined rules). The
		// template's CSS is inlined after rendering (FR-7).
		html, err := renderHTML(body)
		if err != nil {
			return "", "", nil, err
		}
		return html, htmltext.Text(body), nil, nil
	case "html-raw":
		// A complete document, sent byte-identical: no template, no
		// inlining, no additions. Only the text part is derived.
		return body, htmltext.Text(body), nil, nil
	}
	// Local image srcs in --html and --html-raw bodies are left
	// untouched: cid: rewriting is scoped to --md (task 10). --html-raw
	// must be sent byte-identical, so rewriting is impossible there by
	// definition, and both formats are trusted markup whose author can
	// attach files explicitly with --attach.
	return "", body, nil, nil
}

// openAttachments resolves the --attach paths into message attachments.
// A leading ~ is expanded; the attachment's filename is the path's base
// name. Content type is left empty: the provider encoder sniffs it from
// the first bytes of the part. Files are opened for streaming into the
// request spool and must be closed by the
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
// adopts the encoded request spool, never the Message
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
// in between. The file must keep that size while it is read (sizedFile).
func openAttachment(p string) (*sizedFile, error) {
	return openSized("attachment", p)
}

// openImage opens one inline image path; the rules are exactly those of
// attachments, only the error prefix differs.
func openImage(p string) (*sizedFile, error) {
	return openSized("image", p)
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
		return nil, fmt.Errorf("%s %s: %w", kind, path, err)
	}
	if err := checkRegular(kind, path, fi); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", kind, path, err)
	}
	fi, err = f.Stat()
	if err == nil {
		err = checkRegular(kind, path, fi)
	} else {
		err = fmt.Errorf("%s %s: %w", kind, path, err)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
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
			closeReported(c, os.Stderr)
		}
	}
}

// parseAddress validates one address and returns its normalized form:
// "user@example.com" stays as-is, `"Doe, John" <john@example.com>` keeps
// its (quoted) display name.
func parseAddress(s string) (string, error) {
	addr, err := mailbox.Parse(s)
	if err != nil {
		return "", err
	}
	if addr.Name == "" {
		// String re-quotes local parts when needed; strip only its outer
		// angle brackets to preserve the bare-mailbox convention.
		return strings.TrimSuffix(strings.TrimPrefix(addr.String(), "<"), ">"), nil
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
			return nil, fmt.Errorf("invalid %s address %q: %w", flagName, v, err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

// readBody reads the message body from file (~ is expanded) or, when file
// is empty, from stdin. --file wins: stdin is not read at all then.
func readBody(file string, stdin io.Reader) (string, error) {
	if file == "" {
		if readerIsTerminal(stdin) {
			return "", nil
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading body from stdin: %w", err)
		}
		return string(data), nil
	}
	f, err := openRegular("reading body file", file)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(f)
	err = errors.Join(err, f.Close())
	if err != nil {
		return "", fmt.Errorf("reading body file %s: %w", f.Name(), err)
	}
	return string(data), nil
}

// readerIsTerminal distinguishes interactive input from empty pipes/files.
// The optional method allows tests to model a terminal without a real TTY.
func readerIsTerminal(r io.Reader) bool {
	if terminal, ok := r.(interface{ IsTerminal() bool }); ok {
		return terminal.IsTerminal()
	}
	if f, ok := r.(interface{ Fd() uintptr }); ok {
		return isatty.IsTerminal(f.Fd())
	}
	return false
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
	data, err := io.ReadAll(f)
	err = errors.Join(err, f.Close())
	if err != nil {
		return "", fmt.Errorf("footer %s: %w", f.Name(), err)
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
	logOutcome(logger, logging.Attempt{
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Size:    int64(msg.Size),
		Test:    test,
	}, resp, sendErr, stderr)
}

// logOutcome completes a with the outcome of a sending attempt — time,
// success, message ID, status code, and the redacted response or error
// message — and logs it. It is shared by send and resend so both write
// identical field sets (FR-3). A logging failure is reported on stderr
// only.
func logOutcome(logger *logging.Logger, a logging.Attempt,
	resp *mailgun.SendResponse, sendErr error, stderr io.Writer) {
	a.Time = time.Now()
	a.Success = sendErr == nil
	a.Status = 0
	if sendErr == nil {
		a.MessageID = resp.ID
		a.Status = resp.StatusCode
		a.Response = resp.RawBody
	} else if serr, ok := message.As(sendErr); ok {
		a.Status = serr.ReplyCode()
		a.Response = serr.Detail() // providers redact credentials
	} else {
		a.Response = sendErr.Error()
	}
	if err := logger.Log(a); err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
	}
}

// reportSendError maps a client failure to an exit code per PRD §8. A
// deferrable failure defers the message when deferral applies (FR-4): the
// exact encoded request is stored in the defer_dir and the exit code is
// 4. Test sends are never deferred (design decision, review 12): --test
// validates the request rather than delivering it, and a stored body
// would carry o:testmode=yes into a resend (FR-1, Decision Q5).
//
// When the message will not be deferred, releaseSpool runs before any
// output (see sendMessage: a write to a closed pipe ends the process).
func reportSendError(cfg *config.Config, opts *SendOptions, msg *mailgun.Message,
	logger *logging.Logger, n *notification, sendErr error, res *sendResult, stderr io.Writer, releaseSpool func()) int {
	deferring, code, note := deferralFor(cfg, opts, sendErr)
	if !deferring {
		releaseSpool()
	}
	printfOutput(stderr, "mailfire: send: %v\n", sendErr)
	res.setError(sendErrorCode(sendErr), sendErr)
	if !deferring {
		if note != "" {
			printfOutput(stderr, "mailfire: send: %s; the message was NOT saved\n", note)
		}
		return code
	}
	return deferMessage(cfg, msg, sendErr, logger, n, res, stderr)
}

// deferralFor decides what a failed send leads to: deferral, or the exit
// code of a failure that is not deferred and, where the user may have
// expected a deferral, the reason it did not happen.
func deferralFor(cfg *config.Config, opts *SendOptions, sendErr error) (deferring bool, code int, note string) {
	switch {
	case message.IsAuth(sendErr):
		return false, ExitAuth, ""
	case !message.IsDeferrable(sendErr):
		// Permanent failure (other 4xx, encoding error, cancellation):
		// retrying cannot succeed, so nothing is ever stored.
		return false, ExitSendFail, ""
	case opts.Test:
		return false, ExitSendFail, "--test sends are never deferred (test mode only validates the request)"
	case opts.NoDefer:
		return false, ExitSendFail, "not deferring (--no-defer)"
	case !cfg.Defer:
		return false, ExitSendFail, fmt.Sprintf("not deferring (%s)", deferDisabledBy(cfg))
	}
	return true, 0, ""
}

// deferDisabledBy describes the source that disabled deferral, for
// diagnostics: the config file, or the MAILFIRE_DEFER_DEFER variable
// (with environment-only configuration there is no file to name).
func deferDisabledBy(cfg *config.Config) string {
	if env := cfg.EnvSource["defer.defer"]; env != "" {
		return env + "=false"
	}
	return "defer = false in " + cfg.Path
}

// deferMessage adopts the encoded request spool into the defer_dir, recording
// sendErr as its last error, removes messages older than max_age, and
// enforces max_size, dropping the oldest deferred messages (FR-4); each
// removal is reported on stderr and logged when logging is enabled. When
// the message cannot be stored, the send degrades to a plain failure
// (exit 5) — the exit code must not promise a retry that will never
// happen. A message sent with --dedup-key (n non-nil) records its
// deduplication scope, so duplicates are skipped while it is queued.
func deferMessage(cfg *config.Config, msg *mailgun.Message, sendErr error, logger *logging.Logger,
	n *notification, res *sendResult, stderr io.Writer) int {
	if msg.TestMode {
		// Cannot happen: reportSendError never defers --test sends. Guard
		// anyway — a stored body would carry o:testmode=yes into resend.
		printfOutput(stderr, "mailfire: defer: test-mode sends are never deferred; the message was NOT saved\n")
		return ExitSendFail
	}
	if msg.Spool == nil || msg.Spool.Size() == 0 || msg.EncodedType == "" {
		// Cannot happen: a deferrable failure implies the request was
		// encoded (encoding errors are permanent). Guard anyway —
		// storing nothing would silently lose the mail.
		printfOutput(stderr, "mailfire: defer: no encoded request to store; the message was NOT saved and is lost\n")
		res.setError(errDeferFailed, errors.New("no encoded request to store; the message was NOT saved"))
		return ExitSendFail
	}
	store := &deferral.Store{Dir: cfg.DeferDir, MaxSize: cfg.DeferMaxSize, MaxAge: cfg.DeferMaxAge}
	store.OnDrop = func(meta deferral.Metadata) {
		printfOutput(stderr, "mailfire: defer: dropped %s (to %s, subject %q, %d bytes): defer_dir exceeds max_size\n",
			meta.ID, strings.Join(meta.To, ", "), meta.Subject, meta.Size)
		logDrop(logger, meta, false, time.Now(), stderr)
	}
	store.OnExpire = expireReporter("defer", logger, time.Now, stderr)
	meta, err := store.SaveMessageFile(deferMetadata(cfg, msg, sendErr, n), msg.Spool)
	if err != nil {
		if meta.ID == "" {
			printfOutput(stderr, "mailfire: defer: %v; the message was NOT saved and is lost\n", err)
			res.setError(errDeferFailed, fmt.Errorf("%w; the message was NOT saved", err))
			return ExitSendFail
		}
		// The message is stored; only the max_size cleanup failed.
		printfOutput(stderr, "mailfire: defer: %v\n", err)
	}
	printfOutput(stderr, "mailfire: defer: message saved as %s in %s; retry with: mailfire resend\n", meta.ID, cfg.DeferDir)
	res.Outcome = "deferred"
	res.DeferredID = meta.ID
	return ExitDeferred
}

// logDrop records a message removed from the defer_dir (max_size drop or
// max_age expiry) in the send log, when logging is enabled.
func logDrop(logger *logging.Logger, meta deferral.Metadata, expired bool, at time.Time, stderr io.Writer) {
	if logger == nil {
		return
	}
	d := logging.Drop{Time: at, ID: meta.ID, To: meta.To, Subject: meta.Subject, Size: meta.Size, Expired: expired}
	if err := logger.LogDrop(d); err != nil {
		printfOutput(stderr, "mailfire: %v\n", err)
	}
}

// expireReporter returns a deferral.Store OnExpire callback that reports
// each message removed for exceeding max_age on stderr (prefixed with
// the command name) and in the send log.
func expireReporter(cmd string, logger *logging.Logger, now func() time.Time, stderr io.Writer) func(deferral.Metadata) {
	return func(meta deferral.Metadata) {
		printfOutput(stderr, "mailfire: %s: expired %s (to %s, subject %q, %d bytes): deferred %s ago, older than max_age\n",
			cmd, meta.ID, strings.Join(meta.To, ", "), meta.Subject, meta.Size,
			now().Sub(meta.Timestamp).Round(time.Second))
		logDrop(logger, meta, true, now(), stderr)
	}
}
