// Package testutil provides shared test infrastructure for mailfire: a
// programmable mock of the Mailgun v3 API (mailgun.go) and golden-file
// helpers (golden.go).
//
// The package is imported by tests only; none of it ships in the binary.
package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// RecordedFile is one file part of a recorded multipart request.
type RecordedFile struct {
	FieldName   string // form field name ("attachment" or "inline")
	Filename    string
	ContentType string
	Content     []byte
}

// RecordedRequest captures one HTTP request received by a MockMailgun.
// Multipart/form-data bodies (message sends) are parsed into Fields and
// Files; Body always holds the raw request body. ParseErr is set when a
// multipart body could not be parsed completely, so a test can tell a
// client encoding bug from a missing field.
type RecordedRequest struct {
	Method   string
	Path     string
	Header   http.Header
	User     string // Basic Auth user (Mailgun: always "api")
	Password string // Basic Auth password (the API key)
	Body     []byte
	Fields   map[string][]string // multipart form fields, in order per name
	Files    []RecordedFile      // multipart file parts, in order
	ParseErr error               // body read or multipart parse failure
}

// Field returns the first value of multipart form field name, or "".
func (r RecordedRequest) Field(name string) string {
	if vs := r.Fields[name]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// Response describes a canned reply of the mock server.
type Response struct {
	Status  int    // HTTP status code; 0 means 200
	Body    string // response body
	Delay   time.Duration
	Headers map[string]string // extra headers; Content-Type defaults to application/json
}

// Queued returns Mailgun's success reply for a send: HTTP 200 with the
// given message ID.
func Queued(messageID string) Response {
	return Response{Status: http.StatusOK,
		Body: fmt.Sprintf(`{"id":%q,"message":"Queued. Thank you."}`, messageID)}
}

// ErrorJSON returns a Mailgun-style error reply ({"message": ...}) with
// the given status code.
func ErrorJSON(status int, message string) Response {
	return Response{Status: status, Body: fmt.Sprintf(`{"message":%q}`, message)}
}

// Canned replies for the failure classes the client must distinguish
// (PRD §8): auth (401/403), permanent (other 4xx) and deferrable (429,
// 5xx, timeout, unparseable success body).
func Unauthorized() Response { return ErrorJSON(http.StatusUnauthorized, "Forbidden") }
func Forbidden() Response    { return ErrorJSON(http.StatusForbidden, "Domain not allowed") }
func BadRequest() Response {
	return ErrorJSON(http.StatusBadRequest, "'to' parameter is not a valid address")
}
func NotFound() Response { return ErrorJSON(http.StatusNotFound, "Domain not found") }
func Unprocessable() Response {
	return ErrorJSON(http.StatusUnprocessableEntity, "Need at least one body")
}
func RateLimited() Response   { return ErrorJSON(http.StatusTooManyRequests, "Too many requests") }
func InternalError() Response { return ErrorJSON(http.StatusInternalServerError, "Internal error") }

// Timeout stalls for d before replying; use with a client timeout well
// below d to exercise client-side timeout handling.
func Timeout(d time.Duration) Response {
	return Response{Status: http.StatusOK, Body: `{"id":"<x>","message":"ok"}`, Delay: d}
}

// MalformedJSON replies 200 with a body that is not valid JSON.
func MalformedJSON() Response {
	return Response{Status: http.StatusOK, Body: `not json`}
}

// MockMailgun is an httptest.Server speaking the subset of the Mailgun
// v3 API mailfire uses: POST /v3/{domain}/messages and
// GET /v3/domains/{domain}. Every request is recorded for assertions.
//
//	m := testutil.NewMockMailgun(t)
//	m.SendResponse = testutil.RateLimited()
//	// ... point a client at m.URL() and exercise it ...
//	req := m.LastRequest(t)
//	if got := req.Field("subject"); got != "hi" { ... }
//
// The zero-value canned replies are a successful queued send and an
// active domain. Set SendHandler/DomainHandler for full control; the
// request is still recorded first.
type MockMailgun struct {
	// Domain is the sending domain the mock claims to serve.
	Domain string

	// SendResponse is the canned reply for POST .../messages.
	SendResponse Response
	// DomainResponse is the canned reply for GET /v3/domains/{domain}.
	DomainResponse Response
	// SendHandler, when non-nil, handles POST .../messages instead of
	// SendResponse.
	SendHandler http.HandlerFunc
	// DomainHandler, when non-nil, handles GET /v3/domains/... instead
	// of DomainResponse.
	DomainHandler http.HandlerFunc

	mu       sync.Mutex
	requests []RecordedRequest
	srv      *httptest.Server
}

// NewMockMailgun starts a mock server with successful default replies
// and registers its cleanup with t.
func NewMockMailgun(t *testing.T) *MockMailgun {
	t.Helper()
	m := &MockMailgun{Domain: "mg.example.com"}
	m.SendResponse = Queued("<abc123@mg.example.com>")
	m.DomainResponse = Response{Status: http.StatusOK, Body: fmt.Sprintf(
		`{"domain":{"name":%q,"state":"active","spam_action":"disabled"}}`, m.Domain)}
	m.srv = httptest.NewServer(m)
	t.Cleanup(m.srv.Close)
	return m
}

// URL is the mock server's base URL, for mailgun.WithBaseURL.
func (m *MockMailgun) URL() string { return m.srv.URL }

// Requests returns a copy of all recorded requests, in arrival order.
func (m *MockMailgun) Requests() []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RecordedRequest(nil), m.requests...)
}

