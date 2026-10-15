package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// seedDeferred stores one deferred message with a small real multipart
// body via deferral.Store.Save and, when age > 0, ages it by rewriting
// meta.json's timestamp (Save always stamps "now"; the cooldown tests
// need older messages).
func seedDeferred(t *testing.T, deferDir, to, subject string, age time.Duration) deferral.Metadata {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range []struct{ name, value string }{
		{"from", "ops@example.com"},
		{"to", to},
		{"subject", subject},
		{"text", "body of " + subject},
	} {
		if err := w.WriteField(f.name, f.value); err != nil {
			t.Fatalf("encoding seed body: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("encoding seed body: %v", err)
	}
	store := &deferral.Store{Dir: deferDir}
	meta, err := store.Save([]string{to}, subject, buf.Bytes(), w.FormDataContentType())
	if err != nil {
		t.Fatalf("seeding defer_dir: %v", err)
	}
	if age > 0 {
		meta.Timestamp = time.Now().UTC().Add(-age)
		data, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			t.Fatalf("aging %s: %v", meta.ID, err)
		}
		if err := os.WriteFile(filepath.Join(deferDir, meta.ID, deferral.MetaFile),
			append(data, '\n'), 0o600); err != nil {
			t.Fatalf("aging %s: %v", meta.ID, err)
		}
	}
	return meta
}

// storedBodies snapshots every stored message's metadata and body, keyed
// by subject, for verbatim-replay assertions after the resend.
func storedBodies(t *testing.T, deferDir string) map[string]struct {
	meta deferral.Metadata
	body []byte
} {
	t.Helper()
	out := map[string]struct {
		meta deferral.Metadata
		body []byte
	}{}
	store := &deferral.Store{Dir: deferDir}
	msgs, err := store.List()
	if err != nil {
		t.Fatalf("listing defer_dir: %v", err)
	}
	for _, m := range msgs {
		meta, body := readDeferred(t, deferDir, m.ID)
		out[meta.Subject] = struct {
			meta deferral.Metadata
			body []byte
		}{meta, body}
	}
	return out
}

// TestResendSendsAllOldestFirst: all eligible messages are replayed
// oldest-first, byte-verbatim with the stored content type, and removed
// from the defer_dir. The summary line closes the output (FR-4).
func TestResendSendsAllOldestFirst(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")

	// Seeded newest-to-oldest so the arrival order at the mock proves
	// the oldest-first resend order rather than the creation order.
	newest := seedDeferred(t, deferDir, "new@example.com", "newest", 1*time.Hour)
	oldest := seedDeferred(t, deferDir, "old@example.com", "oldest", 3*time.Hour)
	middle := seedDeferred(t, deferDir, "mid@example.com", "middle", 2*time.Hour)
	stored := storedBodies(t, deferDir)

	code, stdout, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	reqs := m.Requests()
	if len(reqs) != 3 {
		t.Fatalf("mock received %d requests, want 3", len(reqs))
	}
	wantOrder := []deferral.Metadata{oldest, middle, newest}
	for i, want := range wantOrder {
		req := reqs[i]
		if got := req.Field("subject"); got != want.Subject {
			t.Errorf("request %d: subject = %q, want %q (oldest first)", i, got, want.Subject)
		}
		if !bytes.Equal(req.Body, stored[want.Subject].body) {
			t.Errorf("request %d (%s): body differs from the stored body.bin", i, want.Subject)
		}
		if got := req.Header.Get("Content-Type"); got != stored[want.Subject].meta.ContentType {
			t.Errorf("request %d (%s): Content-Type = %q, want the stored %q",
				i, want.Subject, got, stored[want.Subject].meta.ContentType)
		}
	}

	for _, want := range wantOrder {
		if !strings.Contains(stdout, want.ID+": sent, message id ") {
			t.Errorf("stdout missing sent line for %s:\n%s", want.ID, stdout)
		}
	}
	if !strings.Contains(stdout, "resend: 3 sent, 0 skipped, 0 failed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}

	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Errorf("defer_dir still holds %v, want empty after successful resend", ids)
	}
}

// Decision Q5 invariant: a resent message is never in test mode. Task 14
// guarantees stored bodies never carry o:testmode; resend replays the
// bytes verbatim, so the mock must never see the field. Asserted on the
// wire and on the raw bytes.
func TestResendNeverTestMode(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")

	meta := seedDeferred(t, deferDir, "a@example.com", "no test mode", time.Hour)
	_, body := readDeferred(t, deferDir, meta.ID)
	if bytes.Contains(body, []byte("o:testmode")) {
		t.Fatal("seeded body unexpectedly contains o:testmode (task 14 invariant broken)")
	}

	code, _, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	req := m.LastRequest(t)
	if req.ParseErr != nil {
		t.Fatalf("mock could not parse the replayed request: %v", req.ParseErr)
	}
	if got := req.Field("o:testmode"); got != "" {
		t.Errorf("resent request carries o:testmode = %q, want absent", got)
	}
	if bytes.Contains(req.Body, []byte("o:testmode")) {
		t.Error("resent raw body contains o:testmode")
	}
}

// Cooldown (Decision Q3): a message younger than [defer].resend_cooldown
// is skipped with its age and eligibility; an older one is sent.
func TestResendCooldownSkipsFresh(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "") // default cooldown 5m

	fresh := seedDeferred(t, deferDir, "fresh@example.com", "fresh", 1*time.Minute)
	old := seedDeferred(t, deferDir, "old@example.com", "old", 10*time.Minute)

	code, stdout, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (skipped is not a failure); stderr:\n%s", code, ExitOK, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	reqs := m.Requests()
	if len(reqs) != 1 {
		t.Fatalf("mock received %d requests, want 1 (the aged message only)", len(reqs))
	}
	if got := reqs[0].Field("subject"); got != "old" {
		t.Errorf("sent subject = %q, want %q", got, "old")
	}
	if !strings.Contains(stdout, old.ID+": sent") {
		t.Errorf("stdout missing the sent line for %s:\n%s", old.ID, stdout)
	}

	if !strings.Contains(stdout, fresh.ID+": skipped") {
		t.Errorf("stdout missing the skip report for %s:\n%s", fresh.ID, stdout)
	}
	for _, want := range []string{"eligible", "in 4m", "--force"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("skip report missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "resend: 1 sent, 1 skipped, 0 failed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}

	// The skipped message stays; the sent one is gone.
	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 || ids[0] != fresh.ID {
		t.Errorf("defer_dir = %v, want only the skipped message %s", ids, fresh.ID)
	}
}

// A configured resend_cooldown replaces the default.
func TestResendCustomCooldown(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "resend_cooldown = \"2h\"\n")

	seedDeferred(t, deferDir, "a@example.com", "one hour old", 1*time.Hour)

	code, stdout, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if !strings.Contains(stdout, ": skipped") {
		t.Errorf("stdout missing the skip report:\n%s", stdout)
	}
	if !strings.Contains(stdout, "resend: 0 sent, 1 skipped, 0 failed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 {
		t.Errorf("defer_dir = %v, want the skipped message kept", ids)
	}
}

