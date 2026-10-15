package mailgun

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

const (
	testAPIKey = "TEST-SECRET-API-KEY-9f8e7d6c"
	testDomain = "mg.example.com"
)

// newTestClient returns a client pointed at a server running handler.
// A *testutil.MockMailgun is used via its own server (m.URL()).
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	var baseURL string
	if m, ok := handler.(*testutil.MockMailgun); ok {
		baseURL = m.URL()
	} else {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		baseURL = srv.URL
	}
	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(baseURL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew(t *testing.T) {
	t.Run("regions", func(t *testing.T) {
		for region, want := range map[string]string{
			"us": "https://api.mailgun.net",
			"eu": "https://api.eu.mailgun.net",
		} {
			c, err := New(testAPIKey, testDomain, region)
			if err != nil {
				t.Fatalf("New(%q): %v", region, err)
			}
			if c.baseURL != want {
				t.Errorf("region %q: baseURL = %q, want %q", region, c.baseURL, want)
			}
		}
	})
	t.Run("unknown region", func(t *testing.T) {
		if _, err := New(testAPIKey, testDomain, "ap"); err == nil {
			t.Fatal("expected error for unknown region")
		}
	})
	t.Run("empty key and domain", func(t *testing.T) {
		if _, err := New("", testDomain, "us"); err == nil {
			t.Fatal("expected error for empty API key")
		}
		if _, err := New(testAPIKey, "", "us"); err == nil {
			t.Fatal("expected error for empty domain")
		}
	})
	t.Run("defaults", func(t *testing.T) {
		c, err := New(testAPIKey, testDomain, "us")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.http.Timeout != DefaultTimeout {
			t.Errorf("timeout = %v, want %v", c.http.Timeout, DefaultTimeout)
		}
	})
	t.Run("options", func(t *testing.T) {
		// Non-positive timeouts are ignored; base URL trailing slash trimmed.
		c, err := New(testAPIKey, testDomain, "us",
			WithTimeout(0), WithBaseURL("http://localhost:9999/"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.http.Timeout != DefaultTimeout {
			t.Errorf("timeout = %v, want default %v", c.http.Timeout, DefaultTimeout)
		}
		if c.baseURL != "http://localhost:9999" {
			t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
		}
		// Custom HTTP client with its own timeout wins.
		c, err = New(testAPIKey, testDomain, "us",
			WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.http.Timeout != 5*time.Second {
			t.Errorf("timeout = %v, want 5s", c.http.Timeout)
		}
		// WithTimeout after WithHTTPClient overrides it.
		c, err = New(testAPIKey, testDomain, "us",
			WithHTTPClient(&http.Client{}), WithTimeout(7*time.Second))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.http.Timeout != 7*time.Second {
			t.Errorf("timeout = %v, want 7s", c.http.Timeout)
		}
	})
}

func TestClientStringOmitsKey(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	c := newTestClient(t, m)
	if s := c.String(); strings.Contains(s, testAPIKey) {
		t.Errorf("String() leaks the API key: %q", s)
	}
}

func TestSendMessageSuccess(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Queued("<abc123@mg.example.com>")
	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(m.URL()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{
		From:    "Ops Bot <ops@example.com>",
		To:      []string{"alice@example.com", "Bob <bob@example.com>"},
		Cc:      []string{"carol@example.com"},
		Bcc:     []string{"dave@example.com"},
		Subject: "Deploy finished",
		Text:    "plain body",
		HTML:    "<p>html body</p>",
		Attachments: []File{
			{Name: "report.txt", Reader: strings.NewReader("report-bytes")},
			{Name: "log.bin", ContentType: "application/x-log",
				Reader: strings.NewReader("\x00\x01log-bytes")},
		},
		Inline: []File{
			{Name: "logo.png", Reader: strings.NewReader("png-bytes")},
		},
	}
	resp, err := c.SendMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp.ID != "<abc123@mg.example.com>" {
		t.Errorf("ID = %q, want %q", resp.ID, "<abc123@mg.example.com>")
	}
	if resp.Message != "Queued. Thank you." {
		t.Errorf("Message = %q", resp.Message)
	}

	captured := m.LastRequest(t)
	if captured.ParseErr != nil {
		t.Fatalf("mock could not parse the multipart body: %v", captured.ParseErr)
	}
	if captured.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", captured.Method)
	}
	if captured.Path != "/v3/"+testDomain+"/messages" {
		t.Errorf("path = %q, want /v3/%s/messages", captured.Path, testDomain)
	}
	if captured.User != "api" {
		t.Errorf("basic auth user = %q, want \"api\"", captured.User)
	}
	if captured.Password != testAPIKey {
		t.Error("basic auth password is not the API key")
	}

	wantFields := map[string][]string{
		"from":    {"Ops Bot <ops@example.com>"},
		"to":      {"alice@example.com", "Bob <bob@example.com>"},
		"cc":      {"carol@example.com"},
		"bcc":     {"dave@example.com"},
		"subject": {"Deploy finished"},
		"text":    {"plain body"},
		"html":    {"<p>html body</p>"},
	}
	for name, want := range wantFields {
		got := captured.Fields[name]
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("field %q = %v, want %v", name, got, want)
		}
	}
	if _, ok := captured.Fields["o:testmode"]; ok {
		t.Error("o:testmode sent although TestMode is false")
	}

	wantFiles := []testutil.RecordedFile{
		{FieldName: "attachment", Filename: "report.txt", Content: []byte("report-bytes")},
		{FieldName: "attachment", Filename: "log.bin", Content: []byte("\x00\x01log-bytes")},
		{FieldName: "inline", Filename: "logo.png", Content: []byte("png-bytes")},
	}
	if len(captured.Files) != len(wantFiles) {
		t.Fatalf("got %d file parts, want %d", len(captured.Files), len(wantFiles))
	}
	for i, want := range wantFiles {
		got := captured.Files[i]
		if got.FieldName != want.FieldName || got.Filename != want.Filename {
			t.Errorf("file %d: %s/%s, want %s/%s", i, got.FieldName, got.Filename, want.FieldName, want.Filename)
		}
		if string(got.Content) != string(want.Content) {
			t.Errorf("file %d (%s): content = %q, want %q", i, got.Filename, got.Content, want.Content)
		}
	}
	// Explicit content type is honored; sniffed type is set when omitted.
	if captured.Files[1].ContentType != "application/x-log" {
		t.Errorf("explicit Content-Type = %q, want application/x-log", captured.Files[1].ContentType)
	}
	if captured.Files[0].ContentType == "" {
		t.Error("sniffed Content-Type missing for report.txt")
	}
}

func TestSendMessageTestMode(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Queued("<t@mg.example.com>")
	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(m.URL()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b", TestMode: true}
	if _, err := c.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	got := m.LastRequest(t).Fields["o:testmode"]
	if len(got) != 1 || got[0] != "yes" {
		t.Errorf("o:testmode = %v, want [yes]", got)
	}
}

func TestSendMessageErrorClassification(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantKind   Kind
		wantSubstr string
	}{
		{"401 unauthorized", 401, `{"message":"Forbidden"}`, KindAuth, "status 401"},
		{"403 forbidden", 403, `{"message":"Domain not allowed"}`, KindAuth, "Domain not allowed"},
		{"400 bad request", 400, `{"message":"'to' parameter is not a valid address"}`, KindPermanent, "not a valid address"},
		{"404 not found", 404, `{"message":"Domain not found"}`, KindPermanent, "status 404"},
		{"422 unprocessable", 422, `{"message":"Need at least one body"}`, KindPermanent, "status 422"},
		{"429 rate limited", 429, `{"message":"Too many requests"}`, KindDeferrable, "Too many requests"},
		{"500 server error", 500, `{"message":"Internal error"}`, KindDeferrable, "status 500"},
		{"502 bad gateway", 502, `{"message":"Bad Gateway"}`, KindDeferrable, "status 502"},
		{"non-JSON error body", 400, `<html>proxy error</html>`, KindPermanent, "proxy error"},
		{"empty error body", 500, ``, KindDeferrable, "Internal Server Error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = testutil.Response{Status: tc.status, Body: tc.body}
			c := newTestClient(t, m)
			msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
				Subject: "s", Text: "b"}
			_, err := c.SendMessage(context.Background(), msg)
			if err == nil {
				t.Fatal("expected error")
			}
			merr, ok := As(err)
			if !ok {
				t.Fatalf("error is %T, want *Error", err)
			}
			if merr.Kind != tc.wantKind {
				t.Errorf("kind = %v, want %v", merr.Kind, tc.wantKind)
			}
			if merr.StatusCode != tc.status {
				t.Errorf("status = %d, want %d", merr.StatusCode, tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q missing %q", err, tc.wantSubstr)
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Errorf("error leaks the API key: %q", err)
			}
			// Helper predicates agree with the classification.
			switch tc.wantKind {
			case KindAuth:
				if !IsAuth(err) || IsDeferrable(err) || IsPermanent(err) {
					t.Errorf("predicates wrong for auth error: %v", err)
				}
			case KindPermanent:
				if !IsPermanent(err) || IsAuth(err) || IsDeferrable(err) {
					t.Errorf("predicates wrong for permanent error: %v", err)
				}
			case KindDeferrable:
				if !IsDeferrable(err) || IsAuth(err) || IsPermanent(err) {
					t.Errorf("predicates wrong for deferrable error: %v", err)
				}
			}
		})
	}
}

func TestErrorNeverContainsAPIKey(t *testing.T) {
	// A malicious or broken server echoes the API key in its error
	// message; the client must redact it.
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid key ` + testAPIKey + ` supplied"}`))
	}))
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b"}
	_, err := c.SendMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("error leaks the API key: %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("error = %q, want redacted placeholder", err)
	}
	// ValidateDomain must redact, too.
	_, err = c.ValidateDomain(context.Background())
	if err == nil || strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("ValidateDomain error = %v, want redacted error", err)
	}
}

