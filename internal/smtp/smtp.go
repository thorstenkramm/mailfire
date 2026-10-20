// Package smtp implements SMTP submission for Mailfire (task 31). It uses
// the standard library SMTP client with an explicit TLS mode: implicit TLS,
// required STARTTLS, or an explicitly configured plaintext local relay.
// It never downgrades a STARTTLS connection to plaintext and it never sends
// credentials on an unencrypted connection.
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	stdsmtp "net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/message"
	mailmime "github.com/thorstenkramm/mailfire/internal/mime"
	"github.com/thorstenkramm/mailfire/internal/spool"
)

const DefaultTimeout = 30 * time.Second

const (
	SecurityTLS      = "tls"
	SecuritySTARTTLS = "starttls"
	SecurityNone     = "none"
)

// Config is the validated SMTP submission configuration. Password is secret
// and is never included in errors or String output.
type Config struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	Timeout  time.Duration
}

// Error is an SMTP send/validation failure. It implements
// message.SendError; Message never contains the configured password.
type Error struct {
	Op      string
	Code    int
	Message string
	Kind    message.Kind
}

func (e *Error) Error() string {
	if e.Code > 0 {
		return fmt.Sprintf("smtp: %s: reply %d: %s", e.Op, e.Code, e.Message)
	}
	return fmt.Sprintf("smtp: %s: %s", e.Op, e.Message)
}
func (e *Error) SendKind() message.Kind { return e.Kind }
func (e *Error) ReplyCode() int         { return e.Code }
func (e *Error) Detail() string         { return e.Message }

// Client submits messages to a single SMTP endpoint.
type Client struct {
	config    Config
	tlsConfig *tls.Config
}

// Option customizes a Client. It exists primarily for local SMTP tests.
type Option func(*Client)

// WithTLSConfig replaces the TLS configuration used for implicit TLS and
// STARTTLS. Production callers use the secure default (normal system-root
// certificate and hostname verification); tests may provide a test CA.
func WithTLSConfig(config *tls.Config) Option {
	return func(c *Client) {
		if config != nil {
			c.tlsConfig = config.Clone()
		}
	}
}

func New(cfg Config, opts ...Option) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("smtp: host must not be empty")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("smtp: invalid port %d", cfg.Port)
	}
	switch cfg.Security {
	case SecurityTLS, SecuritySTARTTLS, SecurityNone:
	default:
		return nil, fmt.Errorf("smtp: invalid security mode %q", cfg.Security)
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("smtp: username and password must be set together")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	c := &Client{config: cfg}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

func (c *Client) String() string {
	return fmt.Sprintf("smtp.Client{host: %s, port: %d, security: %s, username: %t}", c.config.Host, c.config.Port, c.config.Security, c.config.Username != "")
}

// SendMessage builds MIME once into an owner-only spool, records it on msg
// for potential deferral, and streams it using the message envelope.
func (c *Client) SendMessage(ctx context.Context, msg *message.Message) (*message.SendResponse, error) {
	s, err := spool.Create()
	if err != nil {
		return nil, &Error{Op: "encode message", Message: err.Error(), Kind: message.KindPermanent}
	}
	id, err := mailmime.BuildTo(s, msg, time.Now())
	if err != nil {
		_ = s.Abort()
		return nil, &Error{Op: "encode message", Message: err.Error(), Kind: message.KindPermanent}
	}
	if err := s.Finish(); err != nil {
		_ = s.Abort()
		return nil, &Error{Op: "encode message", Message: err.Error(), Kind: message.KindPermanent}
	}
	msg.Spool, msg.EncodedType, msg.Size, msg.MessageID = s, mailmime.ContentType, int(s.Size()), id
	env, err := Envelope(msg)
	if err != nil {
		return nil, &Error{Op: "send message", Message: err.Error(), Kind: message.KindPermanent}
	}
	f, err := s.Open()
	if err != nil {
		return nil, &Error{Op: "send message", Message: err.Error(), Kind: message.KindPermanent}
	}
	defer f.Close()
	return c.SendReader(ctx, env, f, s.Size(), id)
}

// Envelope returns the bare SMTP envelope addresses for msg. It includes Bcc
// recipients while MIME headers intentionally omit Bcc.
func Envelope(msg *message.Message) (message.Envelope, error) {
	from, err := bareAddress(msg.From)
	if err != nil {
		return message.Envelope{}, fmt.Errorf("invalid sender: %w", err)
	}
	var recipients []string
	for _, list := range [][]string{msg.To, msg.Cc, msg.Bcc} {
		for _, s := range list {
			a, err := bareAddress(s)
			if err != nil {
				return message.Envelope{}, fmt.Errorf("invalid recipient: %w", err)
			}
			recipients = append(recipients, a)
		}
	}
	if len(recipients) == 0 {
		return message.Envelope{}, errors.New("no recipients")
	}
	return message.Envelope{From: from, Recipients: recipients}, nil
}

func bareAddress(s string) (string, error) {
	// mailbox.Parse has already validated CLI data, but net/mail's parser
	// is sufficient here and keeps this package independent of cli.
	at := strings.LastIndex(s, "<")
	if at >= 0 && strings.HasSuffix(strings.TrimSpace(s), ">") {
		s = strings.TrimSpace(s[at+1:])
		s = strings.TrimSuffix(s, ">")
	}
	if !strings.Contains(s, "@") || strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("%q", s)
	}
	return s, nil
}

