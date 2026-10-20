package testutil

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// SMTPMessage is one message a MockSMTP accepted.
type SMTPMessage struct {
	From     string   // MAIL FROM address
	To       []string // accepted RCPT TO addresses, in order
	Data     []byte   // DATA content as transmitted, CRLF kept, dot-stuffing removed
	BareLF   bool     // a DATA line ended in LF without CR
	TLS      bool     // the session was encrypted
	AuthUser string   // authenticated user, "" without AUTH
}

// SMTPCommand is one command a MockSMTP received.
type SMTPCommand struct {
	Verb string // upper case, e.g. "EHLO", "AUTH", "MAIL"
	TLS  bool   // received on an encrypted connection
}

// MockSMTP is a small SMTP submission server for tests: implicit TLS or
// STARTTLS, AUTH PLAIN/LOGIN/CRAM-MD5, scripted RCPT and DATA replies, and
// delays. Configure it in NewMockSMTP's callback; change replies later
// with the setters. Every command and accepted message is recorded.
type MockSMTP struct {
	Implicit    bool              // TLS from the first byte (port 465 style)
	StartTLS    bool              // advertise and accept STARTTLS
	TLSConfig   *tls.Config       // server certificate for Implicit/StartTLS
	AuthMechs   []string          // advertised AUTH mechanisms (after TLS); none: no AUTH
	Users       map[string]string // accepted credentials
	RequireAuth bool              // reply 530 to MAIL before AUTH
	GreetDelay  time.Duration     // delay before the 220 greeting
	SizeParam   string            // SIZE argument advertised verbatim (may be malformed); "" omits SIZE
	DiscardData bool              // consume DATA without retaining it (memory tests)

	mu        sync.Mutex
	rcptReply map[string]string // address -> reply line, e.g. "550 5.1.1 no such user"
	dataReply string            // final DATA reply; default "250 2.0.0 queued"
	dataDelay time.Duration
	messages  []SMTPMessage
	commands  []SMTPCommand
	ln        net.Listener
}

// NewMockSMTP starts a server on 127.0.0.1 after configure (may be nil)
// has set it up, and stops it when the test ends.
func NewMockSMTP(t *testing.T, configure func(*MockSMTP)) *MockSMTP {
	t.Helper()
	m := &MockSMTP{rcptReply: map[string]string{}}
	if configure != nil {
		configure(m)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock SMTP: %v", err)
	}
	m.ln = ln
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("mock SMTP: closing listener: %v", err)
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(c)
		}
	}()
	return m
}

// Host and Port address the server.
func (m *MockSMTP) Host() string { return "127.0.0.1" }
func (m *MockSMTP) Port() int {
	if a, ok := m.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// SetRcptReply scripts the reply to RCPT TO for addr ("" restores 250).
func (m *MockSMTP) SetRcptReply(addr, reply string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if reply == "" {
		delete(m.rcptReply, addr)
		return
	}
	m.rcptReply[addr] = reply
}

// SetDataReply scripts the final reply after DATA and an optional delay
// before it (exercising client timeouts).
func (m *MockSMTP) SetDataReply(reply string, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dataReply, m.dataDelay = reply, delay
}

// SetUsers replaces the accepted credentials.
func (m *MockSMTP) SetUsers(users map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Users = users
}

func (m *MockSMTP) password(user string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.Users[user]
	return p, ok
}

// Messages returns the accepted messages.
func (m *MockSMTP) Messages() []SMTPMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SMTPMessage(nil), m.messages...)
}

// Commands returns the received commands.
func (m *MockSMTP) Commands() []SMTPCommand {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SMTPCommand(nil), m.commands...)
}

// Saw reports whether the server received a command with this verb.
func (m *MockSMTP) Saw(verb string) bool {
	for _, c := range m.Commands() {
		if c.Verb == verb {
			return true
		}
	}
	return false
}

type smtpSession struct {
	m        *MockSMTP
	conn     net.Conn
	r        *bufio.Reader
	tls      bool
	from     string
	rcpts    []string
	authUser string
}

func (s *smtpSession) reply(lines ...string) bool {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	_, err := s.conn.Write([]byte(b.String()))
	return err == nil
}

