package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// decodeJSON asserts that stdout is exactly one JSON document on one
// line, whose common fields agree with the process exit code, and
// returns it.
func decodeJSON(t *testing.T, code int, stdout, command string) map[string]any {
	t.Helper()
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout is not exactly one line:\n%s", stdout)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout holds more than one JSON document:\n%s", stdout)
	}
	if doc["schema_version"] != json.Number("1") {
		t.Errorf("schema_version = %v, want 1", doc["schema_version"])
	}
	if doc["command"] != command {
		t.Errorf("command = %v, want %q", doc["command"], command)
	}
	if got, ok := doc["exit_code"].(json.Number); !ok || got.String() != strconv.Itoa(code) {
		t.Errorf("exit_code = %v, process exit = %d", doc["exit_code"], code)
	}
	if _, hasErr := doc["error"]; hasErr != (code != ExitOK) {
		t.Errorf("error present = %v with exit %d:\n%s", hasErr, code, stdout)
	}
	return doc
}

// errorCode returns doc.error.code, or "" without an error.
func errorCode(doc map[string]any) string {
	e, ok := doc["error"].(map[string]any)
	if !ok {
		return ""
	}
	return opt(e, "code")
}

// get returns m[key] as T and fails the test when it is absent or of
// another type.
func get[T any](t *testing.T, m map[string]any, key string) T {
	t.Helper()
	v, ok := m[key].(T)
	if !ok {
		t.Fatalf("%s = %#v, want a %T", key, m[key], v)
	}
	return v
}

// opt returns m[key] as a string, "" when absent.
func opt(m map[string]any, key string) string {
	s, ok := m[key].(string)
	if !ok {
		return ""
	}
	return s
}

// list returns doc[key] as a slice of objects.
func list(t *testing.T, doc map[string]any, key string) []map[string]any {
	t.Helper()
	raw := get[[]any](t, doc, key)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		var ok bool
		if out[i], ok = r.(map[string]any); !ok {
			t.Fatalf("%s[%d] is not an object: %v", key, i, r)
		}
	}
	return out
}

// assertRFC3339UTC checks that v is an RFC 3339 timestamp in UTC.
func assertRFC3339UTC(t *testing.T, name string, v any) {
	t.Helper()
	s, ok := v.(string)
	ts, err := time.Parse(time.RFC3339, s)
	if !ok || err != nil || !strings.HasSuffix(s, "Z") || ts.Location() != time.UTC {
		t.Errorf("%s = %q, want an RFC 3339 UTC timestamp", name, s)
	}
}

func TestJSONSendOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		resp    testutil.Response
		extra   []string
		code    int
		outcome string
		errCode string
	}{
		{"sent", testutil.Queued("<id1@mg.example.com>"), nil, ExitOK, "sent", ""},
		{"test mode", testutil.Queued("<id1@mg.example.com>"), []string{"--test"}, ExitOK, "accepted", ""},
		{"deferred", testutil.InternalError(), nil, ExitDeferred, "deferred", errTemporary},
		{"not deferred", testutil.InternalError(), []string{"--no-defer"}, ExitSendFail, "failed", errTemporary},
		{"test mode never deferred", testutil.RateLimited(), []string{"--test"}, ExitSendFail, "failed", errTemporary},
		{"rejected", testutil.BadRequest(), nil, ExitSendFail, "failed", errRejected},
		{"auth", testutil.Unauthorized(), nil, ExitAuth, "failed", errAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = tc.resp
			cfg, deferDir := deferConfig(t, "")
			args := append([]string{"--json", "-c", cfg, "--to", "a@example.com", "--subject", "hi", "-m", "body"}, tc.extra...)
			code, stdout, _ := runMock(t, m, "", args...)
			if code != tc.code {
				t.Fatalf("exit = %d, want %d", code, tc.code)
			}
			doc := decodeJSON(t, code, stdout, "send")
			if doc["outcome"] != tc.outcome {
				t.Errorf("outcome = %v, want %q", doc["outcome"], tc.outcome)
			}
			if got := errorCode(doc); got != tc.errCode {
				t.Errorf("error.code = %q, want %q", got, tc.errCode)
			}
			wantTest := len(tc.extra) > 0 && tc.extra[0] == "--test"
			if doc["test_mode"] != wantTest {
				t.Errorf("test_mode = %v, want %v", doc["test_mode"], wantTest)
			}
			_, hasID := doc["message_id"]
			if hasID != (code == ExitOK) {
				t.Errorf("message_id present = %v:\n%s", hasID, stdout)
			}
			if code == ExitOK && doc["message_id"] != "<id1@mg.example.com>" {
				t.Errorf("message_id = %v", doc["message_id"])
			}
			if tc.outcome == "deferred" {
				ids := deferredIDs(t, deferDir)
				if len(ids) != 1 || doc["deferred_id"] != ids[0] {
					t.Errorf("deferred_id = %v, stored %v", doc["deferred_id"], ids)
				}
			} else if _, ok := doc["deferred_id"]; ok {
				t.Errorf("unexpected deferred_id:\n%s", stdout)
			}
		})
	}
}

