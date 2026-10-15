package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// deferConfig writes a config with [defer].defer_dir set to a not-yet-
// created directory inside a temp dir, so deferral exercises the
// on-the-fly creation, and returns the config path and the defer_dir.
// Paths go into TOML literal strings, so Windows backslashes survive.
func deferConfig(t *testing.T, extra string) (cfgPath, deferDir string) {
	t.Helper()
	deferDir = filepath.Join(t.TempDir(), "deferred")
	cfg := "from = \"ops@example.com\"\n\n[defer]\ndefer_dir = '" + deferDir + "'\n" + extra
	return writeConfig(t, cfg), deferDir
}

// deferredIDs returns the names of the message directories in defer_dir.
func deferredIDs(t *testing.T, deferDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(deferDir)
	if err != nil {
		t.Fatalf("reading defer_dir: %v", err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.Name())
	}
	return ids
}

// readDeferred reads the stored message id: its metadata and body.
func readDeferred(t *testing.T, deferDir, id string) (deferral.Metadata, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(deferDir, id, deferral.MetaFile))
	if err != nil {
		t.Fatalf("reading %s: %v", deferral.MetaFile, err)
	}
	var meta deferral.Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("%s is not valid JSON: %v", deferral.MetaFile, err)
	}
	body, err := os.ReadFile(filepath.Join(deferDir, id, deferral.BodyFile))
	if err != nil {
		t.Fatalf("reading %s: %v", deferral.BodyFile, err)
	}
	return meta, body
}

// multipartFields parses a stored request body into its form fields.
func multipartFields(t *testing.T, meta deferral.Metadata, body []byte) map[string]string {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(meta.ContentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content_type %q does not parse as multipart/form-data: %v", meta.ContentType, err)
	}
	if params["boundary"] == "" {
		t.Fatalf("content_type %q has no boundary", meta.ContentType)
	}
	fields := map[string]string{}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading part: %v", err)
		}
		fields[part.FormName()] = string(data)
	}
	return fields
}

// TestDeferDeferrableFailures: 5xx, 429, and an unparseable success body
// all persist the exact encoded request and exit 4 (FR-4, PRD §8).
func TestDeferDeferrableFailures(t *testing.T) {
	for _, resp := range []testutil.Response{
		testutil.InternalError(),
		testutil.RateLimited(),
		testutil.MalformedJSON(),
	} {
		t.Run(fmt.Sprintf("status-%d", resp.Status), func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = resp
			cfg, deferDir := deferConfig(t, "")

			before := time.Now().UTC()
			code, stdout, stderr := runMock(t, m, "the body",
				"-c", cfg, "--to", "a@example.com", "--subject", "the subject")
			if code != ExitDeferred {
				t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "message saved as") || !strings.Contains(stderr, deferDir) {
				t.Errorf("stderr missing deferral note:\n%s", stderr)
			}

			ids := deferredIDs(t, deferDir)
			if len(ids) != 1 {
				t.Fatalf("got %d deferred messages, want 1", len(ids))
			}
			meta, body := readDeferred(t, deferDir, ids[0])

			// Metadata: everything `resend --list` shows, without parsing
			// the body.
			if meta.ID != ids[0] {
				t.Errorf("meta.ID = %q, want the directory name %q", meta.ID, ids[0])
			}
			if len(meta.To) != 1 || meta.To[0] != "a@example.com" {
				t.Errorf("meta.To = %v", meta.To)
			}
			if meta.Subject != "the subject" {
				t.Errorf("meta.Subject = %q", meta.Subject)
			}
			if meta.Size != int64(len(body)) {
				t.Errorf("meta.Size = %d, want body.bin length %d", meta.Size, len(body))
			}
			if meta.Timestamp.Before(before) || meta.Timestamp.After(time.Now().UTC()) {
				t.Errorf("meta.Timestamp = %v, want within the test run", meta.Timestamp)
			}

			// body.bin replays byte-exactly: it must parse as the very
			// multipart request the mock server received.
			fields := multipartFields(t, meta, body)
			want := map[string]string{
				"from":    "ops@example.com",
				"to":      "a@example.com",
				"subject": "the subject",
				"text":    "the body",
			}
			for k, v := range want {
				if fields[k] != v {
					t.Errorf("field %q = %q, want %q", k, fields[k], v)
				}
			}
			if _, ok := fields["o:testmode"]; ok {
				t.Error("stored body carries o:testmode without --test")
			}

			// The stored bytes equal the bytes the server saw.
			if got := m.LastRequest(t).Body; !bytes.Equal(got, body) {
				t.Errorf("body.bin differs from the request the server received")
			}
		})
	}
}

// A transport error (connection refused) is deferrable like a 5xx.
func TestDeferNetworkError(t *testing.T) {
	// Grab a port and release it, so connecting is refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String()
	l.Close()

	cfg, deferDir := deferConfig(t, "")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-c", cfg, "--to", "a@example.com", "--subject", "s"},
		&stdout, &stderr, testInfo,
		WithStdin(strings.NewReader("body")), WithBaseURL(url))
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr.String())
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 {
		t.Errorf("got %d deferred messages, want 1", len(ids))
	}
}

// --no-defer overrides defer = true: nothing is stored, exit 5 (FR-4).
func TestDeferDisabledByFlag(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--no-defer")
	if code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stderr, "--no-defer") || !strings.Contains(stderr, "NOT saved") {
		t.Errorf("stderr missing no-defer note:\n%s", stderr)
	}
	if _, err := os.Stat(deferDir); !os.IsNotExist(err) {
		t.Errorf("defer_dir exists despite --no-defer: %v", err)
	}
}