// --force ignores the cooldown and sends fresh messages too.
func TestResendForceIgnoresCooldown(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")

	fresh := seedDeferred(t, deferDir, "fresh@example.com", "fresh", 1*time.Minute)
	old := seedDeferred(t, deferDir, "old@example.com", "old", 10*time.Minute)

	code, stdout, stderr := runMock(t, m, "", "resend", "--force", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	reqs := m.Requests()
	if len(reqs) != 2 {
		t.Fatalf("mock received %d requests, want 2", len(reqs))
	}
	// Oldest first, even with --force.
	if got := reqs[0].Field("subject"); got != "old" {
		t.Errorf("first request subject = %q, want %q", got, "old")
	}
	for _, id := range []string{fresh.ID, old.ID} {
		if !strings.Contains(stdout, id+": sent") {
			t.Errorf("stdout missing sent line for %s:\n%s", id, stdout)
		}
	}
	if strings.Contains(stdout, ": skipped (") {
		t.Errorf("--force must not skip:\n%s", stdout)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 0 {
		t.Errorf("defer_dir = %v, want empty", ids)
	}
}

// `resend <id>...` sends exactly the named messages, in the order given.
func TestResendSpecificIDs(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")

	one := seedDeferred(t, deferDir, "one@example.com", "one", 3*time.Hour)
	two := seedDeferred(t, deferDir, "two@example.com", "two", 2*time.Hour)
	three := seedDeferred(t, deferDir, "three@example.com", "three", 1*time.Hour)

	// Reverse of the oldest-first order, plus a duplicate: the command
	// line order wins and the duplicate sends only once.
	code, stdout, stderr := runMock(t, m, "", "resend", "-c", cfg, two.ID, one.ID, two.ID)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	reqs := m.Requests()
	if len(reqs) != 2 {
		t.Fatalf("mock received %d requests, want 2", len(reqs))
	}
	if got := reqs[0].Field("subject"); got != "two" {
		t.Errorf("first request subject = %q, want %q", got, "two")
	}
	if got := reqs[1].Field("subject"); got != "one" {
		t.Errorf("second request subject = %q, want %q", got, "one")
	}
	if !strings.Contains(stdout, "resend: 2 sent, 0 skipped, 0 failed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}
	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 || ids[0] != three.ID {
		t.Errorf("defer_dir = %v, want only the unselected message %s", ids, three.ID)
	}
}

// An unknown ID is a usage error: exit 1, nothing is sent, nothing is
// touched — even when other IDs are valid.
func TestResendUnknownID(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")

	meta := seedDeferred(t, deferDir, "a@example.com", "kept", time.Hour)

	for _, ids := range [][]string{{"no-such-id"}, {meta.ID, "no-such-id"}} {
		code, stdout, stderr := runMock(t, m, "", append([]string{"resend", "-c", cfg}, ids...)...)
		if code != ExitUsage {
			t.Errorf("resend %v: exit = %d, want %d", ids, code, ExitUsage)
		}
		if !strings.Contains(stderr, `unknown deferred message ID "no-such-id"`) {
			t.Errorf("resend %v: stderr missing the unknown-ID message:\n%s", ids, stderr)
		}
		if stdout != "" {
			t.Errorf("resend %v: stdout = %q, want empty", ids, stdout)
		}
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 || ids[0] != meta.ID {
		t.Errorf("defer_dir = %v, want the message untouched", ids)
	}
}

// --list prints ID, date, to, subject, and size, oldest first, and never
// sends.
func TestResendList(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")

	newest := seedDeferred(t, deferDir, "new@example.com", "newer subject", 1*time.Hour)
	oldest := seedDeferred(t, deferDir, "old@example.com", "older subject", 3*time.Hour)

	code, stdout, stderr := runMock(t, m, "", "resend", "--list", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitOK, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0 (--list never sends)", n)
	}

	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("--list printed %d lines, want header + 2:\n%s", len(lines), stdout)
	}
	for _, col := range []string{"ID", "DATE", "TO", "SUBJECT", "SIZE"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("header line missing %q: %q", col, lines[0])
		}
	}
	// Oldest first.
	if !strings.Contains(lines[1], oldest.ID) || !strings.Contains(lines[2], newest.ID) {
		t.Errorf("list order wrong (want oldest first):\n%s", stdout)
	}
	for _, want := range []string{
		oldest.ID, "old@example.com", "older subject",
		newest.ID, "new@example.com", "newer subject",
		"UTC", fmt.Sprintf("%d", oldest.Size),
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--list output missing %q:\n%s", want, stdout)
		}
	}
	// Listing changes nothing.
	if ids := deferredIDs(t, deferDir); len(ids) != 2 {
		t.Errorf("defer_dir = %v, want both messages kept", ids)
	}
}

// --list combined with message IDs is a usage error.
func TestResendListWithIDs(t *testing.T) {
	cfg, _ := deferConfig(t, "")
	code, _, stderr := run(t, append([]string{"resend", "--list", "-c", cfg}, "some-id")...)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "--list") {
		t.Errorf("stderr missing the --list conflict message:\n%s", stderr)
	}
}

