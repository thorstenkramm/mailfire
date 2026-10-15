package mailgun

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testAPIKey = "TEST-SECRET-API-KEY-9f8e7d6c"
	testDomain = "mg.example.com"
)

// newTestClient returns a client pointed at a server running handler.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// multipartRequest captures a parsed multipart/form-data request.
type multipartRequest struct {
	fields map[string][]string
	files  []capturedFile
}

type capturedFile struct {
	fieldName   string
	filename    string
	contentType string
	content     []byte
}

// parseMultipart reads the whole multipart body and records fields and
// file parts in order.
func parseMultipart(t *testing.T, r *http.Request) multipartRequest {
	t.Helper()
	ct := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", ct)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	got := multipartRequest{fields: map[string][]string{}}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading multipart body: %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading part %q: %v", part.FormName(), err)
		}
		if fn := part.FileName(); fn != "" {
			got.files = append(got.files, capturedFile{
				fieldName:   part.FormName(),
				filename:    fn,
				contentType: part.Header.Get("Content-Type"),
				content:     data,
			})
		} else {
			got.fields[part.FormName()] = append(got.fields[part.FormName()], string(data))
		}
	}
	return got
}

func okHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"<abc123@mg.example.com>","message":"Queued. Thank you."}`))
	})
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
	c := newTestClient(t, okHandler(t))
	if s := c.String(); strings.Contains(s, testAPIKey) {
		t.Errorf("String() leaks the API key: %q", s)
	}
}

func TestSendMessageSuccess(t *testing.T) {
	var captured multipartRequest
	var gotAuth, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		user, pass, _ := r.BasicAuth()
		gotAuth = user + ":" + pass
		captured = parseMultipart(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"<abc123@mg.example.com>","message":"Queued. Thank you."}`))
	}))
	defer srv.Close()

	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(srv.URL))
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

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v3/"+testDomain+"/messages" {
		t.Errorf("path = %q, want /v3/%s/messages", gotPath, testDomain)
	}
	if gotAuth != "api:"+testAPIKey {
		t.Errorf("basic auth = user %q", strings.SplitN(gotAuth, ":", 2)[0])
		if !strings.HasPrefix(gotAuth, "api:") {
			t.Error("basic auth user is not \"api\"")
		}
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
		got := captured.fields[name]
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("field %q = %v, want %v", name, got, want)
		}
	}
	if _, ok := captured.fields["o:testmode"]; ok {
		t.Error("o:testmode sent although TestMode is false")
	}

	wantFiles := []capturedFile{
		{fieldName: "attachment", filename: "report.txt", content: []byte("report-bytes")},
		{fieldName: "attachment", filename: "log.bin", content: []byte("\x00\x01log-bytes")},
		{fieldName: "inline", filename: "logo.png", content: []byte("png-bytes")},
	}
	if len(captured.files) != len(wantFiles) {
		t.Fatalf("got %d file parts, want %d", len(captured.files), len(wantFiles))
	}
	for i, want := range wantFiles {
		got := captured.files[i]
		if got.fieldName != want.fieldName || got.filename != want.filename {
			t.Errorf("file %d: %s/%s, want %s/%s", i, got.fieldName, got.filename, want.fieldName, want.filename)
		}
		if string(got.content) != string(want.content) {
			t.Errorf("file %d (%s): content = %q, want %q", i, got.filename, got.content, want.content)
		}
	}
	// Explicit content type is honored; sniffed type is set when omitted.
	if captured.files[1].contentType != "application/x-log" {
		t.Errorf("explicit Content-Type = %q, want application/x-log", captured.files[1].contentType)
	}
	if captured.files[0].contentType == "" {
		t.Error("sniffed Content-Type missing for report.txt")
	}
}

func TestSendMessageTestMode(t *testing.T) {
	var captured multipartRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = parseMultipart(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"<t@mg.example.com>","message":"Queued."}`))
	}))
	defer srv.Close()

	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b", TestMode: true}
	if _, err := c.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	got := captured.fields["o:testmode"]
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
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
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
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"id":"<x>","message":"ok"}`))
	}))
	defer srv.Close()

	c, err := New(testAPIKey, testDomain, "us",
		WithBaseURL(srv.URL), WithTimeout(50*time.Millisecond))
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
		var gotPath, gotMethod string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"domain":{"name":"mg.example.com","state":"active","spam_action":"disabled"}}`))
		}))
		defer srv.Close()

		c, err := New(testAPIKey, testDomain, "eu", WithBaseURL(srv.URL))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		info, err := c.ValidateDomain(context.Background())
		if err != nil {
			t.Fatalf("ValidateDomain: %v", err)
		}
		if gotMethod != http.MethodGet {
			t.Errorf("method = %q, want GET", gotMethod)
		}
		if gotPath != "/v3/domains/"+testDomain {
			t.Errorf("path = %q, want /v3/domains/%s", gotPath, testDomain)
		}
		if info.Name != testDomain || info.State != "active" {
			t.Errorf("info = %+v, want {%s active}", info, testDomain)
		}
	})

	t.Run("auth error", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
		}))
		_, err := c.ValidateDomain(context.Background())
		if err == nil || !IsAuth(err) {
			t.Fatalf("ValidateDomain: %v, want auth error", err)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{oops`))
		}))
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
	c := newTestClient(t, okHandler(t))
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
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"<x>","message":"ok"}`))
	}))
	defer srv.Close()

	c, err := New(testAPIKey, testDomain, "us", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msg := &Message{From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b"}
	if _, err := c.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("api:"+testAPIKey))
	if gotHeader != want {
		t.Errorf("Authorization header wrong (user or encoding); starts with %q",
			strings.SplitN(gotHeader, " ", 2)[0])
	}
}