// SendRaw submits a previously built RFC 5322 message. The caller supplies
// the persisted original envelope and Message-ID, so retries keep both.
func (c *Client) SendRaw(ctx context.Context, env message.Envelope, raw []byte, messageID string) (*message.SendResponse, error) {
	return c.SendReader(ctx, env, bytes.NewReader(raw), int64(len(raw)), messageID)
}

// SendReader submits a previously built RFC 5322 message from r. size is
// known from the spool or stored body.bin and is checked against an SMTP
// server's advertised SIZE limit before MAIL FROM or DATA.
func (c *Client) SendReader(ctx context.Context, env message.Envelope, r io.Reader, size int64, messageID string) (*message.SendResponse, error) {
	if size <= 0 || r == nil {
		return nil, &Error{Op: "resend message", Message: "nothing to replay: stored message is empty", Kind: message.KindPermanent}
	}
	if env.From == "" || len(env.Recipients) == 0 {
		return nil, &Error{Op: "resend message", Message: "nothing to replay: stored SMTP envelope is empty", Kind: message.KindPermanent}
	}
	cl, err := c.connect(ctx, "send message")
	if err != nil {
		return nil, err
	}
	// QUIT also after a rejection: nothing was sent, the session ends
	// cleanly, and the server releases the transaction.
	defer finish(cl)
	if ok, param := cl.Extension("SIZE"); ok && param != "" {
		limitText := strings.Fields(param)[0]
		if limit, parseErr := strconv.ParseInt(limitText, 10, 64); parseErr == nil && limit > 0 && size > limit {
			return nil, &Error{Op: "send message", Message: fmt.Sprintf("message size %d exceeds SMTP server SIZE limit %d", size, limit), Kind: message.KindPermanent}
		}
	}
	if err := cl.Mail(env.From); err != nil {
		return nil, c.replyError("send message", err)
	}
	// Do not enter DATA until every recipient was accepted. This policy
	// avoids an accidental partial delivery if one RCPT TO is rejected.
	for _, rcpt := range env.Recipients {
		if err := cl.Rcpt(rcpt); err != nil {
			return nil, c.replyError("recipient "+rcpt, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return nil, c.replyError("send message", err)
	}
	if _, err := io.Copy(w, r); err != nil {
		return nil, c.transportError("send message", errors.Join(err, w.Close()))
	}
	// Close ends DATA and reads the server's verdict. A lost reply here
	// leaves delivery uncertain: it is deferrable and a retry may
	// duplicate the message (documented).
	if err := w.Close(); err != nil {
		return nil, c.replyError("send message", err)
	}
	return &message.SendResponse{ID: messageID, Message: "accepted by SMTP server", StatusCode: 250, RawBody: "accepted by SMTP server"}, nil
}

// Validate connects, negotiates TLS as configured, and authenticates if
// credentials are configured. It never issues MAIL FROM, RCPT TO, or DATA.
func (c *Client) Validate(ctx context.Context) error {
	cl, err := c.connect(ctx, "validate connection")
	if err != nil {
		return err
	}
	finish(cl)
	return nil
}

// finish ends a session whose outcome is decided: QUIT politely, closing
// the connection if that fails. Neither can change the outcome — a
// message accepted after DATA stays accepted — so a close failure is only
// logged, like the Mailgun client's response-body close.
func finish(cl *stdsmtp.Client) {
	if err := cl.Quit(); err != nil {
		release(cl)
	}
}

// release closes c on a path whose error is already being returned; a
// close failure there adds nothing actionable and is only logged. An
// already closed connection is not a failure.
func release(c io.Closer) {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Printf("mailfire: smtp: closing connection: %v", err)
	}
}

func (c *Client) connect(ctx context.Context, op string) (*stdsmtp.Client, error) {
	addr := net.JoinHostPort(c.config.Host, strconv.Itoa(c.config.Port))
	d := net.Dialer{Timeout: c.config.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, c.transportError(op, err)
	}
	deadline := time.Now().Add(c.config.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		release(conn)
		return nil, c.transportError(op, err)
	}
	tlsConfig := c.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: c.config.Host, MinVersion: tls.VersionTLS12}
	} else if tlsConfig.ServerName == "" {
		tlsConfig = tlsConfig.Clone()
		tlsConfig.ServerName = c.config.Host
	}
	if c.config.Security == SecurityTLS {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			release(conn)
			return nil, c.transportError(op, err)
		}
		conn = tlsConn
	}
	cl, err := stdsmtp.NewClient(conn, c.config.Host)
	if err != nil {
		release(conn)
		return nil, c.replyError(op, err)
	}
	// net/smtp would greet with "EHLO localhost", which some submission
	// servers reject; identify with this host's name instead.
	if err := cl.Hello(localName()); err != nil {
		release(cl)
		return nil, c.replyError(op, err)
	}
	if c.config.Security == SecuritySTARTTLS {
		ok, _ := cl.Extension("STARTTLS")
		if !ok {
			release(cl)
			return nil, &Error{Op: op, Message: "server does not offer required STARTTLS", Kind: message.KindPermanent}
		}
		if err := cl.StartTLS(tlsConfig); err != nil {
			// A refusal reply (e.g. 454) or a failed handshake; never a
			// fallback to plaintext.
			release(cl)
			return nil, c.replyError(op, err)
		}
	}
	if c.config.Username != "" {
		if c.config.Security == SecurityNone {
			release(cl)
			return nil, &Error{Op: op, Message: "refusing to transmit SMTP credentials over plaintext transport", Kind: message.KindPermanent}
		}
		if err := c.authenticate(cl); err != nil {
			release(cl)
			var reply *textproto.Error
			if !errors.As(err, &reply) {
				// No usable mechanism, or net/smtp refusing to send
				// credentials: a configuration problem retries cannot fix.
				return nil, &Error{Op: op, Message: "authentication: " + err.Error(), Kind: message.KindPermanent}
			}
			return nil, c.replyError(op, err)
		}
	}
	return cl, nil
}

