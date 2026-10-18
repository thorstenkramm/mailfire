package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	stdsmtp "net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/message"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

const (
	testUser = "submit@example.com"
	testPass = "s3cret-password"
)

// newServer starts a mock SMTP server and a client for it with the given
// security mode; the client trusts the server's test certificate.
func newServer(t *testing.T, security string, configure func(*testutil.MockSMTP)) (*testutil.MockSMTP, *Client) {
	m, c, _ := newServerTLS(t, security, configure)
	return m, c
}

func newServerTLS(t *testing.T, security string, configure func(*testutil.MockSMTP)) (*testutil.MockSMTP, *Client, *tls.Config) {
	t.Helper()
	serverTLS, clientTLS := testutil.TLSPair(t)
	m := testutil.NewMockSMTP(t, func(m *testutil.MockSMTP) {
		m.TLSConfig = serverTLS
		m.Implicit = security == SecurityTLS
		m.StartTLS = security == SecuritySTARTTLS
		m.AuthMechs = []string{"PLAIN", "LOGIN"}
		m.Users = map[string]string{testUser: testPass}
		if configure != nil {
			configure(m)
		}
	})
	cfg := Config{Host: m.Host(), Port: m.Port(), Security: security, Timeout: 2 * time.Second}
	if security != SecurityNone {
		cfg.Username, cfg.Password = testUser, testPass
	}
	c, err := New(cfg, WithTLSConfig(clientTLS))
	if err != nil {
		t.Fatal(err)
	}
	return m, c, clientTLS
}

func testMessage() *message.Message {
	return &message.Message{
		From:    `"Ops Bot" <ops@example.com>`,
		To:      []string{"to@example.com"},
		Cc:      []string{"Carol <cc@example.com>"},
		Bcc:     []string{"hidden@example.com"},
		Subject: "Grüße ✓",
		Text:    "line one\n.leading dot must survive\n..two dots\n",
		HTML:    "<p>html</p>\n.<b>dot</b>",
		Attachments: []message.File{
			{Name: "report.txt", Reader: strings.NewReader("attachment\n.dotted\n")},
		},
	}
}

func TestSendSTARTTLSAuthMIMEFidelityAndBccPrivacy(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, func(m *testutil.MockSMTP) { m.RequireAuth = true })
	msg := testMessage()
	resp, err := c.SendMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	msgs := m.Messages()
	if len(msgs) != 1 {
		t.Fatalf("server accepted %d messages, want 1", len(msgs))
	}
	got := msgs[0]
	if !got.TLS || got.AuthUser != testUser {
		t.Errorf("TLS = %v, auth user = %q; want encrypted, authenticated session", got.TLS, got.AuthUser)
	}
	for _, cmd := range m.Commands() {
		if cmd.Verb == "AUTH" && !cmd.TLS {
			t.Error("credentials were sent before TLS")
		}
	}
	// Byte-exact after the server undoes dot-stuffing: CRLF everywhere and
	// lines starting with "." restored.
	if !bytes.Equal(got.Data, msg.Encoded) {
		t.Errorf("received DATA differs from the built message:\n%q\nwant\n%q", got.Data, msg.Encoded)
	}
	if got.BareLF {
		t.Error("DATA contains a bare LF")
	}
	if want := []string{"to@example.com", "cc@example.com", "hidden@example.com"}; strings.Join(got.To, ",") != strings.Join(want, ",") {
		t.Errorf("envelope recipients = %v, want %v", got.To, want)
	}
	if got.From != "ops@example.com" {
		t.Errorf("MAIL FROM = %q", got.From)
	}
	if bytes.Contains(got.Data, []byte("hidden@example.com")) || bytes.Contains(got.Data, []byte("Bcc:")) {
		t.Errorf("Bcc recipient exposed in the delivered message:\n%s", got.Data)
	}
	if !strings.HasPrefix(resp.ID, "<") || !bytes.Contains(got.Data, []byte("Message-ID: "+resp.ID+"\r\n")) {
		t.Errorf("reported message id %q is not the message's Message-ID", resp.ID)
	}
	if strings.Contains(c.String(), testPass) {
		t.Error("client String() leaks the password")
	}
}

