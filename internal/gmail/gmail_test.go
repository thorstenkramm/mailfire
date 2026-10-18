package gmail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/message"
)

func TestEncodeMessageUsesSharedMIMEAndIncludesBccForGmail(t *testing.T) {
	raw, err := EncodeMessage(&message.Message{From: "Sender <sender@example.com>", To: []string{"to@example.com"}, Bcc: []string{"hidden@example.com"}, Subject: "café", Text: "plain", HTML: "<p>html</p>"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"Date: ", "Message-ID: ", "Bcc: <hidden@example.com>", "multipart/alternative", "Subject: =?utf-8?"} {
		if !strings.Contains(s, want) {
			t.Errorf("MIME lacks %q:\n%s", want, s)
		}
	}
}

func TestSendRawRedactsBearerToken(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusBadGateway)
		respond(t, w, `{"error":{"message":"echo secret-token"}}`)
	}))
	defer s.Close()
	c := mustClient(t, "secret-token", WithBaseURL(s.URL))
	_, err := c.SendRaw(context.Background(), "message/rfc822", []byte("From: a@example.com\r\n\r\nx"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("token leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("not redacted: %v", err)
	}
}

func TestRefreshPreservesOmittedRefreshToken(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(t, w, `{"access_token":"new-access","expires_in":3600}`)
	}))
	defer s.Close()
	o := OAuthClient{ClientID: "test", HTTP: s.Client(), TokenURL: s.URL}
	dir := t.TempDir()
	store := &Store{Path: dir + "/token.json"}
	if err := store.Write(Credentials{AccessToken: "old", RefreshToken: "keep", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c, err := store.AccessToken(context.Background(), o, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshToken != "keep" || c.AccessToken != "new-access" {
		t.Fatalf("credentials = %+v", c)
	}
}

// tokenServer is a fake OAuth token endpoint counting requests.
type tokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	reply func(w http.ResponseWriter, r *http.Request)
}

func newTokenServer(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *tokenServer {
	ts := &tokenServer{reply: reply}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		ts.calls++
		ts.mu.Unlock()
		ts.reply(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.calls
}

func (ts *tokenServer) client() OAuthClient {
	return OAuthClient{ClientID: "test", HTTP: ts.Client(), TokenURL: ts.URL}
}

func idToken(sub string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString([]byte(`{"sub":"`+sub+`"}`)) + ".sig"
}

func TestConcurrentRefreshRefreshesOnce(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		respond(t, w, `{"access_token":"fresh","expires_in":3600}`)
	})
	store := &Store{Path: filepath.Join(t.TempDir(), "tok.json")}
	if err := store.Write(Credentials{AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := (&Store{Path: store.Path}).AccessToken(context.Background(), ts.client(), false)
			if err == nil && c.AccessToken != "fresh" {
				err = fmt.Errorf("token = %q", c.AccessToken)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := ts.count(); n != 1 {
		t.Fatalf("token endpoint called %d times, want 1", n)
	}
}

func TestRefreshPersistsReplacementTokenAndAccount(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		respond(t, w, `{"access_token":"a2","refresh_token":"r2","expires_in":60}`)
	})
	store := &Store{Path: filepath.Join(t.TempDir(), "tok.json")}
	mustWrite(t, store, Credentials{AccessToken: "a1", RefreshToken: "r1", Account: "sub-1", Expiry: time.Now().Add(time.Hour)})
	if _, err := store.AccessToken(context.Background(), ts.client(), true); err != nil {
		t.Fatal(err)
	}
	c, err := store.Read()
	if err != nil || c.RefreshToken != "r2" || c.AccessToken != "a2" || c.Account != "sub-1" {
		t.Fatalf("stored = %+v, %v", c, err)
	}
	if fi, err := os.Stat(store.Path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("credential file mode = %v, %v; want 0600", fi.Mode(), err)
	}
}

func TestValidTokenIsNotRefreshedUnlessForced(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		respond(t, w, `{"access_token":"new","expires_in":3600}`)
	})
	store := &Store{Path: filepath.Join(t.TempDir(), "tok.json")}
	mustWrite(t, store, Credentials{AccessToken: "cached", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	if c, err := store.AccessToken(context.Background(), ts.client(), false); err != nil || c.AccessToken != "cached" || ts.count() != 0 {
		t.Fatalf("unforced: token %q, %d calls, %v", c.AccessToken, ts.count(), err)
	}
	if c, err := store.AccessToken(context.Background(), ts.client(), true); err != nil || c.AccessToken != "new" || ts.count() != 1 {
		t.Fatalf("forced: token %q, %d calls, %v", c.AccessToken, ts.count(), err)
	}
}

func TestTokenEndpointErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		kind   message.Kind
	}{
		{400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, message.KindAuth},
		{401, `{"error":"invalid_client"}`, message.KindAuth},
		{400, `{"error":"invalid_request"}`, message.KindPermanent},
		{503, `{"error":"backend"}`, message.KindDeferrable},
		{429, ``, message.KindDeferrable},
	} {
		ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			respond(t, w, tc.body)
		})
		_, err := ts.client().Refresh(context.Background(), "r")
		serr, ok := message.As(err)
		if !ok || serr.SendKind() != tc.kind {
			t.Errorf("%d %s: %v, want %v", tc.status, tc.body, err, tc.kind)
		}
		if tc.kind == message.KindAuth && !strings.Contains(err.Error(), "mailfire auth login --provider gmail") {
			t.Errorf("revoked grant without a login command: %v", err)
		}
		if strings.Contains(err.Error(), "mailgun") {
			t.Errorf("Gmail error reported as Mailgun: %v", err)
		}
	}
}