func (s *smtpSession) readLine() (string, error) {
	l, err := s.r.ReadString('\n')
	return strings.TrimRight(l, "\r\n"), err
}

func (m *MockSMTP) serve(conn net.Conn) {
	s := &smtpSession{m: m, conn: conn}
	defer func() {
		if err := s.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("mock SMTP: closing connection: %v", err)
		}
	}()
	if m.Implicit {
		tc := tls.Server(conn, m.TLSConfig)
		if tc.Handshake() != nil {
			return
		}
		s.conn, s.tls = tc, true
	}
	s.r = bufio.NewReader(s.conn)
	time.Sleep(m.GreetDelay)
	if !s.reply("220 mock.example ESMTP") {
		return
	}
	for {
		line, err := s.readLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		m.mu.Lock()
		m.commands = append(m.commands, SMTPCommand{Verb: verb, TLS: s.tls})
		m.mu.Unlock()
		if !s.handle(verb, arg) {
			return
		}
	}
}

// handle answers one command; false ends the session.
func (s *smtpSession) handle(verb, arg string) bool {
	m := s.m
	switch verb {
	case "EHLO", "HELO":
		ext := []string{"mock.example", "8BITMIME"}
		if m.SizeParam != "" {
			ext = append(ext, "SIZE "+m.SizeParam)
		}
		if m.StartTLS && !s.tls {
			ext = append(ext, "STARTTLS")
		}
		if len(m.AuthMechs) > 0 && s.tls {
			ext = append(ext, "AUTH "+strings.Join(m.AuthMechs, " "))
		}
		lines := make([]string, len(ext))
		for i, e := range ext {
			sep := "-"
			if i == len(ext)-1 {
				sep = " "
			}
			lines[i] = "250" + sep + e
		}
		return s.reply(lines...)
	case "STARTTLS":
		if !m.StartTLS || s.tls {
			return s.reply("502 5.5.1 STARTTLS not available")
		}
		if !s.reply("220 2.0.0 ready to start TLS") {
			return false
		}
		tc := tls.Server(s.conn, m.TLSConfig)
		if tc.Handshake() != nil {
			return false
		}
		s.conn, s.tls, s.r = tc, true, bufio.NewReader(tc)
		s.from, s.rcpts, s.authUser = "", nil, ""
		return true
	case "AUTH":
		return s.auth(arg)
	case "MAIL":
		if m.RequireAuth && s.authUser == "" {
			return s.reply("530 5.7.0 Authentication required")
		}
		s.from, s.rcpts = angle(arg), nil
		return s.reply("250 2.1.0 ok")
	case "RCPT":
		addr := angle(arg)
		m.mu.Lock()
		r, scripted := m.rcptReply[addr]
		m.mu.Unlock()
		if scripted {
			return s.reply(r)
		}
		s.rcpts = append(s.rcpts, addr)
		return s.reply("250 2.1.5 ok")
	case "DATA":
		if len(s.rcpts) == 0 {
			return s.reply("503 5.5.1 no valid recipients")
		}
		if !s.reply("354 end with <CRLF>.<CRLF>") {
			return false
		}
		return s.data()
	case "RSET":
		s.from, s.rcpts = "", nil
		return s.reply("250 2.0.0 ok")
	case "NOOP":
		return s.reply("250 2.0.0 ok")
	case "QUIT":
		s.reply("221 2.0.0 bye")
		return false
	default:
		return s.reply("502 5.5.2 command not recognized")
	}
}

// data reads the message: dot-stuffing removed, line endings kept.
func (s *smtpSession) data() bool {
	msg := SMTPMessage{From: s.from, To: s.rcpts, TLS: s.tls, AuthUser: s.authUser}
	if s.m.DiscardData {
		if !s.discardData(&msg) {
			return false
		}
	} else {
		var buf strings.Builder
		for {
			l, err := s.r.ReadString('\n')
			if err != nil {
				return false
			}
			if l == ".\r\n" {
				break
			}
			if !strings.HasSuffix(l, "\r\n") {
				msg.BareLF = true
			}
			buf.WriteString(strings.TrimPrefix(l, "."))
		}
		msg.Data = []byte(buf.String())
	}
	s.m.mu.Lock()
	reply, delay := s.m.dataReply, s.m.dataDelay
	if reply == "" {
		reply = "250 2.0.0 queued"
	}
	if strings.HasPrefix(reply, "2") {
		s.m.messages = append(s.m.messages, msg)
	}
	s.m.mu.Unlock()
	time.Sleep(delay)
	s.from, s.rcpts = "", nil
	return s.reply(reply)
}

