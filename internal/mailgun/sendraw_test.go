package mailgun

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// SendRaw replays a stored request verbatim: the exact bytes with the
// exact Content-Type, and the same response classification as SendMessage
// (PRD FR-4).
func TestSendRawSuccess(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	c := newTestClient(t, m)

	body := []byte("--boundary\r\nContent-Disposition: form-data; name=\"to\"\r\n\r\na@example.com\r\n--boundary--\r\n")
	contentType := "multipart/form-data; boundary=boundary"
	resp, err := c.SendRaw(context.Background(), contentType, body)
	if err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	if resp.ID != "<abc123@mg.example.com>" {
		t.Errorf("ID = %q, want the mock's queued ID", resp.ID)
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}

	req := m.LastRequest(t)
	if !bytes.Equal(req.Body, body) {
		t.Errorf("request body differs from the replayed bytes")
	}
	if got := req.Header.Get("Content-Type"); got != contentType {
		t.Errorf("Content-Type = %q, want %q", got, contentType)
	}
	if req.User != "api" || req.Password != testAPIKey {
		t.Errorf("basic auth = %q/%q, want api/<key>", req.User, req.Password)
	}
	if !strings.HasSuffix(req.Path, "/v3/"+testDomain+"/messages") {
		t.Errorf("path = %q, want .../v3/%s/messages", req.Path, testDomain)
	}
}

// The failure classification is SendMessage's: auth, permanent and
// deferrable map to the same kinds so the caller's exit codes and
// deferral decisions are identical.
func TestSendRawErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp testutil.Response
		kind Kind
	}{
		{"auth", testutil.Unauthorized(), KindAuth},
		{"permanent", testutil.BadRequest(), KindPermanent},
		{"deferrable", testutil.InternalError(), KindDeferrable},
		{"rate limited", testutil.RateLimited(), KindDeferrable},
		{"malformed success", testutil.MalformedJSON(), KindDeferrable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = tc.resp
			c := newTestClient(t, m)

			_, err := c.SendRaw(context.Background(), "multipart/form-data; boundary=x", []byte("x"))
			if err == nil {
				t.Fatal("want error, got nil")
			}
			merr, ok := As(err)
			if !ok {
				t.Fatalf("error is not a *Error: %T %v", err, err)
			}
			if merr.Kind != tc.kind {
				t.Errorf("kind = %v, want %v", merr.Kind, tc.kind)
			}
			if merr.Op != "resend message" {
				t.Errorf("op = %q, want %q", merr.Op, "resend message")
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Errorf("error leaks the API key: %v", err)
			}
		})
	}
}

// An empty body or content type means there is nothing meaningful to
// replay; that is a permanent error and must not reach the server.
func TestSendRawEmptyInput(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	c := newTestClient(t, m)

	for _, tc := range []struct {
		name        string
		contentType string
		body        []byte
	}{
		{"empty body", "multipart/form-data; boundary=x", nil},
		{"empty content type", "", []byte("x")},
		{"both empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.SendRaw(context.Background(), tc.contentType, tc.body)
			if !IsPermanent(err) {
				t.Errorf("err = %v, want a permanent error", err)
			}
		})
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
}
