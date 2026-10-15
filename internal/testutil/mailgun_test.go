package testutil_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// TestGolden is the reference example for golden-file testing: later
// tasks (template rendering, Markdown) follow this pattern.
func TestGolden(t *testing.T) {
	rendered := "<p>hello, golden world</p>\n"
	testutil.GoldenString(t, "hello.golden", rendered)
}

func sendTestMessage(c *mailgun.Client) error {
	_, err := c.SendMessage(context.Background(), &mailgun.Message{
		From: "ops@example.com", To: []string{"a@example.com"},
		Subject: "s", Text: "b",
	})
	return err
}

// newClient returns a mailgun client authenticated with key and pointed
// at the mock.
func newClient(t *testing.T, m *testutil.MockMailgun, key string, opts ...mailgun.Option) *mailgun.Client {
	t.Helper()
	opts = append([]mailgun.Option{mailgun.WithBaseURL(m.URL())}, opts...)
	c, err := mailgun.New(key, m.Domain, "us", opts...)
	if err != nil {
		t.Fatalf("mailgun.New: %v", err)
	}
	return c
}

// TestMockMailgun proves the shared mock records requests and serves
// programmable responses, driving the real client through it.
func TestMockMailgun(t *testing.T) {
	const key = "TEST-KEY-abc123"

	t.Run("success with recorded request", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.Queued("<msg-42@mg.example.com>")
		c := newClient(t, m, key)
		resp, err := c.SendMessage(context.Background(), &mailgun.Message{
			From:    "Ops <ops@example.com>",
			To:      []string{"a@example.com"},
			Subject: "hi",
			Text:    "body",
			Attachments: []mailgun.File{
				{Name: "f.txt", Reader: strings.NewReader("file-bytes")},
			},
		})
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if resp.ID != "<msg-42@mg.example.com>" {
			t.Errorf("ID = %q, want <msg-42@mg.example.com>", resp.ID)
		}
		req := m.LastRequest(t)
		if req.Method != "POST" || req.Path != "/v3/"+m.Domain+"/messages" {
			t.Errorf("request = %s %s, want POST /v3/%s/messages", req.Method, req.Path, m.Domain)
		}
		if req.User != "api" || req.Password != key {
			t.Errorf("basic auth user = %q, want api", req.User)
		}
		if got := req.Field("subject"); got != "hi" {
			t.Errorf("subject field = %q, want hi", got)
		}
		if len(req.Files) != 1 || req.Files[0].Filename != "f.txt" ||
			string(req.Files[0].Content) != "file-bytes" {
			t.Errorf("files = %+v, want one f.txt with file-bytes", req.Files)
		}
	})

	t.Run("failure classes", func(t *testing.T) {
		cases := []struct {
			name string
			resp testutil.Response
			want mailgun.Kind
		}{
			{"401", testutil.Unauthorized(), mailgun.KindAuth},
			{"403", testutil.Forbidden(), mailgun.KindAuth},
			{"400", testutil.BadRequest(), mailgun.KindPermanent},
			{"404", testutil.NotFound(), mailgun.KindPermanent},
			{"422", testutil.Unprocessable(), mailgun.KindPermanent},
			{"429", testutil.RateLimited(), mailgun.KindDeferrable},
			{"500", testutil.InternalError(), mailgun.KindDeferrable},
			{"malformed JSON", testutil.MalformedJSON(), mailgun.KindDeferrable},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				m := testutil.NewMockMailgun(t)
				m.SendResponse = tc.resp
				c := newClient(t, m, key)
				err := sendTestMessage(c)
				if err == nil {
					t.Fatal("expected error")
				}
				merr, ok := mailgun.As(err)
				if !ok {
					t.Fatalf("error is %T, want *mailgun.Error", err)
				}
				if merr.Kind != tc.want {
					t.Errorf("kind = %v, want %v", merr.Kind, tc.want)
				}
				if strings.Contains(err.Error(), key) {
					t.Errorf("error leaks the API key: %q", err)
				}
			})
		}
	})

	t.Run("timeout", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.Timeout(300 * time.Millisecond)
		c := newClient(t, m, key, mailgun.WithTimeout(50*time.Millisecond))
		err := sendTestMessage(c)
		if err == nil || !mailgun.IsDeferrable(err) {
			t.Fatalf("SendMessage: %v, want deferrable timeout", err)
		}
	})

	t.Run("domain lookup", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		c := newClient(t, m, key)
		info, err := c.ValidateDomain(context.Background())
		if err != nil {
			t.Fatalf("ValidateDomain: %v", err)
		}
		if info.Name != m.Domain || info.State != "active" {
			t.Errorf("info = %+v, want {%s active}", info, m.Domain)
		}
		req := m.LastRequest(t)
		if req.Method != "GET" || req.Path != "/v3/domains/"+m.Domain {
			t.Errorf("request = %s %s, want GET /v3/domains/%s", req.Method, req.Path, m.Domain)
		}
	})

	t.Run("request parsing edge cases", func(t *testing.T) {
		cases := []struct {
			name        string
			contentType string
			body        string
			wantFields  bool
			wantErr     bool
		}{
			{"no content type", "", "raw", false, false},
			{"non-multipart", "application/json", `{"a":1}`, false, false},
			{"missing boundary", "multipart/form-data", "--x\r\n", true, true},
			{"truncated multipart", "multipart/form-data; boundary=x",
				"--x\r\nContent-Disposition: form-data; name=\"subject\"\r\n\r\nhi", true, true},
			{"valid multipart", "multipart/form-data; boundary=x",
				"--x\r\nContent-Disposition: form-data; name=\"subject\"\r\n\r\nhi\r\n--x--\r\n", true, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				m := testutil.NewMockMailgun(t)
				req, err := http.NewRequest(http.MethodPost, m.URL()+"/v3/"+m.Domain+"/messages",
					strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				if tc.contentType != "" {
					req.Header.Set("Content-Type", tc.contentType)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				rec := m.LastRequest(t)
				if string(rec.Body) != tc.body {
					t.Errorf("Body = %q, want %q", rec.Body, tc.body)
				}
				if (rec.Fields != nil) != tc.wantFields {
					t.Errorf("Fields = %v, want parsed=%v", rec.Fields, tc.wantFields)
				}
				if (rec.ParseErr != nil) != tc.wantErr {
					t.Errorf("ParseErr = %v, want error=%v", rec.ParseErr, tc.wantErr)
				}
				if tc.name == "valid multipart" && rec.Field("subject") != "hi" {
					t.Errorf("subject = %q, want hi", rec.Field("subject"))
				}
			})
		}
	})

	t.Run("custom handler still records", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendHandler = func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(`{"message":"teapot"}`))
		}
		c := newClient(t, m, key)
		if err := sendTestMessage(c); err == nil {
			t.Fatal("expected error")
		}
		if got := len(m.Requests()); got != 1 {
			t.Errorf("recorded %d requests, want 1", got)
		}
	})
}
