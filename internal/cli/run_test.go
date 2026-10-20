package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goodsign/monday"
	"github.com/thorstenkramm/mailfire/internal/locale"
	"github.com/thorstenkramm/mailfire/internal/testutil"
)

// TestRunHelperProcess is the job of the run tests: the test binary run
// with RUN_HELPER set. It is not a test and returns immediately without
// RUN_HELPER.
func TestRunHelperProcess(t *testing.T) {
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if os.Getenv("RUN_AS_MAILFIRE") != "" {
		// Act as the mailfire binary itself, with real signal handling
		// (no injected signal channel). The job must not act as the CLI.
		if err := os.Unsetenv("RUN_AS_MAILFIRE"); err != nil {
			t.Fatal(err)
		}
		os.Exit(Run(args, os.Stdout, os.Stderr, testInfo, WithBaseURL(os.Getenv("RUN_MOCK_URL"))))
	}
	mode := os.Getenv("RUN_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "remove-inputs":
		for _, path := range args {
			if err := os.Remove(path); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(99)
			}
		}
		fmt.Println("job completed")
	case "spill-sleep": // spill both streams to temporary files, then sleep args[0]
		for i := 0; i < 2000; i++ {
			fmt.Printf("stdout line %d of a job that prints more than the memory limit\n", i)
			fmt.Fprintf(os.Stderr, "stderr line %d of a job that prints more than the memory limit\n", i)
		}
		if len(args) > 1 {
			if err := os.WriteFile(args[1], nil, 0o600); err != nil {
				os.Exit(99)
			}
		}
		d, err := time.ParseDuration(args[0])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(d)
	case "echo": // print the arguments, one per line
		for _, a := range args {
			fmt.Println(a)
		}
	case "fail": // stdout, stderr, then exit with args[0]
		fmt.Println("some output")
		fmt.Fprintln(os.Stderr, "something went wrong")
		n, err := strconv.Atoi(args[0])
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(n)
	case "touch": // create the marker file args[0]
		if err := os.WriteFile(args[0], nil, 0o600); err != nil {
			os.Exit(99)
		}
	case "sleep":
		d, err := time.ParseDuration(args[0])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(d)
	case "touch-sleep": // create the marker file args[0], then sleep args[1]
		if err := os.WriteFile(args[0], nil, 0o600); err != nil {
			os.Exit(99)
		}
		d, err := time.ParseDuration(args[1])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(d)
	case "lines": // n numbered lines
		n, err := strconv.Atoi(args[0])
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= n; i++ {
			fmt.Printf("line %d\n", i)
		}
	case "fancy": // ANSI colors, progress, invalid UTF-8, a long line
		fmt.Print("\x1b[32mgreen\x1b[0m\n")
		fmt.Print("10%\r50%\r100%\r\n")
		if _, err := os.Stdout.Write([]byte("bad \xff byte\n")); err != nil {
			t.Fatal(err)
		}
		fmt.Println(strings.Repeat("x", 1500))
	}
	os.Exit(0)
}

func TestLogReaderNormalizesIncrementally(t *testing.T) {
	// A one-byte source makes CRLF and UTF-8 sequences cross read boundaries.
	src := &oneByteReader{data: []byte("a\r\nb\rc\xff\xfed\n")}
	got, err := io.ReadAll(newLogReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\nb\rc\uFFFDd\n"; string(got) != want {
		t.Fatalf("normalized log = %q, want %q", got, want)
	}
}

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

// helperJob returns the command line running TestRunHelperProcess in
// mode, and sets RUN_HELPER for the test (the job inherits it).
func helperJob(t *testing.T, mode string, args ...string) []string {
	t.Helper()
	t.Setenv("RUN_HELPER", mode)
	return append([]string{"--", os.Args[0], "-test.run=TestRunHelperProcess", "--"}, args...)
}

// runRun runs "mailfire run" against a mock Mailgun server.
func runRun(t *testing.T, m *testutil.MockMailgun, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts := []Option{WithStdin(strings.NewReader(""))}
	if m != nil {
		opts = append(opts, WithBaseURL(m.URL()))
	}
	code := Run(append([]string{"run"}, args...), &stdout, &stderr, testInfo, opts...)
	return code, stdout.String(), stderr.String()
}