// The API key never appears in JSON output, even when Mailgun echoes it.
func TestJSONRedactsCredentials(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.ErrorJSON(http.StatusBadRequest, "bad key test-key-not-real")
	m.DomainResponse = testutil.ErrorJSON(http.StatusUnauthorized, "bad key test-key-not-real")
	cfg, _ := deferConfig(t, "")
	for _, args := range [][]string{
		{"--json", "-c", cfg, "--to", "a@example.com", "--subject", "hi", "-m", "body"},
		{"validate", "--json", "-c", cfg},
	} {
		code, stdout, stderr := runMock(t, m, "", args...)
		if code == ExitOK {
			t.Fatalf("%v: exit 0", args)
		}
		if strings.Contains(stdout+stderr, "test-key-not-real") {
			t.Errorf("%v: API key leaked:\n%s\n%s", args, stdout, stderr)
		}
		if !strings.Contains(stdout, "[redacted]") {
			t.Errorf("%v: error message not redacted:\n%s", args, stdout)
		}
	}
}

func TestJSONSendDedupSkip(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, _, _ := dedupConfig(t, "")
	c := c0()
	args := []string{"--json", "--dedup-key", "disk", "--to", "a@example.com", "--subject", "s", "-m", "b"}
	if code, _, stderr := notify(t, m, c, cfg, args...); code != ExitOK {
		t.Fatalf("first send: exit %d: %s", code, stderr)
	}
	code, stdout, _ := notify(t, m, c, cfg, args...)
	doc := decodeJSON(t, code, stdout, "send")
	if doc["outcome"] != "skipped" {
		t.Fatalf("outcome = %v, want skipped", doc["outcome"])
	}
	skip := get[map[string]any](t, doc, "skip")
	if skip["reason"] != "cooldown" || skip["dedup_key"] != "disk" {
		t.Errorf("skip = %v", skip)
	}
	assertRFC3339UTC(t, "skip.since", skip["since"])
	assertRFC3339UTC(t, "skip.eligible_at", skip["eligible_at"])
	if _, ok := skip["queued_id"]; ok {
		t.Errorf("queued_id present for a cooldown skip: %v", skip)
	}
}

