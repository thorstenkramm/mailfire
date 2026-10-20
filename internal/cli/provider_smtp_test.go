package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/smtp"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

const (
	smtpTestUser = "submit@example.com"
	smtpTestPass = "Sm7p-Pa55w0rd-never-printed"
)

// smtpEnv is a STARTTLS submission server with authentication plus a
// config pointing at it, with defer_dir, dedup state, and a debug log in
// temp directories.
type smtpEnv struct {
	m        *testutil.MockSMTP
	opt      Option // trusts the server's test certificate
	dir      string
	deferDir string
	logfile  string
}

func newSMTPEnv(t *testing.T) *smtpEnv {
	t.Helper()
	serverTLS, clientTLS := testutil.TLSPair(t)
	m := testutil.NewMockSMTP(t, func(m *testutil.MockSMTP) {
		m.StartTLS, m.TLSConfig = true, serverTLS
		m.AuthMechs = []string{"PLAIN"}
		m.Users = map[string]string{smtpTestUser: smtpTestPass}
		m.RequireAuth = true
	})
	dir := t.TempDir()
	return &smtpEnv{m: m, opt: WithSMTPOptions(smtp.WithTLSConfig(clientTLS)), dir: dir,
		deferDir: filepath.Join(dir, "deferred"), logfile: filepath.Join(dir, "send.log")}
}

// smtpSection is the [smtp] section for the mock server.
func (e *smtpEnv) smtpSection() string {
	return fmt.Sprintf("[smtp]\nhost = %q\nport = %d\nsecurity = \"starttls\"\nusername = %q\npassword = %q\ntimeout = \"5s\"\n",
		e.m.Host(), e.m.Port(), smtpTestUser, smtpTestPass)
}