func runArgs(cfg string, extra ...string) []string {
	return append([]string{"-c", cfg, "--to", "ops@example.com", "--name", "nightly"}, extra...)
}

func hostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRunSuccessReport(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, stdout, stderr := runRun(t, m, append(runArgs(cfg), helperJob(t, "echo", "hello", "world")...)...)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "<abc123@") {
		t.Errorf("stdout = %q, want the message ID", stdout)
	}
	req := m.LastRequest(t)
	if want := "✅ nightly succeeded on " + hostname(t); req.Field("subject") != want {
		t.Errorf("subject = %q, want %q", req.Field("subject"), want)
	}
	text := req.Field("text")
	for _, want := range []string{"nightly succeeded", "Exit code: 0", "Last 10 lines of stdout", "hello\nworld",
		"Full logs attached: nightly.stdout.log"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "stderr") {
		t.Errorf("an empty stderr must have no section:\n%s", text)
	}
	if req.Field("html") == "" {
		t.Error("the report has no html part")
	}
	if len(req.Files) != 1 || req.Files[0].Filename != "nightly.stdout.log" ||
		string(req.Files[0].Content) != "hello\nworld\n" {
		t.Errorf("attachments = %+v, want nightly.stdout.log with the output", req.Files)
	}
}

func TestRunFailureExitCodes(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	for _, n := range []int{1, 3} {
		m := testutil.NewMockMailgun(t)
		code, _, stderr := runRun(t, m, append(runArgs(cfg), helperJob(t, "fail", strconv.Itoa(n))...)...)
		if code != n {
			t.Errorf("job exit %d: mailfire exit = %d; stderr:\n%s", n, code, stderr)
		}
		req := m.LastRequest(t)
		if !strings.HasPrefix(req.Field("subject"), "🔴 nightly failed on ") {
			t.Errorf("subject = %q, want a failure subject", req.Field("subject"))
		}
		text := req.Field("text")
		for _, want := range []string{"Exit code: " + strconv.Itoa(n), "some output", "something went wrong"} {
			if !strings.Contains(text, want) {
				t.Errorf("text part lacks %q:\n%s", want, text)
			}
		}
		if len(req.Files) != 2 {
			t.Errorf("attachments = %d, want 2 (stdout and stderr)", len(req.Files))
		}
	}
}

// Flags after the command belong to the job, unparsed.
func TestRunFlagsAfterCommandReachTheJob(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, stderr := runRun(t, m, append(runArgs(cfg), helperJob(t, "echo", "--to", "-h", "--json")...)...)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if got := string(m.LastRequest(t).Files[0].Content); got != "--to\n-h\n--json\n" {
		t.Errorf("job output = %q, want the flags unchanged", got)
	}
	// Without "--", the first non-flag argument starts the command.
	t.Setenv("RUN_HELPER", "echo")
	code, _, stderr = runRun(t, m, "-c", cfg, "--to", "ops@example.com", os.Args[0], "-test.run=TestRunHelperProcess", "--", "--help")
	if code != ExitOK {
		t.Fatalf("without --: exit = %d; stderr:\n%s", code, stderr)
	}
	if got := string(m.LastRequest(t).Files[0].Content); got != "--help\n" {
		t.Errorf("job output = %q, want %q", got, "--help\n")
	}
}