func TestSendImplicitTLS(t *testing.T) {
	m, c := newServer(t, SecurityTLS, nil)
	if _, err := c.SendMessage(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if msgs := m.Messages(); len(msgs) != 1 || !msgs[0].TLS {
		t.Fatalf("messages = %+v, want one over TLS", msgs)
	}
}

func TestSendUnauthenticatedPlaintextRelay(t *testing.T) {
	m, c := newServer(t, SecurityNone, nil)
	if _, err := c.SendMessage(context.Background(), testMessage()); err != nil {
		t.Fatal(err)
	}
	if msgs := m.Messages(); len(msgs) != 1 || msgs[0].TLS || msgs[0].AuthUser != "" {
		t.Fatalf("messages = %+v, want one plaintext unauthenticated", msgs)
	}
}

func TestAuthMechanisms(t *testing.T) {
	for _, mech := range []string{"PLAIN", "LOGIN", "CRAM-MD5"} {
		t.Run(mech, func(t *testing.T) {
			m, c := newServer(t, SecuritySTARTTLS, func(m *testutil.MockSMTP) {
				m.AuthMechs = []string{mech}
				m.RequireAuth = true
			})
			if _, err := c.SendMessage(context.Background(), testMessage()); err != nil {
				t.Fatal(err)
			}
			if msgs := m.Messages(); len(msgs) != 1 || msgs[0].AuthUser != testUser {
				t.Fatalf("messages = %+v", msgs)
			}
		})
	}
}

func TestAuthFailureIsAuthError(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, func(m *testutil.MockSMTP) {
		m.Users = map[string]string{testUser: "other"}
	})
	_, err := c.SendMessage(context.Background(), testMessage())
	if !message.IsAuth(err) {
		t.Fatalf("error = %v, want auth", err)
	}
	if strings.Contains(err.Error(), testPass) {
		t.Errorf("error leaks the password: %v", err)
	}
	if m.Saw("MAIL") {
		t.Error("MAIL FROM sent after failed authentication")
	}
}

func TestUnsupportedAuthMechanismIsPermanent(t *testing.T) {
	_, c := newServer(t, SecuritySTARTTLS, func(m *testutil.MockSMTP) { m.AuthMechs = []string{"XOAUTH2"} })
	if err := c.Validate(context.Background()); !message.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent", err)
	}
}

func TestMissingSTARTTLSNeverFallsBack(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, func(m *testutil.MockSMTP) { m.StartTLS = false })
	_, err := c.SendMessage(context.Background(), testMessage())
	if !message.IsPermanent(err) || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error = %v, want permanent missing-STARTTLS error", err)
	}
	for _, v := range []string{"AUTH", "MAIL"} {
		if m.Saw(v) {
			t.Errorf("%s sent over plaintext after STARTTLS was unavailable", v)
		}
	}
}