// Usage and configuration errors are reported as JSON whenever --json
// can be recognized, also when flag parsing fails.
func TestJSONInvocationErrors(t *testing.T) {
	cases := []struct {
		args    []string
		command string
		code    int
		errCode string
	}{
		{[]string{"--json", "--subject", "s"}, "send", ExitUsage, errUsage},
		{[]string{"--json", "--bogus"}, "send", ExitUsage, errUsage},
		{[]string{"send", "--to", "a@example.com", "--json=true"}, "send", ExitUsage, errUsage},
		{[]string{"--json", "--preview", "-", "-m", "x"}, "send", ExitUsage, errUsage},
		{[]string{"--json", "--preview", "out.html", "-m", "x"}, "send", ExitUsage, errUsage},
		{[]string{"--json", "-c", "missing.conf", "--to", "a@example.com", "--subject", "s"}, "send", ExitConfig, errConfig},
		{[]string{"frobnicate", "--json"}, "mailfire", ExitUsage, errUsage},
		{[]string{"resend", "--json", "--list", "some-id"}, "resend", ExitUsage, errUsage},
		{[]string{"resend", "--bogus", "--json"}, "resend", ExitUsage, errUsage},
		{[]string{"resend", "--json", "-c", "missing.conf"}, "resend", ExitConfig, errConfig},
		{[]string{"queue", "--json"}, "queue", ExitUsage, errUsage},
		{[]string{"queue", "frob", "--json"}, "queue", ExitUsage, errUsage},
		{[]string{"queue", "show", "--json"}, "queue show", ExitUsage, errUsage},
		{[]string{"queue", "list", "--json", "-c", "missing.conf"}, "queue list", ExitConfig, errConfig},
		{[]string{"validate", "--json", "extra"}, "validate", ExitUsage, errUsage},
		{[]string{"version", "--json", "extra"}, "version", ExitUsage, errUsage},
		{[]string{"export-templates", "--json"}, "export-templates", ExitUsage, errUsage},
		{[]string{"export-templates", "--json", "--bogus"}, "export-templates", ExitUsage, errUsage},
	}
	t.Chdir(t.TempDir())
	for _, tc := range cases {
		code, stdout, stderr := run(t, tc.args...)
		if code != tc.code {
			t.Errorf("Run(%v) = exit %d, want %d; stderr:\n%s", tc.args, code, tc.code, stderr)
			continue
		}
		doc := decodeJSON(t, code, stdout, tc.command)
		if doc["outcome"] != "failed" || errorCode(doc) != tc.errCode {
			t.Errorf("Run(%v): outcome %v, error %v", tc.args, doc["outcome"], doc["error"])
		}
		if len(doc) != 5 {
			t.Errorf("Run(%v): want only the common fields:\n%s", tc.args, stdout)
		}
		if stderr == "" {
			t.Errorf("Run(%v): stderr is empty, want the human-readable error", tc.args)
		}
	}
	if _, err := os.Stat("out.html"); err == nil {
		t.Error("--json --preview wrote a preview")
	}
}

// The output mode of a usage error is read like the flags are parsed:
// a --json that is a flag value or follows "--" is no --json flag, and a
// "--" that is a flag value is no terminator. Validation failures after
// parsing keep the parsed mode.
func TestJSONModeFollowsFlagParsing(t *testing.T) {
	for _, args := range [][]string{
		{"send", "--json", "--message", "--json=false"},
		{"send", "--message", "--", "--json"},
		{"send", "--json", "--to", "a@example.com"},
		{"send", "--bogus", "--json"},
		{"send", "--to", "--json", "--json", "--subject", "s", "--html", "--md"},
		{"resend", "--config", "--", "some-id", "--json", "--list"},
		{"resend", "--timeout", "--json", "--json"},
		{"queue", "show", "--config", "--", "a", "b", "--json"},
		{"queue", "--config", "x", "--json"},
		{"validate", "--json", "--timeout", "nope"},
		{"version", "--json", "--bogus"},
		{"export-templates", "--json", "a", "b"},
	} {
		code, stdout, stderr := run(t, args...)
		if code != ExitUsage {
			t.Errorf("Run(%v) = exit %d, want %d; stderr:\n%s", args, code, ExitUsage, stderr)
			continue
		}
		if doc := decodeJSON(t, code, stdout, commandOf(args)); errorCode(doc) != errUsage {
			t.Errorf("Run(%v): %s", args, stdout)
		}
	}
}

// commandOf is the JSON command name of a command line.
func commandOf(args []string) string {
	switch args[0] {
	case "queue":
		if args[1] == "show" {
			return "queue show"
		}
		return "queue"
	}
	return args[0]
}