// An empty (or not yet created) defer_dir is not an error.
func TestResendEmptyDeferDir(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, _ := deferConfig(t, "") // defer_dir does not exist yet

	for _, args := range [][]string{
		{"resend", "-c", cfg},
		{"resend", "--list", "-c", cfg},
		{"resend", "--force", "-c", cfg},
	} {
		code, stdout, stderr := runMock(t, m, "", args...)
		if code != ExitOK {
			t.Errorf("Run(%v): exit = %d, want %d; stderr:\n%s", args, code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "no deferred messages") {
			t.Errorf("Run(%v): stdout = %q, want the empty-store note", args, stdout)
		}
		if stderr != "" {
			t.Errorf("Run(%v): stderr = %q, want empty", args, stderr)
		}
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
}

// A deferrable failure during resend keeps the message deferred — with
// its original timestamp (see resend.go) — and maps to exit 4.
func TestResendDeferrableFailureStaysDeferred(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "")

	meta := seedDeferred(t, deferDir, "a@example.com", "stays", time.Hour)

	code, stdout, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitDeferred {
		t.Errorf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	if !strings.Contains(stdout, meta.ID+": failed (retryable") {
		t.Errorf("stdout missing the failure line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "resend: 0 sent, 0 skipped, 1 failed") {
		t.Errorf("stdout missing the summary line:\n%s", stdout)
	}
	if !strings.Contains(stderr, "mailfire: resend: "+meta.ID) || !strings.Contains(stderr, "500") {
		t.Errorf("stderr missing the error detail:\n%s", stderr)
	}

	ids := deferredIDs(t, deferDir)
	if len(ids) != 1 || ids[0] != meta.ID {
		t.Fatalf("defer_dir = %v, want the failed message kept", ids)
	}
	kept, _ := readDeferred(t, deferDir, meta.ID)
	if !kept.Timestamp.Equal(meta.Timestamp) {
		t.Errorf("timestamp = %v, want the original %v (a failed resend keeps it)",
			kept.Timestamp, meta.Timestamp)
	}
}

// A permanent failure maps to exit 5; the message is kept for manual
// inspection — removal happens only after successful delivery.
func TestResendPermanentFailure(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.BadRequest()
	cfg, deferDir := deferConfig(t, "")

	meta := seedDeferred(t, deferDir, "a@example.com", "permanent", time.Hour)

	code, stdout, _ := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stdout, meta.ID+": failed (permanent") {
		t.Errorf("stdout missing the failure line:\n%s", stdout)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 || ids[0] != meta.ID {
		t.Errorf("defer_dir = %v, want the failed message kept", ids)
	}
}

// An auth failure maps to exit 3, and the error output never leaks the
// API key.
func TestResendAuthFailure(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Unauthorized()
	cfg, deferDir := deferConfig(t, "")

	meta := seedDeferred(t, deferDir, "a@example.com", "auth", time.Hour)

	code, stdout, stderr := runMock(t, m, "", "resend", "-c", cfg)
	if code != ExitAuth {
		t.Errorf("exit = %d, want %d", code, ExitAuth)
	}
	if !strings.Contains(stdout, meta.ID+": failed (authentication error") {
		t.Errorf("stdout missing the failure line:\n%s", stdout)
	}
	if strings.Contains(stderr, "test-key-not-real") {
		t.Errorf("stderr leaks the API key:\n%s", stderr)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 || ids[0] != meta.ID {
		t.Errorf("defer_dir = %v, want the failed message kept", ids)
	}
}

// Mixed outcomes: the run continues after a failure, and the most severe
// exit code wins — 3 (auth) > 5 (permanent) > 4 (deferrable) > 0.
func TestResendMixedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []testutil.Response
		want    int
		summary string
	}{
		{"permanent beats deferrable",
			[]testutil.Response{testutil.Queued("<m1@mg.example.com>"), testutil.BadRequest(), testutil.InternalError()},
			ExitSendFail, "resend: 1 sent, 0 skipped, 2 failed"},
		{"auth beats deferrable",
			[]testutil.Response{testutil.Unauthorized(), testutil.InternalError()},
			ExitAuth, "resend: 0 sent, 0 skipped, 2 failed"},
		{"deferrable beats success",
			[]testutil.Response{testutil.InternalError(), testutil.Queued("<m2@mg.example.com>")},
			ExitDeferred, "resend: 1 sent, 0 skipped, 1 failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			var call int
			m.SendHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := tc.replies[call%len(tc.replies)]
				call++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(resp.Status)
				fmt.Fprint(w, resp.Body)
			})
			cfg, deferDir := deferConfig(t, "")
			for i := range len(tc.replies) {
				seedDeferred(t, deferDir, fmt.Sprintf("r%d@example.com", i),
					fmt.Sprintf("msg %d", i), time.Duration(3-i)*time.Hour)
			}

			code, stdout, _ := runMock(t, m, "", "resend", "--force", "-c", cfg)
			if code != tc.want {
				t.Errorf("exit = %d, want %d; stdout:\n%s", code, tc.want, stdout)
			}
			if !strings.Contains(stdout, tc.summary) {
				t.Errorf("stdout missing summary %q:\n%s", tc.summary, stdout)
			}
			// Every message was attempted even after a failure.
			if n := len(m.Requests()); n != len(tc.replies) {
				t.Errorf("mock received %d requests, want %d (run continues after failures)",
					n, len(tc.replies))
			}
		})
	}
}