// LastRequest returns the most recent recorded request.
func (m *MockMailgun) LastRequest(t *testing.T) RecordedRequest {
	t.Helper()
	reqs := m.Requests()
	if len(reqs) == 0 {
		t.Fatal("testutil: mock received no requests")
	}
	return reqs[len(reqs)-1]
}

// ServeHTTP records the request and replies per the configured endpoint.
func (m *MockMailgun) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := recordRequest(r)
	m.mu.Lock()
	m.requests = append(m.requests, rec)
	sendResp, domainResp := m.SendResponse, m.DomainResponse
	sendH, domainH := m.SendHandler, m.DomainHandler
	m.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
		if sendH != nil {
			sendH(w, r)
			return
		}
		writeResponse(w, r, sendResp)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v3/domains/"):
		if domainH != nil {
			domainH(w, r)
			return
		}
		writeResponse(w, r, domainResp)
	default:
		http.NotFound(w, r)
	}
}

// recordRequest reads the whole body, parsing multipart/form-data into
// fields and files. Bodies without a multipart Content-Type are recorded
// raw only. On a parse failure the parts read so far are kept and
// ParseErr is set; Body always holds the raw bytes received.
func recordRequest(r *http.Request) RecordedRequest {
	rec := RecordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
	}
	rec.User, rec.Password, _ = r.BasicAuth()
	body, err := io.ReadAll(r.Body)
	rec.Body = body
	if err != nil {
		rec.ParseErr = fmt.Errorf("reading body: %w", err)
		return rec
	}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return rec
	}
	rec.Fields = map[string][]string{}
	if params["boundary"] == "" {
		rec.ParseErr = errors.New("multipart/form-data without boundary")
		return rec
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			rec.ParseErr = fmt.Errorf("multipart: %w", err)
			break
		}
		data, err := io.ReadAll(part)
		if err != nil {
			rec.ParseErr = fmt.Errorf("multipart part %q: %w", part.FormName(), err)
			break
		}
		if fn := part.FileName(); fn != "" {
			rec.Files = append(rec.Files, RecordedFile{
				FieldName:   part.FormName(),
				Filename:    fn,
				ContentType: part.Header.Get("Content-Type"),
				Content:     data,
			})
		} else {
			rec.Fields[part.FormName()] = append(rec.Fields[part.FormName()], string(data))
		}
	}
	return rec
}

// writeResponse writes a canned reply, honoring its Delay.
func writeResponse(w http.ResponseWriter, r *http.Request, resp Response) {
	if resp.Delay > 0 {
		select {
		case <-time.After(resp.Delay):
		case <-r.Context().Done():
			return
		}
	}
	ct := "application/json"
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
		if strings.EqualFold(k, "Content-Type") {
			ct = ""
		}
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, resp.Body)
}