// A usage, configuration, or template error is reported before the job
// starts: the job does not run.
func TestRunPreflightErrorsDoNotRunTheJob(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	badCfg := filepath.Join(t.TempDir(), "bad.conf")
	if err := os.WriteFile(badCfg, []byte("[mailgun]\nregion = \"us\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badLocaleCfg := writeConfig(t, "from = \"ops@example.com\"\n[run]\nlocale = \"xx_YY\"\n")
	noFrom := writeConfig(t, "")
	badReport := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(badReport, []byte("{{.NoSuchField}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"config error", []string{"-c", badCfg, "--to", "a@example.com"}, ExitConfig, "mailgun.api_key"},
		{"invalid [run].locale", []string{"-c", badLocaleCfg, "--to", "a@example.com"}, ExitConfig, "run.locale"},
		{"no sender", []string{"-c", noFrom, "--to", "a@example.com"}, ExitUsage, "no sender"},
		{"invalid recipient", []string{"-c", cfg, "--to", "nope"}, ExitUsage, "invalid --to address"},
		{"missing --to", []string{"-c", cfg}, ExitUsage, "missing required flag: --to"},
		{"unknown placeholder", []string{"-c", cfg, "--to", "a@example.com", "--subject", "{unknown}"}, ExitUsage, "unknown placeholder {unknown}"},
		{"unclosed brace", []string{"-c", cfg, "--to", "a@example.com", "--subject", "{job"}, ExitUsage, "unclosed {"},
		{"bad report template", []string{"-c", cfg, "--to", "a@example.com", "--report-template", badReport}, ExitUsage, "NoSuchField"},
		{"missing report template", []string{"-c", cfg, "--to", "a@example.com", "--report-template", badReport + ".gone"}, ExitUsage, "report template"},
		{"unsupported locale", []string{"-c", cfg, "--to", "a@example.com", "--locale", "xx_YY"}, ExitUsage, "invalid --locale"},
		{"on change", []string{"-c", cfg, "--to", "a@example.com", "--on", "change"}, ExitUsage, "invalid --on"},
		{"tee with preview to stdout", []string{"-c", cfg, "--tee", "--preview", "-"}, ExitUsage, "--tee cannot be combined with --preview -"},
		{"preview with test", []string{"-c", cfg, "--preview", "-", "--test"}, ExitUsage, "--preview cannot be combined with --test"},
		{"preview with json", []string{"-c", cfg, "--preview", "-", "--json"}, ExitUsage, "--json"},
		{"bad success codes", []string{"-c", cfg, "--to", "a@example.com", "--success-codes", "0,x"}, ExitUsage, "invalid --success-codes"},
		{"bad max runtime", []string{"-c", cfg, "--to", "a@example.com", "--max-runtime", "0s"}, ExitUsage, "invalid --max-runtime"},
		{"negative tail", []string{"-c", cfg, "--to", "a@example.com", "--tail", "-1"}, ExitUsage, "invalid --tail"},
		{"unknown template", []string{"-c", cfg, "--to", "a@example.com", "--template", "fancy"}, ExitUsage, "unknown template"},
		{"cooldown without key", []string{"-c", cfg, "--to", "a@example.com", "--cooldown", "1h"}, ExitUsage, "--cooldown requires --dedup-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			marker := filepath.Join(t.TempDir(), "ran")
			code, _, stderr := runRun(t, m, append(tc.args, helperJob(t, "touch", marker)...)...)
			if code != tc.code {
				t.Errorf("exit = %d, want %d; stderr:\n%s", code, tc.code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, stderr)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("the job ran despite the preflight error")
			}
			if len(m.Requests()) != 0 {
				t.Errorf("mock received %d requests, want 0", len(m.Requests()))
			}
		})
	}
}

func TestRunMissingCommand(t *testing.T) {
	code, stdout, stderr := runRun(t, nil, "--to", "a@example.com")
	if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "missing command") ||
		!strings.Contains(stderr, "Flags (run):") {
		t.Errorf("exit %d, stdout %q, stderr:\n%s", code, stdout, stderr)
	}
}

func TestRunCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--to", "a@example.com", "--help"}} {
		code, stdout, _ := runRun(t, nil, args...)
		if code != ExitOK || stdout != runUsage {
			t.Errorf("run %v: exit %d, stdout:\n%s", args, code, stdout)
		}
	}
}

func TestRunSubjectTemplate(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, stderr := runRun(t, m, append(runArgs(cfg, "--subject",
		"{{x}} [{status}] {job} exit {exitcode} {emoji} in {duration}"), helperJob(t, "fail", "2")...)...)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr:\n%s", code, stderr)
	}
	subject := m.LastRequest(t).Field("subject")
	if !regexp.MustCompile(`^\{x\} \[failed\] nightly exit 2 🔴 in \d+(ms|s)$`).MatchString(subject) {
		t.Errorf("subject = %q", subject)
	}
}

