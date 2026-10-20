package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/gmail"
)

// fakeGoogle serves the OAuth token endpoint and the Gmail send endpoint.
type fakeGoogle struct {
	srv *httptest.Server

	mu         sync.Mutex
	tokenCalls int
	sends      [][]byte // decoded raw messages
	auth       []string // Authorization headers of sends
	sendStatus int      // 0: 200
	tokenReply string   // "" : a fresh token with an ID token for sub-42
	tokenCode  int
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{}
	reply := func(w io.Writer, body string) {
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("fake Google: writing the reply: %v", err)
		}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/token":
			f.tokenCalls++
			if f.tokenCode != 0 {
				w.WriteHeader(f.tokenCode)
			}
			tokenReply := f.tokenReply
			if tokenReply == "" {
				enc := base64.RawURLEncoding
				id := enc.EncodeToString([]byte(`{}`)) + "." + enc.EncodeToString([]byte(`{"sub":"sub-42"}`)) + ".x"
				tokenReply = fmt.Sprintf(`{"access_token":"fresh-access","refresh_token":"refresh-1","expires_in":3600,"id_token":%q}`, id)
			}
			reply(w, tokenReply)
		case "/upload/gmail/v1/users/me/messages/send":
			if r.URL.Query().Get("uploadType") != "media" {
				t.Errorf("fake Google: uploadType = %q", r.URL.Query().Get("uploadType"))
			}
			if r.Header.Get("Content-Type") != "message/rfc822" {
				t.Errorf("fake Google: Content-Type = %q", r.Header.Get("Content-Type"))
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("fake Google: send request: %v", err)
			}
			f.sends = append(f.sends, raw)
			f.auth = append(f.auth, r.Header.Get("Authorization"))
			if f.sendStatus != 0 {
				w.WriteHeader(f.sendStatus)
				reply(w, `{"error":{"message":"backend error"}}`)
				return
			}
			reply(w, `{"id":"gmail-msg-1","threadId":"t1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Setenv("MAILFIRE_GMAIL_CLIENT_ID", "test-client.apps.googleusercontent.com")
	return f
}

// set changes the fake's replies under its lock.
func (f *fakeGoogle) set(change func(f *fakeGoogle)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *fakeGoogle) option() Option {
	return WithGmailEndpoints(f.srv.URL+"/gmail/v1", f.srv.URL+"/token")
}

func (f *fakeGoogle) recorded() (raws [][]byte, auths []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.sends...), append([]string(nil), f.auth...)
}

func (f *fakeGoogle) counts() (tokens, sends int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls, len(f.sends)
}

type gmailEnv struct {
	g        *fakeGoogle
	cfg      string
	creds    string
	deferDir string
}

func newGmailEnv(t *testing.T) *gmailEnv {
	t.Helper()
	dir := t.TempDir()
	e := &gmailEnv{g: newFakeGoogle(t), creds: filepath.Join(dir, "gmail.json"), deferDir: filepath.Join(dir, "deferred")}
	e.cfg = filepath.Join(dir, "mailfire.conf")
	content := fmt.Sprintf("[sending]\nprovider = \"gmail\"\nfrom = \"Me <me@gmail.com>\"\n\n[gmail]\ncredentials = '%s'\ntimeout = \"5s\"\n\n[defer]\ndefer_dir = '%s'\n", e.creds, e.deferDir)
	if err := os.WriteFile(e.cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *gmailEnv) login(t *testing.T, expiry time.Time) {
	t.Helper()
	if err := (&gmail.Store{Path: e.creds}).Write(gmail.Credentials{AccessToken: "cached-access", RefreshToken: "refresh-0",
		Expiry: expiry, Account: "sub-42"}); err != nil {
		t.Fatal(err)
	}
}

func (e *gmailEnv) run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr, testInfo, WithStdin(strings.NewReader(stdin)), e.g.option(),
		WithBrowser(func(string) error { t.Error("a browser was opened"); return nil }))
	return code, stdout.String(), stderr.String()
}

func TestGmailSendUsesCachedToken(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(time.Hour))
	code, stdout, stderr := e.run(t, "hello", "-c", e.cfg, "--to", "to@example.com", "--bcc", "hidden@example.com", "--subject", "s")
	if code != ExitOK || strings.TrimSpace(stdout) != "gmail-msg-1" {
		t.Fatalf("exit %d, stdout %q\n%s", code, stdout, stderr)
	}
	tokens, sends := e.g.counts()
	raws, auths := e.g.recorded()
	if tokens != 0 || sends != 1 || auths[0] != "Bearer cached-access" {
		t.Fatalf("token calls %d, sends %d, auth %v", tokens, sends, auths)
	}
	raw := raws[0]
	// Gmail takes Bcc from the header (and strips it before delivery).
	for _, want := range []string{"Bcc: <hidden@example.com>\r\n", "Message-ID: <", "Date: "} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("raw message lacks %q", want)
		}
	}
	if strings.Contains(stdout+stderr, "cached-access") || strings.Contains(stdout+stderr, "refresh-0") {
		t.Error("token printed")
	}
}

func TestGmailSendRefreshesExpiredToken(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(-time.Minute))
	if code, _, stderr := e.run(t, "hello", "-c", e.cfg, "--to", "to@example.com", "--subject", "s"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if tokens, _ := e.g.counts(); tokens != 1 {
		t.Fatalf("token calls %d", tokens)
	}
	if _, auths := e.g.recorded(); auths[0] != "Bearer fresh-access" {
		t.Fatalf("auth %v", auths)
	}
	if c, err := (&gmail.Store{Path: e.creds}).Read(); err != nil || c.RefreshToken != "refresh-1" || c.Account != "sub-42" {
		t.Errorf("stored credentials after refresh = %+v, %v", c, err)
	}
}

func TestGmailSendNotLoggedIn(t *testing.T) {
	e := newGmailEnv(t)
	code, _, stderr := e.run(t, "hello", "-c", e.cfg, "--to", "to@example.com", "--subject", "s")
	if code != ExitAuth || !strings.Contains(stderr, "mailfire auth login --provider gmail") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if _, sends := e.g.counts(); sends != 0 {
		t.Error("sent without credentials")
	}
}

func TestGmailRevokedGrantIsAuthError(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(-time.Minute))
	e.g.set(func(f *fakeGoogle) {
		f.tokenCode, f.tokenReply = 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
	})
	code, _, stderr := e.run(t, "hello", "-c", e.cfg, "--to", "to@example.com", "--subject", "s")
	if code != ExitAuth || !strings.Contains(stderr, "auth login") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if msgs := queuedMessages(t, e.deferDir); len(msgs) != 0 {
		t.Error("revoked authorization was deferred")
	}
}

func TestGmailTestModeRefused(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(time.Hour))
	if code, _, stderr := e.run(t, "hello", "-c", e.cfg, "--to", "to@example.com", "--subject", "s", "--test"); code != ExitUsage || !strings.Contains(stderr, "gmail") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if tokens, sends := e.g.counts(); tokens+sends != 0 {
		t.Error("--test contacted Google")
	}
}

func TestGmailDeferAndResend(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(time.Hour))
	e.g.set(func(f *fakeGoogle) { f.sendStatus = http.StatusServiceUnavailable })
	if code, _, stderr := e.run(t, "hello", "-c", e.cfg, "--to", "to@example.com", "--subject", "s"); code != ExitDeferred {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	msgs := queuedMessages(t, e.deferDir)
	if len(msgs) != 1 || msgs[0].Provider != "gmail" || msgs[0].Account != "google:sub-42" {
		t.Fatalf("queue = %+v", msgs)
	}
	raws, _ := e.g.recorded()
	first := raws[0]

	// Logged into another Google account: blocked, kept, untouched.
	if err := (&gmail.Store{Path: e.creds}).Write(gmail.Credentials{AccessToken: "x", RefreshToken: "y", Expiry: time.Now().Add(time.Hour), Account: "sub-other"}); err != nil {
		t.Fatal(err)
	}
	e.g.set(func(f *fakeGoogle) { f.sendStatus = 0 })
	code, stdout, _ := e.run(t, "", "resend", "--force", "-c", e.cfg)
	if _, sends := e.g.counts(); code != ExitSendFail || sends != 1 || !strings.Contains(stdout, "account mismatch") {
		t.Fatalf("other account: exit %d, sends %d\n%s", code, sends, stdout)
	}

	e.login(t, time.Now().Add(time.Hour))
	if code, stdout, stderr := e.run(t, "", "resend", "-c", e.cfg, "--force"); code != ExitOK {
		t.Fatalf("resend exit %d\n%s%s", code, stdout, stderr)
	}
	if raws, _ := e.g.recorded(); len(raws) != 2 || !bytes.Equal(raws[1], first) {
		t.Fatal("the resend did not replay the identical message")
	}
}

func TestGmailValidateSendsNothing(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(time.Hour))
	code, stdout, stderr := e.run(t, "", "validate", "-c", e.cfg)
	if code != ExitOK || !strings.Contains(stdout, "Gmail send authorization is available") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if _, sends := e.g.counts(); sends != 0 {
		t.Error("validate sent a message")
	}
}

func TestAuthRefreshForcesExchangeAndNeverSends(t *testing.T) {
	e := newGmailEnv(t)
	e.login(t, time.Now().Add(time.Hour)) // still valid: refresh must happen anyway
	code, stdout, stderr := e.run(t, "", "auth", "refresh", "--provider", "gmail", "-c", e.cfg, "--json")
	if code != ExitOK || !strings.Contains(stdout, `"outcome":"ok"`) {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if tokens, sends := e.g.counts(); tokens != 1 || sends != 0 {
		t.Fatalf("token calls %d, sends %d", tokens, sends)
	}
	if strings.Contains(stdout+stderr, "fresh-access") || strings.Contains(stdout+stderr, "refresh-1") {
		t.Error("token printed")
	}
}

func TestAuthRefreshExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status int
		reply  string
		want   int
	}{
		{400, `{"error":"invalid_grant"}`, ExitAuth},
		{503, `{"error":"unavailable"}`, ExitDeferred},
	} {
		e := newGmailEnv(t)
		e.login(t, time.Now().Add(time.Hour))
		e.g.set(func(f *fakeGoogle) { f.tokenCode, f.tokenReply = tc.status, tc.reply })
		if code, _, stderr := e.run(t, "", "auth", "refresh", "--provider", "gmail", "-c", e.cfg); code != tc.want {
			t.Errorf("token %d: exit %d, want %d: %s", tc.status, code, tc.want, stderr)
		}
	}
	e := newGmailEnv(t)
	if code, _, _ := e.run(t, "", "auth", "refresh", "--provider", "gmail", "-c", e.cfg); code != ExitAuth {
		t.Errorf("not logged in: exit %d, want %d", code, ExitAuth)
	}
}

func TestAuthStatusAndLogout(t *testing.T) {
	e := newGmailEnv(t)
	code, stdout, _ := e.run(t, "", "auth", "status", "--provider", "gmail", "-c", e.cfg, "--json")
	if code != ExitAuth || !strings.Contains(stdout, `"logged_in":false`) {
		t.Errorf("logged out: exit %d, %s", code, stdout)
	}
	e.login(t, time.Now().Add(time.Hour))
	if code, stdout, _ = e.run(t, "", "auth", "status", "--provider", "gmail", "-c", e.cfg, "--json"); code != ExitOK || !strings.Contains(stdout, `"logged_in":true`) {
		t.Errorf("logged in: exit %d, %s", code, stdout)
	}
	if code, stdout, _ = e.run(t, "", "auth", "logout", "--provider", "gmail", "-c", e.cfg); code != ExitOK || !strings.Contains(stdout, "was NOT revoked") {
		t.Errorf("logout: exit %d, %s", code, stdout)
	}
	if _, err := os.Stat(e.creds); !os.IsNotExist(err) {
		t.Error("logout left the credentials")
	}
}

func TestAuthUsageAndConfigErrors(t *testing.T) {
	e := newGmailEnv(t)
	for _, args := range [][]string{
		{"auth", "login"},                                          // no provider
		{"auth", "dance", "--provider", "gmail"},                   // unknown subcommand
		{"auth", "status", "--provider", "gmail", "--manual"},      // login-only flag
		{"auth", "refresh", "--provider", "gmail", "--timeout="},   // empty timeout
		{"auth", "login", "--provider", "gmail", "--port", "http"}, // bad port
		{"auth", "login", "--provider", "gmail", "--manual"},       // manual without port
	} {
		if code, _, stderr := e.run(t, "", args...); code != ExitUsage || stderr == "" {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
	broken := filepath.Join(t.TempDir(), "broken.conf")
	if err := os.WriteFile(broken, []byte("[gmail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run(t, "", "auth", "refresh", "--provider", "gmail", "-c", broken); code != ExitConfig {
		t.Errorf("broken config: exit %d, want %d (never silently fall back to defaults)", code, ExitConfig)
	}
	if code, stdout, _ := e.run(t, "", "auth", "--help"); code != ExitOK || !strings.Contains(stdout, "auth refresh") {
		t.Errorf("help: %d %s", code, stdout)
	}
}

// syncBuffer is a goroutine-safe writer the test can poll.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startLogin runs auth login in the background and returns the consent
// URL it printed and a channel with the exit code.
func startLogin(t *testing.T, e *gmailEnv, extra ...string) (*url.URL, <-chan int, *syncBuffer) {
	t.Helper()
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	args := append([]string{"auth", "login", "--provider", "gmail", "-c", e.cfg}, extra...)
	go func() { done <- Run(args, &stdout, &stderr, testInfo, e.g.option(), WithStdin(strings.NewReader(""))) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(stdout.String(), "\n") {
			if strings.HasPrefix(line, "https://accounts.google.com/") {
				u, err := url.Parse(line)
				if err != nil {
					t.Fatal(err)
				}
				return u, done, &stderr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no consent URL printed; stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	return nil, nil, nil
}

func httpStatus(t *testing.T, u string) int {
	t.Helper()
	resp, err := http.Get(u) //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
	return resp.StatusCode
}

func TestAuthLoginLoopback(t *testing.T) {
	e := newGmailEnv(t)
	consent, done, stderr := startLogin(t, e, "--no-browser")
	q := consent.Query()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	if q.Get("scope") != "openid "+gmail.ScopeSend || q.Get("code_challenge") == "" || !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Fatalf("consent URL = %s", consent)
	}
	base, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	base.Path = "/favicon.ico"
	if s := httpStatus(t, base.String()); s != http.StatusNotFound {
		t.Errorf("unrelated path: status %d, want 404", s)
	}
	if s := httpStatus(t, redirect+"?state=forged&code=x"); s != http.StatusBadRequest {
		t.Errorf("forged state: status %d, want 400", s)
	}
	if s := httpStatus(t, redirect+"?state="+url.QueryEscape(state)+"&code=auth-code"); s != http.StatusOK {
		t.Errorf("callback: status %d", s)
	}
	select {
	case code := <-done:
		if code != ExitOK {
			t.Fatalf("login exit %d: %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login did not finish")
	}
	c, err := (&gmail.Store{Path: e.creds}).Read()
	if err != nil || c.RefreshToken != "refresh-1" || c.Account != "sub-42" {
		t.Fatalf("saved credentials = %+v, %v", c, err)
	}
}

func TestAuthLoginDenied(t *testing.T) {
	e := newGmailEnv(t)
	consent, done, _ := startLogin(t, e, "--no-browser")
	q := consent.Query()
	httpStatus(t, q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&error=access_denied")
	if code := <-done; code != ExitAuth {
		t.Fatalf("denied consent: exit %d, want %d", code, ExitAuth)
	}
	if tokens, _ := e.g.counts(); tokens != 0 {
		t.Error("token endpoint contacted after denial")
	}
}

// A consent that grants sign-in but not sending (Google shows each scope
// as its own checkbox) fails the login and saves nothing.
func TestAuthLoginWithoutSendPermission(t *testing.T) {
	e := newGmailEnv(t)
	e.g.set(func(f *fakeGoogle) {
		f.tokenReply = `{"access_token":"a","refresh_token":"r","expires_in":3600,"scope":"openid"}`
	})
	consent, done, stderr := startLogin(t, e, "--no-browser")
	q := consent.Query()
	httpStatus(t, q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&code=auth-code")
	if code := <-done; code != ExitAuth || !strings.Contains(stderr.String(), "Send email on your behalf") {
		t.Fatalf("exit %d, stderr %q; want %d with the missing permission explained", code, stderr.String(), ExitAuth)
	}
	if _, err := os.Stat(e.creds); !os.IsNotExist(err) {
		t.Errorf("credentials saved without the send permission (stat: %v)", err)
	}
}

func TestAuthLoginManual(t *testing.T) {
	e := newGmailEnv(t)
	r, w := io.Pipe()
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"auth", "login", "--provider", "gmail", "-c", e.cfg, "--manual", "--port", "8765"},
			&stdout, &stderr, testInfo, e.g.option(), WithStdin(r))
	}()
	var consent *url.URL
	for i := 0; i < 500 && consent == nil; i++ {
		for _, line := range strings.Split(stdout.String(), "\n") {
			if strings.HasPrefix(line, "https://accounts.google.com/") {
				u, err := url.Parse(line)
				if err != nil {
					t.Fatal(err)
				}
				consent = u
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if consent == nil {
		t.Fatalf("no consent URL: %s", stdout.String())
	}
	q := consent.Query()
	if _, err := fmt.Fprintf(w, "%s?state=%s&code=pasted\n", q.Get("redirect_uri"), url.QueryEscape(q.Get("state"))); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != ExitOK {
		t.Fatalf("manual login exit %d: %s", code, stderr.String())
	}
	if c, err := (&gmail.Store{Path: e.creds}).Read(); err != nil || c.Account != "sub-42" {
		t.Fatalf("credentials = %+v, %v", c, err)
	}
}