// defer = false: nothing is stored even with a defer_dir set, exit 5.
func TestDeferDisabledInConfig(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "defer = false\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stderr, "defer = false") || !strings.Contains(stderr, "NOT saved") {
		t.Errorf("stderr missing defer=false note:\n%s", stderr)
	}
	if _, err := os.Stat(deferDir); !os.IsNotExist(err) {
		t.Errorf("defer_dir exists despite defer = false: %v", err)
	}
}

// Permanent failures (4xx other than 429) and auth failures never defer,
// no matter the configuration.
func TestDeferNeverForPermanentOrAuth(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp testutil.Response
		want int
	}{
		{"permanent 400", testutil.BadRequest(), ExitSendFail},
		{"auth 401", testutil.Unauthorized(), ExitAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = tc.resp
			cfg, deferDir := deferConfig(t, "")

			code, _, _ := runMock(t, m, "body",
				"-c", cfg, "--to", "a@example.com", "--subject", "s")
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
			if _, err := os.Stat(deferDir); !os.IsNotExist(err) {
				t.Errorf("defer_dir exists after a non-deferrable failure: %v", err)
			}
		})
	}
}

// Design decision (review 12): --test sends are never deferred — test
// mode validates the request, it does not deliver, and the stored body
// would carry o:testmode=yes into a resend (FR-1, Decision Q5).
func TestDeferTestSendNeverDeferred(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--test")
	if code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stderr, "test") || !strings.Contains(stderr, "never deferred") {
		t.Errorf("stderr missing the test-mode explanation:\n%s", stderr)
	}
	if got := m.LastRequest(t).Field("o:testmode"); got != "yes" {
		t.Errorf("o:testmode = %q, want %q (the send itself was a test send)", got, "yes")
	}
	if _, err := os.Stat(deferDir); !os.IsNotExist(err) {
		t.Errorf("defer_dir exists after a failed --test send: %v", err)
	}
}

// When the defer_dir exceeds [defer].max_size, the oldest deferred
// message is dropped; the drop is reported on stderr (FR-4).
func TestDeferMaxSizeEvictsOldest(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "max_size = \"3000\"\n")

	// An old, big deferred message: it cannot fit together with the new
	// one under max_size.
	seeder := &deferral.Store{Dir: deferDir}
	old, err := seeder.Save([]string{"old@example.com"}, "old subject",
		make([]byte, 10000), "multipart/form-data; boundary=seed")
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	if !strings.Contains(stderr, "dropped") || !strings.Contains(stderr, old.ID) {
		t.Errorf("stderr missing the drop report:\n%s", stderr)
	}

	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 || ids[0] == old.ID {
		t.Errorf("defer_dir = %v, want only the new message", ids)
	}
	meta, _ := readDeferred(t, deferDir, ids[0])
	if meta.Subject != "s" {
		t.Errorf("surviving message subject = %q, want the new one", meta.Subject)
	}
}

// Every drop is also logged when logging is enabled (FR-4), and the
// deferred attempt itself is a failure for logging (status recorded).
func TestDeferDropLoggedAndAttemptLoggedAsFailure(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	logfile := filepath.Join(t.TempDir(), "mailfire.log")
	cfg, deferDir := deferConfig(t, "max_size = \"3000\"\n\n[logging]\nlogfile = '"+logfile+"'\n")

	seeder := &deferral.Store{Dir: deferDir}
	old, err := seeder.Save([]string{"old@example.com"}, "old subject",
		make([]byte, 10000), "multipart/form-data; boundary=seed")
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}

	entries := readLog(t, logfile)
	if len(entries) != 2 {
		t.Fatalf("got %d log entries, want 2 (failed attempt + drop): %v", len(entries), entries)
	}
	attempt := entries[0]
	if _, ok := attempt["event"]; ok {
		t.Errorf("attempt entry has an event field: %v", attempt)
	}
	if attempt["status"] != float64(500) {
		t.Errorf("attempt status = %v, want 500", attempt["status"])
	}
	if attempt["message_id"] != "" {
		t.Errorf("attempt message_id = %v, want empty on failure", attempt["message_id"])
	}
	drop := entries[1]
	if drop["event"] != "defer_drop" {
		t.Errorf("drop event = %v, want defer_drop", drop["event"])
	}
	if drop["message_id"] != old.ID {
		t.Errorf("drop message_id = %v, want %s", drop["message_id"], old.ID)
	}
}

// An unusable defer_dir degrades the send to a plain failure: exit 5,
// because exit 4 would promise a retry that will never happen.
func TestDeferStoreFailureExits5(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	// A regular file where the defer_dir is expected fails on every
	// platform (permission bits are unreliable under root).
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, "from = \"ops@example.com\"\n\n[defer]\ndefer_dir = '"+blocker+"'\n")

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s")
	if code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stderr, "mailfire: defer:") || !strings.Contains(stderr, "NOT saved") {
		t.Errorf("stderr missing the deferral failure:\n%s", stderr)
	}
}

// A deferred message keeps the full composed request: attachments are
// part of the stored bytes (their file handles are long closed by the
// time a resend happens).
func TestDeferStoresAttachments(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "")

	dir := t.TempDir()
	att := writeAttachment(t, dir, "notes.txt", txtBytes)

	code, _, stderr := runMock(t, m, "body",
		"-c", cfg, "--to", "a@example.com", "--subject", "s", "--attach", att)
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 {
		t.Fatalf("got %d deferred messages, want 1", len(ids))
	}
	_, body := readDeferred(t, deferDir, ids[0])
	if !bytes.Contains(body, txtBytes) {
		t.Errorf("stored body does not contain the attachment content")
	}
	if !bytes.Contains(body, []byte(`filename="notes.txt"`)) {
		t.Errorf("stored body does not contain the attachment part")
	}
}