func (c *Client) authenticate(cl *stdsmtp.Client) error {
	ok, advertised := cl.Extension("AUTH")
	if !ok {
		return errors.New("server does not offer SMTP authentication")
	}
	mechanisms := strings.Fields(strings.ToUpper(advertised))
	has := func(name string) bool {
		for _, mechanism := range mechanisms {
			if mechanism == name {
				return true
			}
		}
		return false
	}
	// Select an advertised mechanism before issuing AUTH. Retrying after a
	// failed AUTH exchange is not reliable: many servers close or poison the
	// transaction, and a retry could expose credentials unexpectedly.
	switch {
	case has("PLAIN"):
		// PlainAuth verifies the server name and only sends credentials on
		// TLS. The caller has already rejected security=none.
		return cl.Auth(stdsmtp.PlainAuth("", c.config.Username, c.config.Password, c.config.Host))
	case has("LOGIN"):
		return cl.Auth(loginAuth{username: c.config.Username, password: c.config.Password})
	case has("CRAM-MD5"):
		return cl.Auth(stdsmtp.CRAMMD5Auth(c.config.Username, c.config.Password))
	default:
		return fmt.Errorf("server offers unsupported SMTP authentication mechanisms: %s", advertised)
	}
}

// loginAuth implements the widely deployed LOGIN SASL mechanism. LOGIN
// sends credentials in base64 as part of the SMTP exchange; callers invoke
// it only after TLS has been established, so the credentials are encrypted
// on the wire. It intentionally refuses server mechanism substitution.
type loginAuth struct {
	username string
	password string
}