// Without --json (or with --json=false, or --json as a flag value or
// after "--"), output stays human-readable.
func TestJSONNotRequested(t *testing.T) {
	for _, args := range [][]string{
		{"--subject", "s"},
		{"--json=false", "--subject", "s"},
		{"--subject", "s", "--", "--json"},
		{"--message", "--json", "--subject", "s"},
		{"--message", "--json", "--bogus"},
		{"--to", "a@example.com", "positional", "--json"},
		{"resend", "--config", "--json", "--bogus"},
		{"resend", "--bogus", "id", "--", "--json"},
		{"queue", "delete", "--config", "--json"},
		{"validate", "--timeout", "--json", "--bogus"},
		{"frobnicate"},
	} {
		code, stdout, _ := run(t, args...)
		if code != ExitUsage || stdout != "" {
			t.Errorf("Run(%v) = exit %d, stdout %q; want 1 and empty stdout", args, code, stdout)
		}
	}
	if !jsonRequested([]string{"-json"}) || jsonRequested([]string{"--json=no"}) || !jsonRequested([]string{"--json=false", "--json"}) {
		t.Error("jsonRequested misreads the flag forms")
	}
}

// splitArgs reads flags exactly like the flag package: values are
// consumed by value flags only, and only an unconsumed "--" terminates.
func TestSplitArgs(t *testing.T) {
	var opts queueOptions
	var configShort string
	fs := queueFlagSet("show", &opts, &configShort)
	for _, tc := range []struct {
		args              []string
		interspersed      bool
		flags, positional []string
	}{
		{[]string{"--config", "--", "id", "--json"}, true, []string{"--config", "--", "--json"}, []string{"id"}},
		{[]string{"id", "--", "--json", "-c", "x"}, true, nil, []string{"id", "--json", "-c", "x"}},
		{[]string{"-c", "x", "id", "--raw", "id2"}, true, []string{"-c", "x", "--raw"}, []string{"id", "id2"}},
		{[]string{"-c", "x", "id", "--raw"}, false, []string{"-c", "x"}, []string{"id", "--raw"}},
		{[]string{"--config=--", "--json=false", "-", "--bogus", "v"}, true, []string{"--config=--", "--json=false", "--bogus"}, []string{"-", "v"}},
		{[]string{"--raw", "--config"}, true, []string{"--raw", "--config"}, nil},
	} {
		flags, positional := splitArgs(fs, tc.args, tc.interspersed)
		if !equalStrings(flags, tc.flags) || !equalStrings(positional, tc.positional) {
			t.Errorf("splitArgs(%q) = %q, %q; want %q, %q", tc.args, flags, positional, tc.flags, tc.positional)
		}
	}
}

// IDs and flags may be mixed in any order; a "--" consumed as a flag
// value does not end the flags.
func TestInterspersedIDs(t *testing.T) {
	r, err := parseResendFlags([]string{"--config", "--", "some-id", "--json", "other", "--force"})
	if err != nil || r.Config != "--" || !r.JSON || !r.Force || !equalStrings(r.IDs, []string{"some-id", "other"}) {
		t.Errorf("parseResendFlags = %+v, %v", r, err)
	}
	r, err = parseResendFlags([]string{"--json", "a", "--", "--force", "-c"})
	if err != nil || !r.JSON || r.Force || r.Config != "" || !equalStrings(r.IDs, []string{"a", "--force", "-c"}) {
		t.Errorf("parseResendFlags after --: %+v, %v", r, err)
	}
	q, err := parseQueueFlags("delete", []string{"a", "-c", "x.conf", "b", "--json", "--", "-c"})
	if err != nil || q.Config != "x.conf" || !q.JSON || !equalStrings(q.IDs, []string{"a", "b", "-c"}) {
		t.Errorf("parseQueueFlags = %+v, %v", q, err)
	}
	q, err = parseQueueFlags("show", []string{"--config", "--", "--raw", "id"})
	if err != nil || q.Config != "--" || !q.Raw || !equalStrings(q.IDs, []string{"id"}) {
		t.Errorf("parseQueueFlags show = %+v, %v", q, err)
	}
	// A consumed "--" does not hide a later help request.
	code, stdout, _ := run(t, "resend", "--config", "--", "-h")
	if code != ExitOK || !strings.Contains(stdout, "Flags (resend):") {
		t.Errorf("resend --config -- -h = exit %d:\n%s", code, stdout)
	}
}