func TestRunOnFailure(t *testing.T) {
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	m := testutil.NewMockMailgun(t)
	code, stdout, stderr := runRun(t, m, append(runArgs(cfg, "--on", "failure"), helperJob(t, "echo", "ok")...)...)
	if code != ExitOK || len(m.Requests()) != 0 {
		t.Errorf("success: exit %d, %d requests, want 0 and 0; stderr:\n%s", code, len(m.Requests()), stderr)
	}
	if !strings.Contains(stdout, "report not sent") {
		t.Errorf("stdout = %q, want a not-sent note", stdout)
	}
	code, _, _ = runRun(t, m, append(runArgs(cfg, "--on", "failure"), helperJob(t, "fail", "1")...)...)
	if code != 1 || len(m.Requests()) != 1 {
		t.Errorf("failure: exit %d, %d requests, want 1 and 1", code, len(m.Requests()))
	}
	// A job that could not start counts as a failure.
	code, _, _ = runRun(t, m, runArgs(cfg, "--on", "failure", "--", "mailfire-no-such-command-xyz")...)
	if code != 127 || len(m.Requests()) != 2 {
		t.Errorf("not found: exit %d, %d requests, want 127 and 2", code, len(m.Requests()))
	}
	if text := m.LastRequest(t).Field("text"); !strings.Contains(text, "could not start:") {
		t.Errorf("text part lacks the start error:\n%s", text)
	}
	// A timeout counts as a failure.
	code, _, _ = runRun(t, m, append(runArgs(cfg, "--on", "failure", "--max-runtime", "300ms"), helperJob(t, "sleep", "30s")...)...)
	if code == 0 || len(m.Requests()) != 3 {
		t.Errorf("timeout: exit %d, %d requests, want nonzero and 3", code, len(m.Requests()))
	}
}

func TestRunSuccessCodes(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, stderr := runRun(t, m, append(runArgs(cfg, "--success-codes", "0,24"), helperJob(t, "fail", "24")...)...)
	if code != 24 {
		t.Fatalf("exit = %d, want 24 (the job's); stderr:\n%s", code, stderr)
	}
	if subject := m.LastRequest(t).Field("subject"); !strings.HasPrefix(subject, "✅ nightly succeeded") {
		t.Errorf("subject = %q, want success", subject)
	}
}

func TestRunMaxRuntime(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	start := time.Now()
	code, _, stderr := runRun(t, m, append(runArgs(cfg, "--max-runtime", "300ms"), helperJob(t, "sleep", "30s")...)...)
	if time.Since(start) > 15*time.Second {
		t.Errorf("the job was not stopped promptly")
	}
	if code == ExitOK {
		t.Errorf("exit = 0, want the killed job's code; stderr:\n%s", stderr)
	}
	if subject := m.LastRequest(t).Field("subject"); !strings.HasPrefix(subject, "⏱️ nightly timed out") {
		t.Errorf("subject = %q, want a timeout subject", subject)
	}
}