// config writes a config file selecting provider; extra is appended.
func (e *smtpEnv) config(t *testing.T, provider, extra string) string {
	t.Helper()
	content := fmt.Sprintf("[sending]\nprovider = %q\ndomain = \"example.com\"\nfrom = \"Ops <ops@example.com>\"\n\n%s\n"+
		"[logging]\nlogfile = '%s'\nloglevel = \"debug\"\n\n[defer]\ndefer_dir = '%s'\n\n[dedup]\nstate_dir = '%s'\n%s",
		provider, e.smtpSection(), e.logfile, e.deferDir, filepath.Join(e.dir, "dedup"), extra)
	path := filepath.Join(t.TempDir(), "mailfire.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e *smtpEnv) run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr, testInfo, WithStdin(strings.NewReader(stdin)), e.opt)
	return code, stdout.String(), stderr.String()
}

// assertNoPassword checks every place a credential could leak to.
func (e *smtpEnv) assertNoPassword(t *testing.T, outputs ...string) {
	t.Helper()
	for i, o := range outputs {
		if strings.Contains(o, smtpTestPass) {
			t.Errorf("output %d contains the SMTP password:\n%s", i, o)
		}
	}
	err := filepath.WalkDir(e.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if bytes.Contains(readBytes(t, p), []byte(smtpTestPass)) {
			t.Errorf("%s contains the SMTP password", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", e.dir, err)
	}
}

func TestSMTPSendEndToEnd(t *testing.T) {
	e := newSMTPEnv(t)
	cfg := e.config(t, "smtp", "")
	attach := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(attach, []byte("a,b\n.1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run(t, "# Report\n\n.dot line\n", "-c", cfg, "--md", "--subject", "Wöchentlicher Bericht",
		"--to", "to@example.com", "--cc", "cc@example.com", "--bcc", "Secret <hidden@example.com>", "--attach", attach)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	msgs := e.m.Messages()
	if len(msgs) != 1 {
		t.Fatalf("server received %d messages", len(msgs))
	}
	got := msgs[0]
	if id := strings.TrimSpace(stdout); !strings.HasPrefix(id, "<") || !bytes.Contains(got.Data, []byte("Message-ID: "+id+"\r\n")) {
		t.Errorf("stdout %q is not the delivered Message-ID", stdout)
	}
	if strings.Join(got.To, ",") != "to@example.com,cc@example.com,hidden@example.com" {
		t.Errorf("envelope = %v", got.To)
	}
	if bytes.Contains(got.Data, []byte("hidden@example.com")) {
		t.Error("Bcc address in the delivered message")
	}
	for _, want := range []string{"multipart/mixed", "multipart/alternative", "text/html", `filename="report.csv"`, "Subject: =?utf-8?q?W=C3=B6chentlicher_Bericht?="} {
		if !bytes.Contains(got.Data, []byte(want)) {
			t.Errorf("message lacks %q", want)
		}
	}
	log := readBytes(t, e.logfile)
	if !bytes.Contains(log, []byte(`"status":250`)) || !bytes.Contains(log, []byte(strings.TrimSpace(stdout))) {
		t.Errorf("log entry = %s", log)
	}
	e.assertNoPassword(t, stdout, stderr)
}

func TestSMTPSendJSON(t *testing.T) {
	e := newSMTPEnv(t)
	code, stdout, _ := e.run(t, "body", "-c", e.config(t, "smtp", ""), "--to", "to@example.com", "--subject", "s", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if code != ExitOK || doc["outcome"] != "sent" || doc["provider"] != "smtp" || !strings.HasPrefix(fmt.Sprint(doc["message_id"]), "<") {
		t.Errorf("exit %d, doc %v", code, doc)
	}
}

func TestSMTPAuthFailureJSON(t *testing.T) {
	e := newSMTPEnv(t)
	cfg := e.config(t, "smtp", "")
	e.m.SetUsers(map[string]string{smtpTestUser: "rotated"})
	code, stdout, stderr := e.run(t, "body", "-c", cfg, "--to", "to@example.com", "--subject", "s", "--json")
	if code != ExitAuth || !strings.Contains(stdout, `"code":"auth_failed"`) {
		t.Errorf("exit %d, stdout %s", code, stdout)
	}
	if msgs := queuedMessages(t, e.deferDir); len(msgs) != 0 {
		t.Error("authentication failure was deferred")
	}
	e.assertNoPassword(t, stdout, stderr)
}

func TestSMTPRejectedRecipientJSON(t *testing.T) {
	e := newSMTPEnv(t)
	e.m.SetRcptReply("cc@example.com", "550 5.1.1 no such user")
	code, stdout, _ := e.run(t, "body", "-c", e.config(t, "smtp", ""), "--to", "to@example.com", "--cc", "cc@example.com", "--subject", "s", "--json")
	if code != ExitSendFail || !strings.Contains(stdout, `"code":"rejected"`) || !strings.Contains(stdout, "cc@example.com") {
		t.Errorf("exit %d, stdout %s", code, stdout)
	}
	if e.m.Saw("DATA") {
		t.Error("partial delivery: DATA sent although a recipient was rejected")
	}
}

func TestSMTPTestModeRefusedBeforeConnecting(t *testing.T) {
	e := newSMTPEnv(t)
	code, stdout, stderr := e.run(t, "body", "-c", e.config(t, "smtp", ""), "--to", "to@example.com", "--subject", "s", "--test", "--json")
	if code != ExitUsage || !strings.Contains(stderr, "would deliver real email") || !strings.Contains(stdout, `"code":"usage_error"`) {
		t.Errorf("exit %d, stdout %s, stderr %s", code, stdout, stderr)
	}
	if len(e.m.Commands()) != 0 {
		t.Errorf("--test connected to the server: %+v", e.m.Commands())
	}
}

func TestSMTPPreviewIsLocal(t *testing.T) {
	e := newSMTPEnv(t)
	out := filepath.Join(t.TempDir(), "p.html")
	code, _, stderr := e.run(t, "# hi", "-c", e.config(t, "smtp", ""), "--md", "--preview", out)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(e.m.Commands()) != 0 {
		t.Error("preview contacted the SMTP server")
	}
}

// Without credentials, validate must not claim authentication was
// verified: the server below requires it, so a send fails afterwards.
func TestSMTPValidateWithoutCredentials(t *testing.T) {
	e := newSMTPEnv(t)
	path := e.config(t, "smtp", "")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	noCreds := strings.NewReplacer(fmt.Sprintf("username = %q\n", smtpTestUser), "",
		fmt.Sprintf("password = %q\n", smtpTestPass), "").Replace(string(data))
	if err := os.WriteFile(path, []byte(noCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run(t, "", "validate", "-c", path)
	if code != ExitOK {
		t.Fatalf("validate: exit %d\n%s%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "authentication verified") ||
		!strings.Contains(stdout, "SMTP connection and TLS negotiation verified") ||
		!strings.Contains(stdout, "authentication not checked: no SMTP credentials configured") {
		t.Errorf("validate output:\n%s", stdout)
	}
	if code, _, stderr := e.run(t, "body", "-c", path, "--no-defer", "--to", "a@example.com", "--subject", "s"); code != ExitAuth ||
		!strings.Contains(stderr, "530") {
		t.Errorf("send without credentials: exit %d, want %d (530); stderr:\n%s", code, ExitAuth, stderr)
	}
}

func TestSMTPValidatedMessage(t *testing.T) {
	for _, tc := range []struct {
		security, user, want string
	}{
		{"starttls", "u", "SMTP connection, TLS negotiation, and authentication verified with mx:587"},
		{"tls", "", "SMTP connection and TLS negotiation verified with mx:587; authentication not checked"},
		{"none", "", "SMTP connection verified with mx:587 (plaintext); authentication not checked"},
	} {
		cfg := &config.Config{SMTPHost: "mx", SMTPPort: 587, SMTPSecurity: tc.security, SMTPUsername: tc.user}
		if got := smtpValidated(cfg); !strings.HasPrefix(got, tc.want) {
			t.Errorf("smtpValidated(%s, user %q) = %q, want prefix %q", tc.security, tc.user, got, tc.want)
		}
	}
}

func TestSMTPValidate(t *testing.T) {
	e := newSMTPEnv(t)
	code, stdout, stderr := e.run(t, "", "validate", "-c", e.config(t, "smtp", ""))
	if code != ExitOK || !strings.Contains(stdout, "SMTP connection, TLS negotiation, and authentication verified") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	for _, v := range []string{"MAIL", "RCPT", "DATA"} {
		if e.m.Saw(v) {
			t.Errorf("validate sent %s", v)
		}
	}
	e.m.SetUsers(map[string]string{smtpTestUser: "rotated"})
	if code, stdout, stderr = e.run(t, "", "validate", "-c", e.config(t, "smtp", "")); code != ExitAuth {
		t.Errorf("bad password: exit %d\n%s", code, stdout)
	}
	e.assertNoPassword(t, stdout, stderr)
}

func TestSMTPDeferredMessageKeepsMessageIDAndEnvelope(t *testing.T) {
	e := newSMTPEnv(t)
	cfg := e.config(t, "smtp", "")
	e.m.SetRcptReply("to@example.com", "451 4.7.1 greylisted, try again later")
	code, _, stderr := e.run(t, "body", "-c", cfg, "--to", "to@example.com", "--bcc", "hidden@example.com", "--subject", "s")
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	msgs, err := (&deferral.Store{Dir: e.deferDir}).List()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("queue = %v, %v", msgs, err)
	}
	meta := msgs[0]
	wantAccount := fmt.Sprintf("%s@%s:%d/starttls", smtpTestUser, e.m.Host(), e.m.Port())
	if meta.Provider != "smtp" || meta.Account != wantAccount || meta.ContentType != "message/rfc822" ||
		meta.EnvelopeFrom != "ops@example.com" || strings.Join(meta.EnvelopeRecipients, ",") != "to@example.com,hidden@example.com" ||
		!strings.HasPrefix(meta.MessageID, "<") {
		t.Fatalf("metadata = %+v", meta)
	}

	e.m.SetRcptReply("to@example.com", "")
	code, stdout, stderr := e.run(t, "", "resend", "--force", "-c", cfg)
	if code != ExitOK {
		t.Fatalf("resend exit %d\n%s%s", code, stdout, stderr)
	}
	got := e.m.Messages()
	if len(got) != 1 || !bytes.Contains(got[0].Data, []byte("Message-ID: "+meta.MessageID+"\r\n")) ||
		strings.Join(got[0].To, ",") != "to@example.com,hidden@example.com" || bytes.Contains(got[0].Data, []byte("hidden@")) {
		t.Fatalf("resent message = %+v", got)
	}
	if !strings.Contains(stdout, meta.MessageID) {
		t.Errorf("resend did not report the stable Message-ID:\n%s", stdout)
	}
	if log := readBytes(t, e.logfile); !bytes.Contains(log, []byte(`"from":"ops@example.com"`)) {
		t.Errorf("resend log lacks the envelope sender:\n%s", log)
	}
	e.assertNoPassword(t, stdout, stderr)
}

// The original transport is used even after the default provider changed.
func TestResendUsesOriginalProviderAfterDefaultChanged(t *testing.T) {
	e := newSMTPEnv(t)
	e.m.SetRcptReply("to@example.com", "421 4.3.2 service not available")
	if code, _, stderr := e.run(t, "body", "-c", e.config(t, "smtp", ""), "--to", "to@example.com", "--subject", "s"); code != ExitDeferred {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	e.m.SetRcptReply("to@example.com", "")
	mg := testutil.NewMockMailgun(t)
	cfg := e.config(t, "mailgun", "\n[mailgun]\napi_key = \"k\"\nregion = \"us\"\n")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"resend", "--force", "-c", cfg, "--json"}, &stdout, &stderr, testInfo, e.opt, WithBaseURL(mg.URL()))
	if code != ExitOK || len(e.m.Messages()) != 1 || len(mg.Requests()) != 0 {
		t.Fatalf("exit %d, smtp messages %d, mailgun requests %d\n%s%s", code, len(e.m.Messages()), len(mg.Requests()), stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"provider":"smtp"`) {
		t.Errorf("resend JSON lacks the provider: %s", stdout.String())
	}
}

// Queue entries written before provider identity existed stay Mailgun's.
func TestResendLegacyMailgunEntryWithSMTPDefault(t *testing.T) {
	e := newSMTPEnv(t)
	seedDeferred(t, e.deferDir, "to@example.com", "legacy", 0)
	mg := testutil.NewMockMailgun(t)
	cfg := e.config(t, "smtp", "\n[mailgun]\napi_key = \"k\"\nregion = \"us\"\n")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"resend", "--force", "-c", cfg}, &stdout, &stderr, testInfo, e.opt, WithBaseURL(mg.URL()))
	if code != ExitOK || len(mg.Requests()) != 1 || len(e.m.Commands()) != 0 {
		t.Fatalf("exit %d, mailgun %d, smtp %d\n%s%s", code, len(mg.Requests()), len(e.m.Commands()), stdout.String(), stderr.String())
	}
}

// A message whose provider is no longer configured is kept untouched.
func TestResendProviderUnavailableKeepsMessage(t *testing.T) {
	e := newSMTPEnv(t)
	meta := seedDeferred(t, e.deferDir, "to@example.com", "legacy", 0)
	cfg := e.config(t, "smtp", "") // no [mailgun] section
	code, stdout, stderr := e.run(t, "", "resend", "--force", "-c", cfg, "--json")
	if code != ExitSendFail || !strings.Contains(stdout, `"reason":"provider_unavailable"`) || !strings.Contains(stderr, "mailgun.api_key") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	got, ok, err := (&deferral.Store{Dir: e.deferDir}).Get(meta.ID)
	if err != nil || !ok || got.Failed || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("message after blocked resend = %+v, %v, %v", got, ok, err)
	}
	// A later plain resend (no --force needed) works once configured.
	mg := testutil.NewMockMailgun(t)
	var out, errOut bytes.Buffer
	code = Run([]string{"resend", "-c", e.config(t, "smtp", "\n[mailgun]\napi_key = \"k\"\nregion = \"us\"\n"), meta.ID, "--force"},
		&out, &errOut, testInfo, e.opt, WithBaseURL(mg.URL()))
	if code != ExitOK || len(mg.Requests()) != 1 {
		t.Fatalf("after configuring Mailgun: exit %d\n%s%s", code, out.String(), errOut.String())
	}
}

func TestResendBlockedOnSMTPAccountChange(t *testing.T) {
	e := newSMTPEnv(t)
	e.m.SetRcptReply("to@example.com", "421 4.3.2 try later")
	if code, _, _ := e.run(t, "body", "-c", e.config(t, "smtp", ""), "--to", "to@example.com", "--subject", "s"); code != ExitDeferred {
		t.Fatal("not deferred")
	}
	e.m.SetRcptReply("to@example.com", "")
	other := e.config(t, "smtp", "")
	data := readBytes(t, other)
	if err := os.WriteFile(other, bytes.Replace(data, []byte(smtpTestUser), []byte("someone-else@example.com"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run(t, "", "resend", "--force", "-c", other)
	if code != ExitSendFail || !strings.Contains(stdout, "blocked (account mismatch") || len(e.m.Messages()) != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	msgs := queuedMessages(t, e.deferDir)
	if len(msgs) != 1 || msgs[0].Failed || msgs[0].Attempts != 0 {
		t.Fatalf("queue after mismatch = %+v", msgs)
	}
}

func TestSMTPDedupSkipsRepeat(t *testing.T) {
	e := newSMTPEnv(t)
	cfg := e.config(t, "smtp", "")
	for i, want := range []int{ExitOK, ExitOK} {
		code, stdout, stderr := e.run(t, "body", "-c", cfg, "--to", "to@example.com", "--subject", "s", "--dedup-key", "disk-full")
		if code != want {
			t.Fatalf("send %d: exit %d\n%s%s", i, code, stdout, stderr)
		}
		if i == 1 && !strings.HasPrefix(stdout, "skipped: duplicate notification") {
			t.Errorf("second send not skipped: %s", stdout)
		}
	}
	if n := len(e.m.Messages()); n != 1 {
		t.Errorf("server received %d messages, want 1", n)
	}
}

func TestQueueShowsProvider(t *testing.T) {
	e := newSMTPEnv(t)
	e.m.SetRcptReply("to@example.com", "421 4.3.2 try later")
	e.run(t, "body", "-c", e.config(t, "smtp", ""), "--to", "to@example.com", "--subject", "s")
	code, stdout, _ := e.run(t, "", "queue", "list", "-c", e.config(t, "smtp", ""), "--json")
	if code != ExitOK || !strings.Contains(stdout, `"provider":"smtp"`) {
		t.Errorf("queue list: exit %d\n%s", code, stdout)
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// queuedMessages lists the deferred messages (a missing defer_dir is empty).
func queuedMessages(t *testing.T, dir string) []deferral.Metadata {
	t.Helper()
	msgs, err := (&deferral.Store{Dir: dir}).List()
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}