func TestInvalidCertificateIsPermanent(t *testing.T) {
	for _, security := range []string{SecurityTLS, SecuritySTARTTLS} {
		t.Run(security, func(t *testing.T) {
			m, _ := newServer(t, security, nil)
			// A client with the system roots does not trust the test CA.
			c, err := New(Config{Host: m.Host(), Port: m.Port(), Security: security, Username: testUser, Password: testPass, Timeout: 2 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.SendMessage(context.Background(), testMessage())
			if !message.IsPermanent(err) || !strings.Contains(err.Error(), "TLS verification failed") {
				t.Fatalf("error = %v, want permanent TLS verification failure", err)
			}
			if m.Saw("AUTH") || len(m.Messages()) != 0 {
				t.Error("credentials or message sent despite an untrusted certificate")
			}
		})
	}
}

func TestHostnameMismatchIsPermanent(t *testing.T) {
	m, _ := newServer(t, SecurityTLS, nil)
	_, clientTLS := testutil.TLSPair(t) // trusts a different certificate
	clientTLS.ServerName = "smtp.example.com"
	c := mustNew(t, Config{Host: m.Host(), Port: m.Port(), Security: SecurityTLS, Timeout: 2 * time.Second}, WithTLSConfig(clientTLS))
	if err := c.Validate(context.Background()); !message.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent", err)
	}
}

// The mixed-recipient policy: a rejected RCPT TO aborts before DATA, so no
// recipient gets the message and a retry cannot duplicate it for anyone.
func TestRecipientRejectionAbortsBeforeData(t *testing.T) {
	for _, tc := range []struct {
		reply string
		kind  message.Kind
	}{
		{"550 5.1.1 no such user", message.KindPermanent},
		{"452 4.2.2 mailbox full", message.KindDeferrable},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			m, c := newServer(t, SecuritySTARTTLS, nil)
			m.SetRcptReply("cc@example.com", tc.reply)
			_, err := c.SendMessage(context.Background(), testMessage())
			serr, ok := message.As(err)
			if !ok || serr.SendKind() != tc.kind {
				t.Fatalf("error = %v, want kind %v", err, tc.kind)
			}
			if !strings.Contains(err.Error(), "cc@example.com") || serr.ReplyCode() == 0 {
				t.Errorf("error %v does not name the rejected recipient and reply code", err)
			}
			if m.Saw("DATA") || len(m.Messages()) != 0 {
				t.Error("DATA was sent although a recipient was rejected")
			}
		})
	}
}

func TestTemporaryDataFailureIsDeferrable(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, nil)
	m.SetDataReply("451 4.3.0 try again later", 0)
	if _, err := c.SendMessage(context.Background(), testMessage()); !message.IsDeferrable(err) {
		t.Fatalf("error = %v, want deferrable", err)
	}
}

func TestPermanentDataFailure(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, nil)
	m.SetDataReply("554 5.6.0 message rejected", 0)
	if _, err := c.SendMessage(context.Background(), testMessage()); !message.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent", err)
	}
}

// A lost final reply after DATA leaves delivery uncertain; it is deferrable
// (and may duplicate on retry, as documented).
func TestTimeoutIsDeferrable(t *testing.T) {
	m, _, clientTLS := newServerTLS(t, SecuritySTARTTLS, nil)
	m.SetDataReply("250 ok", 2*time.Second)
	c, err := New(Config{Host: m.Host(), Port: m.Port(), Security: SecuritySTARTTLS, Username: testUser, Password: testPass,
		Timeout: 200 * time.Millisecond}, WithTLSConfig(clientTLS))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.SendMessage(context.Background(), testMessage())
	if !message.IsDeferrable(err) || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Fatalf("error = %v, want deferrable timeout", err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Errorf("timeout took %s", time.Since(start))
	}
}

func TestGreetingTimeoutIsDeferrable(t *testing.T) {
	m := testutil.NewMockSMTP(t, func(m *testutil.MockSMTP) { m.GreetDelay = 2 * time.Second })
	c := mustNew(t, Config{Host: m.Host(), Port: m.Port(), Security: SecurityNone, Timeout: 200 * time.Millisecond})
	if err := c.Validate(context.Background()); !message.IsDeferrable(err) {
		t.Fatalf("error = %v, want deferrable", err)
	}
}

func TestConnectionRefusedIsDeferrable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v", ln.Addr())
	}
	port := addr.Port
	if err := ln.Close(); err != nil { // nothing listens on port any more
		t.Fatal(err)
	}
	c := mustNew(t, Config{Host: "127.0.0.1", Port: port, Security: SecurityNone, Timeout: time.Second})
	if err := c.Validate(context.Background()); !message.IsDeferrable(err) {
		t.Fatalf("error = %v, want deferrable", err)
	}
}

func TestCanceledIsPermanent(t *testing.T) {
	m := testutil.NewMockSMTP(t, func(m *testutil.MockSMTP) { m.GreetDelay = time.Second })
	c := mustNew(t, Config{Host: m.Host(), Port: m.Port(), Security: SecurityNone, Timeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Validate(ctx); !message.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent (canceled)", err)
	}
}

func TestValidateSendsNoMail(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, nil)
	if err := c.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.Saw("STARTTLS") || !m.Saw("AUTH") || !m.Saw("QUIT") {
		t.Errorf("validate did not negotiate TLS and authenticate: %+v", m.Commands())
	}
	for _, v := range []string{"MAIL", "RCPT", "DATA"} {
		if m.Saw(v) {
			t.Errorf("validate sent %s", v)
		}
	}
}