func TestTokenTimeoutAndCancel(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	})
	o := ts.client()
	o.HTTP = &http.Client{Timeout: 100 * time.Millisecond}
	if _, err := o.Refresh(context.Background(), "r"); !message.IsDeferrable(err) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: %v, want deferrable", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ts.client().Refresh(ctx, "r"); !message.IsPermanent(err) {
		t.Errorf("canceled: %v, want permanent", err)
	}
}

func TestExchangeRecordsAccountID(t *testing.T) {
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the token request: %v", err)
		}
		if r.Form.Get("code_verifier") != "v" || r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("exchange form = %v", r.Form)
		}
		respond(t, w, fmt.Sprintf(`{"access_token":"a","refresh_token":"r","expires_in":3600,"id_token":%q}`, idToken("1234567890")))
	})
	c, err := ts.client().Exchange(context.Background(), "code", "http://127.0.0.1:1/oauth2/callback", "v")
	if err != nil || c.Account != "1234567890" || c.RefreshToken != "r" {
		t.Fatalf("credentials = %+v, %v", c, err)
	}
	if subject("not-a-jwt") != "" || subject("a.!!!.c") != "" {
		t.Error("malformed ID tokens must yield no account")
	}
}

func TestUnconfiguredClient(t *testing.T) {
	// The built-in client without its build-time secret, or no client at
	// all, cannot sign in; Google would reject the request.
	for _, o := range []OAuthClient{{ClientID: BuiltinClientID}, {}} {
		if _, err := o.Refresh(context.Background(), "r"); !errors.Is(err, ErrClientNotConfigured) {
			t.Errorf("%q: error = %v", o.ClientID, err)
		}
	}
	for _, o := range []OAuthClient{{ClientID: BuiltinClientID, ClientSecret: "s"}, {ClientID: "dev-client"}} {
		if err := o.Configured(); err != nil {
			t.Errorf("%q: Configured() = %v", o.ClientID, err)
		}
	}
}

// The build-time secret belongs to the built-in client only: it is never
// sent with a developer's own client ID, and the variable overrides it.
func TestDefaultOAuthClientSecret(t *testing.T) {
	saved := builtinClientSecret
	t.Cleanup(func() { builtinClientSecret = saved })
	builtinClientSecret = "built-in"
	for _, tc := range []struct{ id, secret, wantID, wantSecret string }{
		{"", "", BuiltinClientID, "built-in"},
		{"", " env-secret ", BuiltinClientID, "env-secret"},
		{BuiltinClientID, "", BuiltinClientID, "built-in"},
		{"dev-client", "", "dev-client", ""},
		{"dev-client", "dev-secret", "dev-client", "dev-secret"},
	} {
		t.Setenv("MAILFIRE_GMAIL_CLIENT_ID", tc.id)
		t.Setenv("MAILFIRE_GMAIL_CLIENT_SECRET", tc.secret)
		if o := DefaultOAuthClient(0); o.ClientID != tc.wantID || o.ClientSecret != tc.wantSecret {
			t.Errorf("id %q, secret %q: got %q, %q; want %q, %q", tc.id, tc.secret, o.ClientID, o.ClientSecret, tc.wantID, tc.wantSecret)
		}
	}
}

func TestReadNotLoggedInAndInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	store := &Store{Path: filepath.Join(dir, "tok.json")}
	if _, err := store.Read(); !message.IsAuth(err) || !strings.Contains(err.Error(), "auth login") {
		t.Fatalf("missing file: %v, want an auth error naming the login command", err)
	}
	if err := os.WriteFile(store.Path, []byte(`{"access_token":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); !message.IsAuth(err) {
		t.Errorf("no refresh token: %v, want auth error", err)
	}
	// A crash mid-write leaves a temporary file; it must neither be read
	// nor block the next write.
	if err := os.WriteFile(filepath.Join(dir, "tok.json.123.tmp"), []byte("{trunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(Credentials{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if c, err := store.Read(); err != nil || c.RefreshToken != "r" {
		t.Fatalf("after write: %+v, %v", c, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Error("Delete left the credential file")
	}
	if _, err := (&Store{}).Read(); err == nil || message.IsAuth(err) {
		t.Errorf("empty path: %v, want a configuration error", err)
	}
}

func TestDefaultCredentialPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := defaultCredentialPath("windows", env(map[string]string{"APPDATA": `C:\Users\me\AppData\Roaming`, "HOME": "/ignored"})); !strings.HasSuffix(got, "gmail-credentials.json") || !strings.HasPrefix(got, `C:\Users\me\AppData\Roaming`) {
		t.Errorf("windows = %q", got)
	}
	if got := defaultCredentialPath("windows", env(nil)); got != "" {
		t.Errorf("windows without APPDATA = %q, want \"\" (never relative to the working directory)", got)
	}
	if got := defaultCredentialPath("linux", env(map[string]string{"HOME": "/home/me"})); got != filepath.Join("/home/me", ".config", "mailfire", "gmail-credentials.json") {
		t.Errorf("linux = %q", got)
	}
	if got := defaultCredentialPath("darwin", env(nil)); got != "" {
		t.Errorf("unix without HOME = %q", got)
	}
}

func TestAuthorizationURLScopes(t *testing.T) {
	u, err := url.Parse(AuthorizationURL(OAuthClient{ClientID: "c"}, "http://127.0.0.1:9/cb", "ch", "st"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("scope") != "openid "+ScopeSend || q.Get("access_type") != "offline" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" {
		t.Errorf("authorization query = %v", q)
	}
}

// Google's consent screen grants each scope separately: a token without
// gmail.send is rejected at sign-in and at refresh, not saved for a send
// that could never work.
func TestTokenWithoutSendScopeIsRejected(t *testing.T) {
	for _, tc := range []struct {
		scope string
		ok    bool
	}{
		{"openid", false},
		{"openid " + ScopeSend, true},
		{ScopeSend, true},
		{"", true}, // no list: says nothing about the grant
	} {
		ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
			respond(t, w, fmt.Sprintf(`{"access_token":"a","refresh_token":"r","expires_in":3600,"scope":%q}`, tc.scope))
		})
		_, err := ts.client().Exchange(context.Background(), "code", "http://127.0.0.1:1/oauth2/callback", "v")
		_, rerr := ts.client().Refresh(context.Background(), "r")
		for op, err := range map[string]error{"exchange": err, "refresh": rerr} {
			if tc.ok && err != nil {
				t.Errorf("%s, scope %q: %v", op, tc.scope, err)
			}
			if !tc.ok && (!message.IsAuth(err) || !strings.Contains(err.Error(), "Send email on your behalf")) {
				t.Errorf("%s, scope %q: %v, want an auth error explaining the missing permission", op, tc.scope, err)
			}
		}
	}
}

func TestSendInsufficientScopeExplainsFix(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"error":{"code":403,"message":"Request had insufficient authentication scopes."}}`)); err != nil {
			t.Errorf("writing the reply: %v", err)
		}
	}))
	defer s.Close()
	_, err := mustClient(t, "tok", WithBaseURL(s.URL)).SendRaw(context.Background(), "message/rfc822", []byte("x"))
	if !message.IsAuth(err) || !strings.Contains(err.Error(), "auth login --provider gmail") {
		t.Errorf("error = %v, want an auth error with the login hint", err)
	}
}