// A Mailgun message ID echoing the API key is redacted in JSON output of
// send and resend.
func TestJSONRedactsMessageID(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.Queued("<test-key-not-real@mg.example.com>")
	cfg, deferDir := deferConfig(t, "")
	seedAt(t, deferDir, "a@example.com", "s", qt0.Add(-time.Hour))
	for _, args := range [][]string{
		{"--json", "-c", cfg, "--to", "a@example.com", "--subject", "hi", "-m", "body"},
		{"-c", cfg, "--to", "a@example.com", "--subject", "hi", "-m", "body"},
		{"resend", "--json", "-c", cfg},
	} {
		code, stdout, stderr := runAt(t, m, &testClock{now: qt0}, args...)
		if code != ExitOK {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
		if strings.Contains(stdout+stderr, "test-key-not-real") || !strings.Contains(stdout, "<[redacted]@mg.example.com>") {
			t.Errorf("%v: message ID not redacted:\n%s", args, stdout)
		}
	}
}

// Help is always text, also with --json.
func TestJSONHelpIsText(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "--help"}, {"resend", "--json", "-h"}, {"queue", "list", "--json", "--help"},
		{"validate", "--json", "--help"}, {"version", "--json", "-h"}, {"export-templates", "--json", "--help"},
	} {
		code, stdout, _ := run(t, args...)
		if code != ExitOK || !strings.HasPrefix(stdout, "Usage:") {
			t.Errorf("Run(%v) = exit %d:\n%s", args, code, stdout)
		}
	}
}

// Warnings go to stderr and leave stdout a single valid document.
func TestJSONWarningsStayOnStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission warning on Windows")
	}
	m := testutil.NewMockMailgun(t)
	cfg, _ := deferConfig(t, "")
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runMock(t, m, "", "--json", "-c", cfg, "--test", "--dedup-key", "k",
		"--to", "a@example.com", "--subject", "s", "-m", "b")
	doc := decodeJSON(t, code, stdout, "send")
	if doc["outcome"] != "accepted" {
		t.Errorf("outcome = %v", doc["outcome"])
	}
	for _, w := range []string{"warning: config file", "deduplication not applied"} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr lacks %q:\n%s", w, stderr)
		}
	}
}

// Escaping and Unicode survive a round trip through queue list --json;
// absent optional fields are omitted.
func TestJSONQueueListEscaping(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")
	subject := "Grüße \"quoted\" <b>&</b> \\ tab\t🚀 \u2028"
	to := "\"Doe, Jöhn\" <john@example.com>"
	meta := seedAt(t, deferDir, to, subject, qt0.Add(-time.Hour).In(time.FixedZone("CEST", 2*3600)))

	code, stdout, _ := runAt(t, m, &testClock{now: qt0}, "queue", "list", "--json", "-c", cfg)
	doc := decodeJSON(t, code, stdout, "queue list")
	msgs := list(t, doc, "messages")
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	q := msgs[0]
	if q["id"] != meta.ID || q["subject"] != subject || q["status"] != "pending" || q["size"] != json.Number(strconv.FormatInt(meta.Size, 10)) {
		t.Errorf("record = %v", q)
	}
	if tos := get[[]any](t, q, "to"); len(tos) != 1 || tos[0] != to {
		t.Errorf("to = %v", q["to"])
	}
	if q["deferred_at"] != "2026-10-15T11:00:00Z" {
		t.Errorf("deferred_at = %v, want UTC", q["deferred_at"])
	}
	assertRFC3339UTC(t, "next_retry", q["next_retry"])
	for _, absent := range []string{"last_attempt", "last_error", "error"} {
		if _, ok := q[absent]; ok {
			t.Errorf("absent field %s present: %v", absent, q)
		}
	}
	if strings.Contains(stdout, "body of") {
		t.Errorf("message content leaked:\n%s", stdout)
	}

	// resend --list prints the same records.
	code, stdout2, _ := runAt(t, m, &testClock{now: qt0}, "resend", "--list", "--json", "-c", cfg)
	doc2 := decodeJSON(t, code, stdout2, "resend")
	if got, want := list(t, doc2, "messages"), msgs; len(got) != 1 || got[0]["subject"] != want[0]["subject"] {
		t.Errorf("resend --list records = %v", got)
	}
}