// An unreadable defer_dir fails the run with exit 5 (nothing was sent),
// for resend and --list alike.
func TestResendUnreadableDeferDir(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, "from = \"ops@example.com\"\n\n[defer]\ndefer_dir = '"+blocker+"'\n")

	for _, args := range [][]string{
		{"resend", "-c", cfg},
		{"resend", "--list", "-c", cfg},
	} {
		code, _, stderr := runMock(t, m, "", args...)
		if code != ExitSendFail {
			t.Errorf("Run(%v): exit = %d, want %d", args, code, ExitSendFail)
		}
		if !strings.Contains(stderr, "mailfire: resend:") {
			t.Errorf("Run(%v): stderr missing the error:\n%s", args, stderr)
		}
	}
}

// Resend loads its configuration like send does, -c/--config included.
func TestResendConfigHandling(t *testing.T) {
	t.Run("missing config file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.conf")
		code, _, stderr := run(t, "resend", "-c", missing)
		if code != ExitConfig {
			t.Errorf("exit = %d, want %d", code, ExitConfig)
		}
		if !strings.Contains(stderr, "no such file") {
			t.Errorf("stderr missing message:\n%s", stderr)
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mailfire.conf")
		if err := os.WriteFile(path, []byte("[mailgun]\nregion = \"us\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := run(t, "resend", "-c", path)
		if code != ExitConfig {
			t.Errorf("exit = %d, want %d", code, ExitConfig)
		}
		if !strings.Contains(stderr, "mailgun.api_key") {
			t.Errorf("stderr missing key name:\n%s", stderr)
		}
	})
	t.Run("conflicting config flags", func(t *testing.T) {
		code, _, stderr := run(t, "resend", "-c", "a.conf", "--config", "b.conf")
		if code != ExitUsage {
			t.Errorf("exit = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(stderr, "conflicting config paths") {
			t.Errorf("stderr missing message:\n%s", stderr)
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		code, _, stderr := run(t, "resend", "--bogus")
		if code != ExitUsage {
			t.Errorf("exit = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(stderr, "flag provided but not defined") {
			t.Errorf("stderr missing message:\n%s", stderr)
		}
	})
}

// config.Load guarantees valid credentials, so the client construction
// failure in resendSend is defensive; exercise it directly.
func TestResendClientConstructionError(t *testing.T) {
	deferDir := t.TempDir()
	seedDeferred(t, deferDir, "a@example.com", "s", time.Hour)
	cfg := &config.Config{APIKey: "k", Domain: "d", Region: "atlantis", DeferDir: deferDir}
	store := &deferral.Store{Dir: deferDir}
	opts := &resendOptions{}
	var stdout, stderr bytes.Buffer
	if code := resendSend(cfg, store, opts, &runContext{}, &stdout, &stderr); code != ExitConfig {
		t.Errorf("exit = %d, want %d", code, ExitConfig)
	}
	if !strings.Contains(stderr.String(), "unknown region") {
		t.Errorf("stderr missing region error:\n%s", stderr.String())
	}
}

// With defer = false and no defer_dir available (no default because the
// home directory is unknown), there is no place to look for deferred
// messages: an empty result, not an error.
func TestListDeferredNoDir(t *testing.T) {
	var stderr bytes.Buffer
	msgs, code := listDeferred(&deferral.Store{}, &stderr)
	if code != ExitOK {
		t.Errorf("code = %d, want %d", code, ExitOK)
	}
	if msgs != nil {
		t.Errorf("msgs = %v, want nil", msgs)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// A stored body that vanishes between listing and reading is a local
// failure: exit 5, the message directory is left alone.
func TestResendOneUnreadableBody(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	client, err := mailgun.New("k", "d", "us", mailgun.WithBaseURL(m.URL()))
	if err != nil {
		t.Fatal(err)
	}
	store := &deferral.Store{Dir: t.TempDir()}
	meta := deferral.Metadata{ID: "no-such-dir", To: []string{"a@example.com"}, Subject: "s"}
	var stdout, stderr bytes.Buffer
	if code := resendOne(client, store, meta, &stdout, &stderr); code != ExitSendFail {
		t.Errorf("exit = %d, want %d", code, ExitSendFail)
	}
	if !strings.Contains(stdout.String(), "failed (stored request unreadable") {
		t.Errorf("stdout missing the failure line:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "reading "+deferral.BodyFile) {
		t.Errorf("stderr missing the read error:\n%s", stderr.String())
	}
	if n := len(m.Requests()); n != 0 {
		t.Errorf("mock received %d requests, want 0", n)
	}
}