// discardData consumes DATA without retaining it, for memory tests:
// ReadSlice reuses the reader's buffer, and a line longer than the buffer
// arrives in several chunks (bufio.ErrBufferFull) instead of failing.
func (s *smtpSession) discardData(msg *SMTPMessage) bool {
	lineStart, prevCR := true, false
	for {
		l, err := s.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			lineStart, prevCR = false, l[len(l)-1] == '\r'
			continue
		}
		if err != nil {
			return false
		}
		if lineStart && bytes.Equal(l, []byte(".\r\n")) {
			return true
		}
		// The CR of a CRLF may end the previous chunk of a long line.
		if crlf := len(l) >= 2 && l[len(l)-2] == '\r' || len(l) == 1 && prevCR; !crlf {
			msg.BareLF = true
		}
		lineStart, prevCR = true, false
	}
}

func (s *smtpSession) auth(arg string) bool {
	mech, initial, _ := strings.Cut(arg, " ")
	user, pass, ok := s.credentials(strings.ToUpper(mech), initial)
	if !ok {
		return s.reply("501 5.5.2 malformed authentication exchange")
	}
	if want, known := s.m.password(user); known && want == pass && user != "" {
		s.authUser = user
		return s.reply("235 2.7.0 authentication successful")
	}
	return s.reply("535 5.7.8 authentication credentials invalid")
}

// credentials runs the SASL exchange of mech and returns the claimed user
// and password (for CRAM-MD5, the user's password when the digest matches).
// ok is false for an unknown mechanism or a malformed exchange.
func (s *smtpSession) credentials(mech, initial string) (user, pass string, ok bool) {
	b64 := base64.StdEncoding
	// challenge sends a 334 prompt and decodes the client's answer.
	challenge := func(prompt string) (string, bool) {
		if !s.reply("334 " + b64.EncodeToString([]byte(prompt))) {
			return "", false
		}
		line, err := s.readLine()
		if err != nil {
			return "", false
		}
		answer, err := b64.DecodeString(line)
		return string(answer), err == nil
	}
	switch mech {
	case "PLAIN":
		raw := ""
		if initial == "" {
			answer, good := challenge("")
			if !good {
				return "", "", false
			}
			raw = answer
		} else {
			decoded, err := b64.DecodeString(initial)
			if err != nil {
				return "", "", false
			}
			raw = string(decoded)
		}
		parts := strings.Split(raw, "\x00")
		if len(parts) != 3 {
			return "", "", false
		}
		return parts[1], parts[2], true
	case "LOGIN":
		u, good := challenge("Username:")
		if !good {
			return "", "", false
		}
		p, good := challenge("Password:")
		return u, p, good
	case "CRAM-MD5":
		const nonce = "<1896.697170952@mock.example>"
		answer, good := challenge(nonce)
		u, digest, found := strings.Cut(answer, " ")
		if !good || !found {
			return "", "", false
		}
		if want, known := s.m.password(u); known {
			h := hmac.New(md5.New, []byte(want))
			if _, err := h.Write([]byte(nonce)); err != nil {
				return "", "", false
			}
			if hex.EncodeToString(h.Sum(nil)) == digest {
				return u, want, true
			}
		}
		return u, "", true
	}
	return "", "", false
}

// angle extracts the address from "FROM:<a@b> SIZE=1" or "TO:<a@b>".
func angle(arg string) string {
	i, j := strings.Index(arg, "<"), strings.Index(arg, ">")
	if i < 0 || j < i {
		return ""
	}
	return arg[i+1 : j]
}

// TLSPair returns a server configuration with a fresh self-signed
// certificate for 127.0.0.1 and localhost, and a client configuration
// that trusts exactly that certificate.
func TLSPair(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mailfire test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	server = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	client = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return server, client
}

// SMTPAddress formats host:port for messages.
func (m *MockSMTP) String() string { return fmt.Sprintf("MockSMTP{%s:%d}", m.Host(), m.Port()) }