func TestSendRawClassificationAndContentType(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   message.Kind
	}{{401, message.KindAuth}, {403, message.KindAuth}, {400, message.KindPermanent}, {429, message.KindDeferrable}, {500, message.KindDeferrable}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
		c := mustClient(t, "tok", WithBaseURL(s.URL))
		_, err := c.SendRaw(context.Background(), "message/rfc822", []byte("x"))
		if serr, _ := message.As(err); serr == nil || serr.SendKind() != tc.kind {
			t.Errorf("status %d: %v, want %v", tc.status, err, tc.kind)
		}
		s.Close()
	}
	c := mustClient(t, "tok")
	if _, err := c.SendRaw(context.Background(), "multipart/form-data; boundary=x", []byte("x")); !message.IsPermanent(err) {
		t.Errorf("Mailgun payload accepted: %v", err)
	}
	if _, err := New("", time.Second); !message.IsAuth(err) {
		t.Errorf("empty token: %v, want auth error", err)
	}
	if strings.Contains(c.String(), "tok") {
		t.Error("String() leaks the token")
	}
}

func TestSendMessageEncodesAndRecords(t *testing.T) {
	var got []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Raw string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(body.Raw)
		if err != nil {
			t.Errorf("raw message: %v", err)
		}
		got = raw
		respond(t, w, `{"id":"m1"}`)
	}))
	defer s.Close()
	c := mustClient(t, "tok", WithBaseURL(s.URL))
	msg := &message.Message{From: "me@gmail.com", To: []string{"to@example.com"}, Subject: "s", Text: "hello"}
	resp, err := c.SendMessage(context.Background(), msg)
	if err != nil || resp.ID != "m1" || resp.StatusCode != 200 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	if !bytes.Equal(got, msg.Encoded) || msg.EncodedType != "message/rfc822" || msg.Size != len(got) {
		t.Error("the sent message is not the recorded encoding")
	}
	if _, err := c.SendMessage(context.Background(), &message.Message{TestMode: true}); !message.IsPermanent(err) {
		t.Errorf("test mode: %v, want permanent", err)
	}
	if _, err := c.SendMessage(context.Background(), &message.Message{From: "bogus"}); !message.IsPermanent(err) {
		t.Errorf("invalid sender: %v, want permanent", err)
	}
	if _, err := c.SendRaw(context.Background(), "message/rfc822", make([]byte, maxMessageSize+1)); !message.IsPermanent(err) {
		t.Errorf("oversized: %v, want permanent", err)
	}
}

func TestPKCEAndDefaults(t *testing.T) {
	v, ch, st, err := PKCE()
	if err != nil || len(v) < 43 || ch == "" || st == "" {
		t.Fatalf("PKCE() = %q, %q, %q, %v", v, ch, st, err)
	}
	sum := sha256.Sum256([]byte(v))
	if ch != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("challenge is not S256 of the verifier")
	}
	v2, _, st2, err := PKCE()
	if err != nil || v2 == v || st2 == st {
		t.Errorf("PKCE values repeat (err %v)", err)
	}
	t.Setenv("MAILFIRE_GMAIL_CLIENT_ID", "")
	o := DefaultOAuthClient(0)
	if o.ClientID != BuiltinClientID || o.HTTP.Timeout != DefaultTimeout || o.TokenURL != tokenURL {
		t.Errorf("default client = %+v", o)
	}
	t.Setenv("MAILFIRE_GMAIL_CLIENT_ID", " dev-client ")
	if o := DefaultOAuthClient(5 * time.Second); o.ClientID != "dev-client" || o.HTTP.Timeout != 5*time.Second {
		t.Errorf("override = %+v", o)
	}
	if p := DefaultCredentialPath(); p != "" && !strings.HasSuffix(p, "gmail-credentials.json") {
		t.Errorf("DefaultCredentialPath() = %q", p)
	}
}

func TestErrorFormatting(t *testing.T) {
	e := &Error{Provider: "gmail", Op: "send message", StatusCode: 400, Message: "bad", Kind: message.KindPermanent}
	if e.Error() != "gmail: send message: status 400: bad" || e.ReplyCode() != 400 || e.Detail() != "bad" {
		t.Errorf("error = %q", e.Error())
	}
	if (&Error{Provider: "gmail", Op: "x", Message: "y"}).Error() != "gmail: x: y" {
		t.Error("transport error format")
	}
}

// respond writes a canned reply from a test handler.
func respond(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing the reply: %v", err)
	}
}

func mustWrite(t *testing.T, store *Store, c Credentials) {
	t.Helper()
	if err := store.Write(c); err != nil {
		t.Fatal(err)
	}
}

func mustClient(t *testing.T, token string, opts ...Option) *Client {
	t.Helper()
	c, err := New(token, time.Second, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