func TestRunJSONWithTee(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, stdout, stderr := runRun(t, m, append(runArgs(cfg, "--json", "--tee"), helperJob(t, "fail", "3")...)...)
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout has more than one JSON document:\n%s", stdout)
	}
	if doc["command"] != "run" || doc["outcome"] != "sent" || doc["exit_code"] != float64(3) || doc["error"] != nil {
		t.Errorf("document = %v", doc)
	}
	job, ok := doc["job"].(map[string]any)
	if !ok {
		t.Fatalf("job is not an object: %v", doc["job"])
	}
	if job["status"] != "failure" || job["exit_code"] != float64(3) || job["name"] != "nightly" {
		t.Errorf("job = %v", job)
	}
	if out, ok := job["stdout"].(map[string]any); !ok || out["bytes"] != float64(12) || out["attached"] != true {
		t.Errorf("job.stdout = %v", out)
	}
	for _, want := range []string{"some output", "something went wrong"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("--tee with --json: stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestRunTee(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, stdout, stderr := runRun(t, m, append(runArgs(cfg, "--tee"), helperJob(t, "fail", "0")...)...)
	if code != ExitOK || !strings.Contains(stdout, "some output") || !strings.Contains(stderr, "something went wrong") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestRunJSONErrors(t *testing.T) {
	// A usage error before the job is an envelope-only document.
	code, stdout, _ := runRun(t, nil, "--json", "--on", "change", "--", "true")
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if code != ExitUsage || doc["command"] != "run" || doc["exit_code"] != float64(ExitUsage) {
		t.Errorf("exit %d, document %v", code, doc)
	}
	// --json after the command belongs to the job.
	if jsonRequested := (func() bool {
		_, out, _ := runRun(t, nil, "--on", "change", "--", "true", "--json")
		return strings.HasPrefix(out, "{")
	})(); jsonRequested {
		t.Error("--json after the command must not select JSON output")
	}
}

// When the report cannot be sent, mailfire's own code wins even though
// the job succeeded; a deferred report exits 4.
func TestRunReportDeferred(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	m.SendResponse = testutil.InternalError()
	cfg, deferDir := deferConfig(t, "")
	code, stdout, stderr := runRun(t, m, append(runArgs(cfg, "--json"), helperJob(t, "echo", "ok")...)...)
	if code != ExitDeferred {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitDeferred, stderr)
	}
	if ids := deferredIDs(t, deferDir); len(ids) != 1 {
		t.Errorf("deferred messages = %v, want 1", ids)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["outcome"] != "deferred" || doc["exit_code"] != float64(ExitDeferred) || doc["error"] == nil {
		t.Errorf("document = %v", doc)
	}
}

func TestRunFailedJobKeepsExitCodeWhenReportFails(t *testing.T) {
	for _, noDefer := range []bool{false, true} {
		t.Run(fmt.Sprint(noDefer), func(t *testing.T) {
			m := testutil.NewMockMailgun(t)
			m.SendResponse = testutil.InternalError()
			cfg, _ := deferConfig(t, "")
			args := runArgs(cfg, "--json")
			if noDefer {
				args = append(args, "--no-defer")
			}
			code, stdout, stderr := runRun(t, m, append(args, helperJob(t, "fail", "17")...)...)
			var doc runResult
			if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
				t.Fatal(err)
			}
			if code != 17 || doc.ExitCode != 17 || doc.Error == nil || stderr == "" {
				t.Errorf("code %d, result %+v, stderr %q", code, doc, stderr)
			}
		})
	}
}

func TestRunPreviewLocale(t *testing.T) {
	cfg := writeConfig(t, "[run]\nlocale = \"de_DE\"\n")
	for _, tc := range []struct{ env, flag, want string }{
		{"", "", "Okt."},
		{"fr_FR", "", "oct"},
		{"xx_YY", "en_US", "Oct"},
	} {
		t.Run(tc.env+tc.flag, func(t *testing.T) {
			t.Setenv("MAILFIRE_RUN_LOCALE", tc.env)
			// Use a stable template-independent assertion on locale resolution;
			// the same loaded configuration is used to build the preview report.
			opts := RunOptions{Config: cfg, Preview: "-", Locale: tc.flag}
			loaded, err := loadRunConfig(&opts, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			ts := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			if got := locale.Format(ts, monday.Locale(runLocale(&opts, loaded))); !strings.Contains(got, tc.want) {
				t.Errorf("date %q does not contain %q", got, tc.want)
			}
		})
	}
	bad := writeConfig(t, "[run]\nlocale = \"xx_YY\"\n")
	marker := filepath.Join(t.TempDir(), "ran")
	code, _, _ := runRun(t, nil, append([]string{"-c", bad, "--preview", "-"}, helperJob(t, "touch", marker)...)...)
	if code != ExitConfig {
		t.Errorf("invalid preview locale: exit %d", code)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("job ran with invalid preview locale")
	}
}

func TestRunLocalDeliveryErrorsDoNotRunJob(t *testing.T) {
	badLog := writeConfig(t, "from = \"ops@example.com\"\n[logging]\nlogfile = "+strconv.Quote(t.TempDir())+"\n")
	deferDir := t.TempDir()
	badDedup := writeConfig(t, "from = \"ops@example.com\"\n[defer]\ndefer_dir = "+strconv.Quote(deferDir)+"\n[dedup]\nstate_dir = "+strconv.Quote(filepath.Join(deferDir, "invalid"))+"\n")
	for _, cfg := range []string{badLog, badDedup} {
		marker := filepath.Join(t.TempDir(), "ran")
		code, _, stderr := runRun(t, nil, append(runArgs(cfg, "--dedup-key", "test"), helperJob(t, "touch", marker)...)...)
		if code != ExitConfig {
			t.Errorf("exit %d, stderr %s", code, stderr)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("job ran with invalid local delivery settings")
		}
	}
}

func TestRunGmailCredentialsCheckedBeforeJob(t *testing.T) {
	cfg := writeConfig(t, "provider = \"gmail\"\nfrom = \"ops@example.com\"\n[gmail]\ncredentials = "+strconv.Quote(t.TempDir())+"\n")
	marker := filepath.Join(t.TempDir(), "ran")
	code, _, stderr := runRun(t, nil, append(runArgs(cfg), helperJob(t, "touch", marker)...)...)
	if code != ExitConfig || !strings.Contains(stderr, "credentials") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("job ran with unreadable Gmail credentials")
	}
}

func TestRunPreview(t *testing.T) {
	out := filepath.Join(t.TempDir(), "report.html")
	code, stdout, stderr := runRun(t, nil, append([]string{"--preview", out, "--name", "nightly"}, helperJob(t, "fail", "5")...)...)
	if code != 5 {
		t.Fatalf("exit = %d, want 5 (the job's); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "preview written to") {
		t.Errorf("stdout = %q", stdout)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<html", "nightly failed", "something went wrong"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	// To stdout.
	code, stdout, _ = runRun(t, nil, append([]string{"--preview", "-"}, helperJob(t, "echo", "x")...)...)
	if code != ExitOK || !strings.HasPrefix(stdout, "<!DOCTYPE html>") {
		t.Errorf("--preview -: exit %d, stdout %.60q", code, stdout)
	}
}

func TestRunNoLogsAndNoTail(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	code, _, stderr := runRun(t, m, append(runArgs(cfg, "--no-attach-logs", "--tail", "0"), helperJob(t, "fail", "1")...)...)
	if code != 1 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	req := m.LastRequest(t)
	if len(req.Files) != 0 {
		t.Errorf("attachments = %d, want none", len(req.Files))
	}
	text := req.Field("text")
	if strings.Contains(text, "some output") || strings.Contains(text, "Full logs attached") {
		t.Errorf("text part shows output despite --tail 0 --no-attach-logs:\n%s", text)
	}
}

func TestRunTailLines(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	if code, _, stderr := runRun(t, m, append(runArgs(cfg, "--tail", "3"), helperJob(t, "lines", "20")...)...); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	text := m.LastRequest(t).Field("text")
	if !strings.Contains(text, "line 18\nline 19\nline 20") || strings.Contains(text, "line 17") {
		t.Errorf("text part does not show exactly the last 3 lines:\n%s", text)
	}
}

func TestRunCleansOutput(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	if code, _, stderr := runRun(t, m, append(runArgs(cfg), helperJob(t, "fancy")...)...); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	text := m.LastRequest(t).Field("text")
	if strings.Contains(text, "\x1b") {
		t.Error("ANSI escape sequences reached the body")
	}
	for _, want := range []string{"green\n100%\nbad \uFFFD byte\n", strings.Repeat("x", 1000) + "…"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, strings.Repeat("x", 1001)) {
		t.Error("a long line was not cut at 1,000 characters")
	}
	// The attachment keeps the full line, as valid UTF-8.
	att := string(m.LastRequest(t).Files[0].Content)
	if !strings.Contains(att, strings.Repeat("x", 1500)) || !strings.Contains(att, "bad \uFFFD byte") {
		t.Error("the attachment lost content or kept invalid UTF-8")
	}
}

func TestRunLocale(t *testing.T) {
	german := regexp.MustCompile(`Started: \S+\., \d{1,2}\. \S+\. \d{4} \d{2}:\d{2}:\d{2} `)
	english := regexp.MustCompile(`Started: [A-Z][a-z]{2}, [A-Z][a-z]{2} \d{1,2}, \d{4} \d{2}:\d{2}:\d{2} `)
	british := regexp.MustCompile(`Started: [A-Z][a-z]{2}, \d{1,2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} `)
	plain := writeConfig(t, "from = \"ops@example.com\"\n")
	deCfg := writeConfig(t, "from = \"ops@example.com\"\n[run]\nlocale = \"de_DE\"\n")
	for _, tc := range []struct {
		name string
		cfg  string
		env  map[string]string
		flag string
		want *regexp.Regexp
	}{
		{"flag beats config", deCfg, nil, "en_GB", british},
		{"config beats environment", deCfg, map[string]string{"LANG": "en_GB.UTF-8"}, "", german},
		{"environment variable beats file", deCfg, map[string]string{"MAILFIRE_RUN_LOCALE": "en_US"}, "", english},
		{"LC_ALL", plain, map[string]string{"LC_ALL": "de_DE.UTF-8"}, "", german},
		{"LC_TIME before LANG", plain, map[string]string{"LC_TIME": "en_GB", "LANG": "de_DE"}, "", british},
		{"LANG=C", plain, map[string]string{"LANG": "C"}, "", english},
		{"unset environment", plain, nil, "", english},
		{"unsupported environment locale", plain, map[string]string{"LANG": "xx_YY.UTF-8"}, "", english},
		{"bare language", plain, nil, "de", german},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.flag == "" && tc.cfg == plain {
				// Without --locale or [run].locale, Windows uses the user
				// locale (TestRunWindowsUserLocale), not LANG or en_US.
				t.Skip("Windows reads the user locale, not LANG")
			}
			for _, k := range []string{"LC_ALL", "LC_TIME", "LANG", "MAILFIRE_RUN_LOCALE"} {
				t.Setenv(k, tc.env[k])
			}
			m := testutil.NewMockMailgun(t)
			extra := []string{}
			if tc.flag != "" {
				extra = []string{"--locale", tc.flag}
			}
			if code, _, stderr := runRun(t, m, append(runArgs(tc.cfg, extra...), helperJob(t, "echo", "x")...)...); code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", code, stderr)
			}
			if text := m.LastRequest(t).Field("text"); !tc.want.MatchString(text) {
				t.Errorf("start date does not match %s:\n%s", tc.want, text)
			}
		})
	}
}