func TestSendMessageMalformedSuccessJSON(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON at all": `queued`,
		"JSON without id": `{"message":"Queued."}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = testutil.Response{Status: http.StatusOK, Body: body}
			c := newTestClient(t, m)
			msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
				Subject: "s", Text: "b"}
			_, err := c.SendMessage(context.Background(), msg)
			if err == nil {
				t.Fatal("expected error")
			}
			if !IsDeferrable(err) {
				t.Errorf("kind: %v should be deferrable", err)
			}
			if !strings.Contains(err.Error(), "invalid response") {
				t.Errorf("error = %q, want invalid response", err)
			}
		})
	}
}

func TestSendMessageTimeout(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Timeout(300 * time.Millisecond)
	c, err := New(testAPIKey, testDomain, "us",
		WithBaseURL(m.URL()), WithTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b"}
	_, err = c.SendMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !IsDeferrable(err) {
		t.Errorf("timeout should be deferrable: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want mention of timeout", err)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("error leaks the API key: %q", err)
	}
}

func TestSendMessageNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused from here on

	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(url))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b"}
	_, err = c.SendMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected network error")
	}
	if !IsDeferrable(err) {
		t.Errorf("network error should be deferrable: %v", err)
	}
	if merr, _ := As(err); merr.StatusCode != 0 {
		t.Errorf("status = %d, want 0 for transport error", merr.StatusCode)
	}
}

func TestValidateDomain(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		c, err := New(testAPIKey, testDomain, "eu", WithBaseURL(m.URL()))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		info, err := c.ValidateDomain(context.Background())
		if err != nil {
			t.Fatalf("ValidateDomain: %v", err)
		}
		captured := m.LastRequest(t)
		if captured.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", captured.Method)
		}
		if captured.Path != "/v3/domains/"+testDomain {
			t.Errorf("path = %q, want /v3/domains/%s", captured.Path, testDomain)
		}
		if info.Name != testDomain || info.State != "active" {
			t.Errorf("info = %+v, want {%s active}", info, testDomain)
		}
	})

	t.Run("auth error", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.DomainResponse = testutil.Unauthorized()
		c := newTestClient(t, m)
		_, err := c.ValidateDomain(context.Background())
		if err == nil || !IsAuth(err) {
			t.Fatalf("ValidateDomain: %v, want auth error", err)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.DomainResponse = testutil.Response{Status: http.StatusOK, Body: `{oops`}
		c := newTestClient(t, m)
		_, err := c.ValidateDomain(context.Background())
		if err == nil || !IsDeferrable(err) {
			t.Fatalf("ValidateDomain: %v, want deferrable invalid-response error", err)
		}
	})
}

// errReader always fails, simulating an unreadable attachment.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSendMessageUnreadableAttachment(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	c := newTestClient(t, m)
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b",
		Attachments: []File{{Name: "broken.pdf", Reader: errReader{}}}}
	_, err := c.SendMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsPermanent(err) {
		t.Errorf("local encoding error should be permanent (never reaches Mailgun): %v", err)
	}
	if !strings.Contains(err.Error(), "broken.pdf") {
		t.Errorf("error = %q, want the offending file named", err)
	}
}

func TestAsWithForeignError(t *testing.T) {
	if _, ok := As(io.EOF); ok {
		t.Error("As(io.EOF) = true, want false")
	}
	for _, pred := range []func(error) bool{IsAuth, IsPermanent, IsDeferrable} {
		if pred(io.EOF) {
			t.Error("predicate true for a non-mailgun error")
		}
	}
}

func TestKindString(t *testing.T) {
	want := map[Kind]string{
		KindUnknown:    "unknown",
		KindAuth:       "auth",
		KindPermanent:  "permanent",
		KindDeferrable: "deferrable",
	}
	for k, s := range want {
		if k.String() != s {
			t.Errorf("Kind(%d).String() = %q, want %q", k, k.String(), s)
		}
	}
}

func TestBasicAuthHeaderValue(t *testing.T) {
	// The password must be exactly the API key, sent as Basic Auth.
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Queued("<x>")
	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(m.URL()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b"}
	if _, err := c.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	gotHeader := m.LastRequest(t).Header.Get("Authorization")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("api:"+testAPIKey))
	if gotHeader != want {
		t.Errorf("Authorization header wrong (user or encoding); starts with %q",
			strings.SplitN(gotHeader, " ", 2)[0])
	}
}

func simpleMessage() *Message {
	return &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b"}
}

func TestSendMessageCanceledIsNotDeferrable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := c.SendMessage(ctx, simpleMessage())
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsPermanent(err) {
		t.Errorf("caller cancellation must not be deferrable: %v", err)
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %q, want mention of cancellation", err)
	}
}

func TestSendMessageContextDeadlineIsDeferrable(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.SendMessage(ctx, simpleMessage())
	if err == nil || !IsDeferrable(err) {
		t.Fatalf("SendMessage: %v, want deferrable timeout", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want mention of timeout", err)
	}
}

func TestTruncatedResponseBodyIsDeferrable(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"id":`))
	}))
	_, err := c.SendMessage(context.Background(), simpleMessage())
	if err == nil || !IsDeferrable(err) {
		t.Fatalf("SendMessage: %v, want deferrable read error", err)
	}
	if !strings.Contains(err.Error(), "reading response") {
		t.Errorf("error = %q, want reading response", err)
	}
}