func TestJSONEmptyQueue(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, _ := deferConfig(t, "")
	for _, tc := range []struct {
		args    []string
		command string
	}{
		{[]string{"queue", "list"}, "queue list"},
		{[]string{"resend", "--list"}, "resend"},
		{[]string{"resend"}, "resend"},
	} {
		args := append(append([]string{}, tc.args...), "--json", "-c", cfg)
		code, stdout, _ := runMock(t, m, "", args...)
		doc := decodeJSON(t, code, stdout, tc.command)
		if code != ExitOK || doc["outcome"] != "ok" || len(list(t, doc, "messages")) != 0 {
			t.Errorf("%v: exit %d:\n%s", tc.args, code, stdout)
		}
	}
	code, stdout, _ := runMock(t, m, "", "queue", "stats", "--json", "-c", cfg)
	doc := decodeJSON(t, code, stdout, "queue stats")
	st := get[map[string]any](t, doc, "stats")
	if st["messages"] != json.Number("0") || st["total_size"] != json.Number("0") {
		t.Errorf("stats = %v", st)
	}
	for _, absent := range []string{"oldest_id", "oldest_deferred_at", "last_attempt", "next_retry"} {
		if _, ok := st[absent]; ok {
			t.Errorf("empty queue stats has %s", absent)
		}
	}
}

// A resend run with mixed outcomes: one sent, one temporary failure,
// one not yet eligible, one permanently failed, one expired.
func TestJSONResendPartialBatch(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	var status, n atomic.Int32
	status.Store(http.StatusOK)
	cfg, deferDir := deferConfig(t, "max_age = \"48h\"\n")
	expired := seedAt(t, deferDir, "e@example.com", "expired", qt0.Add(-72*time.Hour))
	sent := seedAt(t, deferDir, "a@example.com", "sent", qt0.Add(-3*time.Hour))
	retry := seedAt(t, deferDir, "b@example.com", "retry", qt0.Add(-2*time.Hour))
	perm := seedAt(t, deferDir, "c@example.com", "perm", qt0.Add(-90*time.Minute))
	perm.Failed = true
	writeMetaFile(t, deferDir, perm)
	young := seedAt(t, deferDir, "d@example.com", "young", qt0.Add(-time.Minute))

	m.SendHandler = func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 2 {
			status.Store(http.StatusServiceUnavailable)
		}
		statusHandler(t, &status)(w, r)
	}
	code, stdout, stderr := runAt(t, m, &testClock{now: qt0}, "resend", "--json", "-c", cfg)
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	doc := decodeJSON(t, code, stdout, "resend")
	if doc["outcome"] != "partial" || errorCode(doc) != errTemporary {
		t.Errorf("outcome %v, error %v", doc["outcome"], doc["error"])
	}
	want := map[string][2]string{
		expired.ID: {"expired", ""},
		sent.ID:    {"sent", ""},
		retry.ID:   {"failed", ""},
		perm.ID:    {"skipped", "permanently_failed"},
		young.ID:   {"skipped", "not_eligible"},
	}
	msgs := list(t, doc, "messages")
	if len(msgs) != len(want) {
		t.Fatalf("messages = %v", msgs)
	}
	for _, rec := range msgs {
		id := get[string](t, rec, "id")
		w := want[id]
		reason := opt(rec, "reason")
		if rec["outcome"] != w[0] || reason != w[1] {
			t.Errorf("%s: outcome %v reason %v, want %v", id, rec["outcome"], rec["reason"], w)
		}
		switch id {
		case sent.ID:
			if rec["message_id"] != "<ok@mg.example.com>" {
				t.Errorf("sent record = %v", rec)
			}
		case retry.ID:
			if e := get[map[string]any](t, rec, "error"); e["code"] != errTemporary {
				t.Errorf("failed record = %v", rec)
			}
			assertRFC3339UTC(t, "next_retry", rec["next_retry"])
		case young.ID:
			assertRFC3339UTC(t, "next_retry", rec["next_retry"])
		}
	}
	sum := get[map[string]any](t, doc, "summary")
	for k, v := range map[string]string{"sent": "1", "skipped": "2", "failed": "1", "expired": "1"} {
		if sum[k] != json.Number(v) {
			t.Errorf("summary.%s = %v, want %s", k, sum[k], v)
		}
	}
}