func TestRunCustomReportTemplate(t *testing.T) {
	m := testutil.NewMockMailgun(t)
	cfg := writeConfig(t, "from = \"ops@example.com\"\n")
	tmpl := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(tmpl, []byte("# {{.Job}}: {{.Status}} ({{.ExitCode}})\n\n{{.StdoutTail}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runRun(t, m, append(runArgs(cfg, "--report-template", tmpl), helperJob(t, "echo", "custom")...)...); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if text := m.LastRequest(t).Field("text"); !strings.HasPrefix(text, "nightly: success (0)\n\ncustom") {
		t.Errorf("text part = %q", text)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		850 * time.Millisecond:                       "850ms",
		4 * time.Second:                              "4s",
		3*time.Minute + 12*time.Second:               "3m 12s",
		2*time.Hour + 5*time.Minute + 59*time.Second: "2h 5m",
		27 * time.Hour:                               "1d 3h",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestExpandSubject(t *testing.T) {
	vals := map[string]string{"job": "backup", "status": "succeeded"}
	for tpl, want := range map[string]string{
		"{job} {status}":  "backup succeeded",
		"{{x}}":           "{x}",
		"a }} b {{":       "a } b {",
		"no placeholders": "no placeholders",
	} {
		if got, err := expandSubject(tpl, vals); err != nil || got != want {
			t.Errorf("expandSubject(%q) = %q, %v; want %q", tpl, got, err, want)
		}
	}
	for _, tpl := range []string{"{nope}", "{job", "job}"} {
		if _, err := expandSubject(tpl, vals); err == nil {
			t.Errorf("expandSubject(%q): want an error", tpl)
		}
	}
}

func TestQuoteCommand(t *testing.T) {
	got := quoteArg("it's here", false)
	if got != `'it'\''s here'` {
		t.Errorf("quoteArg = %q", got)
	}
	if got := quoteArg("plain-arg", false); got != "plain-arg" {
		t.Errorf("quoteArg(plain-arg) = %q", got)
	}
	if got := quoteArg(`C:\Program Files\x`, true); got != `"C:\Program Files\x"` {
		t.Errorf("windows quoteArg = %q", got)
	}
	if got := quoteArg(`C:\Jobs\backup.bat`, true); got != `C:\Jobs\backup.bat` {
		t.Errorf("windows quoteArg of a plain path = %q, want it unquoted", got)
	}
	if got := fileSafe("a/b c"); got != "a_b_c" {
		t.Errorf("fileSafe = %q", got)
	}
	if got := fileSafe("../"); got != "job" {
		t.Errorf("fileSafe(../) = %q", got)
	}
}