func TestRedactsEncodedKeyForms(t *testing.T) {
	const key = "key/with+special chars&=?"
	var echo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(echo))
	}))
	defer srv.Close()
	c, err := New(key, testDomain, "us", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	forms := map[string]string{
		"raw":         key,
		"query":       url.QueryEscape(key),
		"path":        url.PathEscape(key),
		"basic auth":  "Basic " + base64.StdEncoding.EncodeToString([]byte("api:"+key)),
		"base64 only": base64.StdEncoding.EncodeToString([]byte(key)),
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			echo = `{"message":"got ` + form + `"}`
			_, err := c.SendMessage(context.Background(), simpleMessage())
			if err == nil {
				t.Fatal("expected error")
			}
			for _, f := range forms {
				if strings.Contains(err.Error(), strings.TrimPrefix(f, "Basic ")) {
					t.Fatalf("error leaks key form %q: %q", f, err)
				}
			}
			if !strings.Contains(err.Error(), "[redacted]") {
				t.Errorf("error = %q, want redacted placeholder", err)
			}
		})
	}
}

func TestErrorMessageTruncatedAfterRedaction(t *testing.T) {
	// The key straddles the truncation boundary; truncating first would
	// leak a prefix of it.
	msg := strings.Repeat("x", maxErrorMessage-5) + testAPIKey + strings.Repeat("y", 500)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"` + msg + `"}`))
	}))
	_, err := c.SendMessage(context.Background(), simpleMessage())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testAPIKey[:5]) {
		t.Fatalf("error leaks part of the key: %q", err)
	}
	merr, _ := As(err)
	if n := utf8.RuneCountInString(merr.Message); n > maxErrorMessage+1 {
		t.Errorf("message has %d runes, want ≤ %d", n, maxErrorMessage+1)
	}
	if !strings.HasSuffix(merr.Message, "…") {
		t.Errorf("message = %q, want ellipsis", merr.Message)
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	got := truncate(strings.Repeat("ä", 10), 3)
	if got != "äää…" {
		t.Errorf("truncate = %q, want äää…", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate = %q, want short", got)
	}
}

func TestWithTimeoutDoesNotMutateCallerClient(t *testing.T) {
	hc := &http.Client{Timeout: 7 * time.Second}
	c, err := New(testAPIKey, testDomain, "us", WithHTTPClient(hc), WithTimeout(time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if hc.Timeout != 7*time.Second {
		t.Errorf("caller's client mutated: Timeout = %s", hc.Timeout)
	}
	if c.http.Timeout != time.Second || c.timeout != time.Second {
		t.Errorf("client timeout = %s/%s, want 1s", c.http.Timeout, c.timeout)
	}
}

func TestWithHTTPClientZeroTimeout(t *testing.T) {
	c, err := New(testAPIKey, testDomain, "us", WithHTTPClient(&http.Client{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != 0 {
		t.Errorf("timeout = %s, want 0 (mirrors the supplied client)", c.timeout)
	}
	kind, msg := c.classifyTransport(&url.Error{Op: "Post", URL: "http://x", Err: timeoutErr{}})
	if kind != KindDeferrable || !strings.Contains(msg, "timed out") {
		t.Errorf("classifyTransport = %v %q, want deferrable timeout", kind, msg)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestSendMessageNilAttachmentReader(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	c := newTestClient(t, m)
	msg := simpleMessage()
	msg.Attachments = []File{{Name: "empty.bin"}}
	_, err := c.SendMessage(context.Background(), msg)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("SendMessage: %v, want permanent encoding error", err)
	}
}

func TestLargeAttachmentStreamedAndSniffed(t *testing.T) {
	content := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte("0123456789"), 100_000)...)
	small := []byte("hi")
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Queued("<x>")
	c := newTestClient(t, m)
	msg := simpleMessage()
	msg.Attachments = []File{
		{Name: "big.pdf", Reader: bytes.NewReader(content)},
		{Name: "small.txt", Reader: bytes.NewReader(small)},
		{Name: "empty.txt", Reader: bytes.NewReader(nil)},
	}
	if _, err := c.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	got := m.LastRequest(t)
	if got.ParseErr != nil {
		t.Fatalf("mock could not parse the multipart body: %v", got.ParseErr)
	}
	if len(got.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(got.Files))
	}
	if !bytes.Equal(got.Files[0].Content, content) {
		t.Errorf("big attachment corrupted: %d bytes, want %d", len(got.Files[0].Content), len(content))
	}
	if got.Files[0].ContentType != "application/pdf" {
		t.Errorf("big content type = %q, want application/pdf", got.Files[0].ContentType)
	}
	if !bytes.Equal(got.Files[1].Content, small) {
		t.Errorf("small attachment = %q, want %q", got.Files[1].Content, small)
	}
	if len(got.Files[2].Content) != 0 {
		t.Errorf("empty attachment has %d bytes", len(got.Files[2].Content))
	}
}