func TestJSONResendFailures(t *testing.T) {
	t.Run("all failed", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.SendResponse = testutil.Unauthorized()
		cfg, deferDir := deferConfig(t, "")
		seedAt(t, deferDir, "a@example.com", "s", qt0.Add(-time.Hour))
		code, stdout, _ := runAt(t, m, &testClock{now: qt0}, "resend", "--json", "-c", cfg)
		doc := decodeJSON(t, code, stdout, "resend")
		if code != ExitAuth || doc["outcome"] != "failed" || errorCode(doc) != errAuth {
			t.Errorf("exit %d:\n%s", code, stdout)
		}
	})
	t.Run("unknown id", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		cfg, _ := deferConfig(t, "")
		code, stdout, _ := runMock(t, m, "", "resend", "nope", "--json", "-c", cfg)
		doc := decodeJSON(t, code, stdout, "resend")
		if code != ExitUsage || errorCode(doc) != errUnknownID {
			t.Errorf("exit %d:\n%s", code, stdout)
		}
	})
}

func TestJSONQueueShowDeleteStats(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg, deferDir := deferConfig(t, "")
	meta := seedAt(t, deferDir, "a@example.com", "s", qt0.Add(-time.Hour))
	meta.Attempts, meta.LastAttempt, meta.LastError, meta.Failed = 2, qt0.Add(-time.Minute), "status 400: bad", true
	writeMetaFile(t, deferDir, meta)
	c := &testClock{now: qt0}

	code, stdout, _ := runAt(t, m, c, "queue", "show", meta.ID, "--json", "-c", cfg)
	doc := decodeJSON(t, code, stdout, "queue show")
	msg := get[map[string]any](t, doc, "message")
	if msg["status"] != "failed" || msg["attempts"] != json.Number("2") || msg["last_error"] != "status 400: bad" {
		t.Errorf("message = %v", msg)
	}
	if _, ok := msg["next_retry"]; ok {
		t.Errorf("failed message has next_retry: %v", msg)
	}
	if _, ok := doc["raw"]; ok || strings.Contains(stdout, "body of") {
		t.Errorf("content without --raw:\n%s", stdout)
	}

	code, stdout, _ = runAt(t, m, c, "queue", "show", "--raw", "--json", meta.ID, "-c", cfg)
	doc = decodeJSON(t, code, stdout, "queue show")
	raw := get[map[string]any](t, doc, "raw")
	_, body := readDeferred(t, deferDir, meta.ID)
	got, err := base64.StdEncoding.DecodeString(get[string](t, raw, "body_base64"))
	if err != nil || !bytes.Equal(got, body) || raw["content_type"] != meta.ContentType {
		t.Errorf("raw = %v (err %v)", raw, err)
	}

	code, stdout, _ = runAt(t, m, c, "queue", "show", "nope", "--json", "-c", cfg)
	if doc = decodeJSON(t, code, stdout, "queue show"); errorCode(doc) != errUnknownID {
		t.Errorf("unknown id: %s", stdout)
	}

	code, stdout, _ = runAt(t, m, c, "queue", "stats", "--json", "-c", cfg)
	doc = decodeJSON(t, code, stdout, "queue stats")
	st := get[map[string]any](t, doc, "stats")
	if st["messages"] != json.Number("1") || st["failed"] != json.Number("1") || st["oldest_id"] != meta.ID ||
		st["attempts"] != json.Number("2") || st["last_attempt"] != "2026-10-15T11:59:00Z" {
		t.Errorf("stats = %v", st)
	}

	code, stdout, _ = runAt(t, m, c, "queue", "delete", meta.ID, "nope", "--json", "-c", cfg)
	doc = decodeJSON(t, code, stdout, "queue delete")
	if code != ExitUsage || errorCode(doc) != errUnknownID || len(list(t, doc, "deleted")) != 0 {
		t.Errorf("unknown delete:\n%s", stdout)
	}
	if ids := get[[]any](t, doc, "unknown_ids"); len(ids) != 1 || ids[0] != "nope" {
		t.Errorf("unknown_ids = %v", doc["unknown_ids"])
	}

	code, stdout, _ = runAt(t, m, c, "queue", "delete", meta.ID, "--json", "-c", cfg)
	doc = decodeJSON(t, code, stdout, "queue delete")
	if ids := get[[]any](t, doc, "deleted"); code != ExitOK || len(ids) != 1 || ids[0] != meta.ID {
		t.Errorf("delete:\n%s", stdout)
	}
}