func TestCredentialsNeverSentInPlaintext(t *testing.T) {
	m := testutil.NewMockSMTP(t, func(m *testutil.MockSMTP) { m.AuthMechs = []string{"PLAIN"} })
	c, err := New(Config{Host: m.Host(), Port: m.Port(), Security: SecurityNone, Username: testUser, Password: testPass, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(context.Background()); !message.IsPermanent(err) {
		t.Fatalf("error = %v, want permanent refusal", err)
	}
	if m.Saw("AUTH") {
		t.Error("AUTH sent over plaintext")
	}
}

func TestSendRawReplaysStoredEnvelope(t *testing.T) {
	m, c := newServer(t, SecuritySTARTTLS, nil)
	raw := []byte("From: ops@example.com\r\nTo: to@example.com\r\nMessage-ID: <x@example.com>\r\n\r\nbody\r\n")
	env := message.Envelope{From: "ops@example.com", Recipients: []string{"to@example.com", "bcc@example.com"}}
	resp, err := c.SendRaw(context.Background(), env, raw, "<x@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "<x@example.com>" {
		t.Errorf("id = %q", resp.ID)
	}
	if got := m.Messages(); len(got) != 1 || !bytes.Equal(got[0].Data, raw) || len(got[0].To) != 2 {
		t.Fatalf("messages = %+v", got)
	}
	if _, err := c.SendRaw(context.Background(), message.Envelope{}, raw, ""); !message.IsPermanent(err) {
		t.Errorf("empty envelope: %v, want permanent", err)
	}
}

func TestReplyClassification(t *testing.T) {
	c := &Client{}
	for _, tc := range []struct {
		code int
		msg  string
		kind message.Kind
	}{
		{535, "5.7.8 bad credentials", message.KindAuth},
		{530, "5.7.0 Authentication required", message.KindAuth},
		{530, "5.7.0 Must issue a STARTTLS command first", message.KindPermanent},
		{421, "4.7.0 try later", message.KindDeferrable},
		{550, "5.1.1 unknown", message.KindPermanent},
	} {
		err := c.replyError("op", &textproto.Error{Code: tc.code, Msg: tc.msg})
		if serr, _ := message.As(err); serr == nil || serr.SendKind() != tc.kind {
			t.Errorf("%d %s: %v, want %v", tc.code, tc.msg, err, tc.kind)
		}
	}
}

func TestLoginAuth(t *testing.T) {
	a := loginAuth{username: "user", password: "secret"}
	if mechanism, initial, err := a.Start(&stdsmtp.ServerInfo{TLS: true}); err != nil || mechanism != "LOGIN" || initial != nil {
		t.Fatalf("Start() = %q, %q, %v", mechanism, initial, err)
	}
	if _, _, err := a.Start(&stdsmtp.ServerInfo{}); err == nil {
		t.Fatal("plaintext LOGIN Start() succeeded")
	}
	for _, tc := range []struct{ prompt, want string }{{"Username:", "user"}, {"Password:", "secret"}, {"", "user"}} {
		got, err := a.Next([]byte(tc.prompt), true)
		if err != nil || string(got) != tc.want {
			t.Errorf("Next(%q) = %q, %v; want %q", tc.prompt, got, err, tc.want)
		}
	}
	if got, err := a.Next(nil, false); err != nil || got != nil {
		t.Errorf("final Next() = %q, %v", got, err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Port: 25, Security: SecurityNone},
		{Host: "h", Port: 0, Security: SecurityNone},
		{Host: "h", Port: 25, Security: "ssl"},
		{Host: "h", Port: 25, Security: SecurityTLS, Username: "u"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
}

func mustNew(t *testing.T, cfg Config, opts ...Option) *Client {
	t.Helper()
	c, err := New(cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