func (a loginAuth) Start(server *stdsmtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("LOGIN authentication requires TLS")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(fromServer))
	switch {
	case strings.Contains(prompt, "username") || strings.Contains(prompt, "user name"):
		return []byte(a.username), nil
	case strings.Contains(prompt, "password"):
		return []byte(a.password), nil
	default:
		// Some servers use an empty first challenge for the username and
		// label only the password prompt. Keep state unnecessary by using
		// the conventional ordering for an empty challenge.
		if len(fromServer) == 0 {
			return []byte(a.username), nil
		}
		return nil, fmt.Errorf("unexpected LOGIN authentication challenge")
	}
}

func (c *Client) replyError(op string, err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		kind := message.KindPermanent
		switch {
		case reply.Code == 530 && strings.Contains(strings.ToUpper(reply.Msg), "STARTTLS"):
			// "530 5.7.0 Must issue a STARTTLS command first": the
			// configured security mode is wrong, not the credentials.
			kind = message.KindPermanent
		case reply.Code == 535 || reply.Code == 534 || reply.Code == 530:
			kind = message.KindAuth
		case reply.Code >= 400 && reply.Code < 500:
			kind = message.KindDeferrable
		case reply.Code >= 500:
			kind = message.KindPermanent
		}
		return &Error{Op: op, Code: reply.Code, Message: reply.Msg, Kind: kind}
	}
	return c.transportError(op, err)
}

func (c *Client) transportError(op string, err error) error {
	var nerr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return &Error{Op: op, Message: "request canceled", Kind: message.KindPermanent}
	case errors.As(err, &nerr) && nerr.Timeout():
		return &Error{Op: op, Message: fmt.Sprintf("request timed out after %s", c.config.Timeout), Kind: message.KindDeferrable}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Op: op, Message: fmt.Sprintf("request timed out after %s", c.config.Timeout), Kind: message.KindDeferrable}
	case isTLSVerificationError(err):
		return &Error{Op: op, Message: "TLS verification failed: " + err.Error(), Kind: message.KindPermanent}
	default:
		return &Error{Op: op, Message: err.Error(), Kind: message.KindDeferrable}
	}
}

func isTLSVerificationError(err error) bool {
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return true
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return true
	}
	var hostname x509.HostnameError
	return errors.As(err, &hostname)
}

// localName is the EHLO identity: this host's name when it is a plausible
// DNS name, else "localhost".
func localName() string {
	h, err := os.Hostname()
	if err != nil || h == "" || len(h) > 253 {
		return "localhost"
	}
	for _, r := range h {
		if !isHostChar(r) {
			return "localhost"
		}
	}
	return h
}

func isHostChar(r rune) bool {
	return r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