func TestJSONValidate(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		code, stdout, _ := runValidateMock(t, m, "--json", "-c", writeConfig(t, ""))
		doc := decodeJSON(t, code, stdout, "validate")
		checks := list(t, doc, "checks")
		if code != ExitOK || len(checks) != 3 || checks[2]["name"] != "api_key_and_domain" || checks[2]["ok"] != true {
			t.Errorf("exit %d:\n%s", code, stdout)
		}
		if d := get[map[string]any](t, doc, "domain"); d["name"] != "mg.example.com" || d["state"] != "active" {
			t.Errorf("domain = %v", doc["domain"])
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		m.DomainResponse = testutil.InternalError()
		code, stdout, _ := runValidateMock(t, m, "--json", "-c", writeConfig(t, ""))
		doc := decodeJSON(t, code, stdout, "validate")
		checks := list(t, doc, "checks")
		if code != ExitDeferred || errorCode(doc) != errTemporary || checks[len(checks)-1]["ok"] != false {
			t.Errorf("exit %d:\n%s", code, stdout)
		}
	})
	t.Run("config", func(t *testing.T) {
		m := testutil.NewMockMailgun(t)
		path := filepath.Join(t.TempDir(), "bad.conf")
		if err := os.WriteFile(path, []byte("[mailgun\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ := runValidateMock(t, m, "--json", "-c", path)
		doc := decodeJSON(t, code, stdout, "validate")
		checks := list(t, doc, "checks")
		if code != ExitConfig || errorCode(doc) != errConfig || len(checks) != 1 || checks[0]["name"] != "config_file" {
			t.Errorf("exit %d:\n%s", code, stdout)
		}
	})
}

func TestJSONVersionAndExport(t *testing.T) {
	code, stdout, _ := run(t, "version", "--json")
	doc := decodeJSON(t, code, stdout, "version")
	if doc["version"] != "1.2.3" || doc["commit"] != "abc1234" || doc["build_date"] != "2026-10-15" || doc["outcome"] != "ok" {
		t.Errorf("version:\n%s", stdout)
	}

	dir := filepath.Join(t.TempDir(), "tpl")
	code, stdout, _ = run(t, "export-templates", dir, "--json")
	doc = decodeJSON(t, code, stdout, "export-templates")
	if code != ExitOK || doc["directory"] != dir {
		t.Errorf("export:\n%s", stdout)
	}
	code, stdout, _ = run(t, "export-templates", "--json", dir)
	if doc = decodeJSON(t, code, stdout, "export-templates"); code != ExitUsage || errorCode(doc) != errExport {
		t.Errorf("second export:\n%s", stdout)
	}
}

// The error-code fallback covers every exit code.
func TestExitErrorCode(t *testing.T) {
	for exit, want := range map[int]string{ExitUsage: errUsage, ExitConfig: errConfig, ExitAuth: errAuth,
		ExitDeferred: errTemporary, ExitSendFail: errSendFailed} {
		if got := exitErrorCode(exit); got != want {
			t.Errorf("exitErrorCode(%d) = %q, want %q", exit, got, want)
		}
	}
	var buf bytes.Buffer
	emitJSON(&buf, &envelope{Command: "x"}, ExitSendFail)
	if !strings.Contains(buf.String(), `"code":"send_failed"`) {
		t.Errorf("fallback error missing: %s", buf.String())
	}
}
