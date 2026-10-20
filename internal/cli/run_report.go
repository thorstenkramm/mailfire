package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/goodsign/monday"
	"github.com/thorstenkramm/mailfire/internal/config"
	"github.com/thorstenkramm/mailfire/internal/jobrun"
	"github.com/thorstenkramm/mailfire/internal/locale"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
	"github.com/thorstenkramm/mailfire/internal/render"
	"github.com/thorstenkramm/mailfire/templates"
)

// run_report.go builds the report of "mailfire run" (task 33): the job's
// status, the subject from its placeholder template, the Markdown body
// from the report content template, and the log attachments.

// Job statuses, as in the JSON document and the report's .Status field.
const (
	statusSuccess     = "success"
	statusFailure     = "failure"
	statusTimeout     = "timeout"
	statusInterrupted = "interrupted"
)

// defaultRunSubject is the subject template without --subject.
const defaultRunSubject = "{emoji} {job} {status} on {host}"

// maxTailLine cuts each tail line shown in the body, so a job printing
// one huge line cannot blow up the email (the attachment keeps it).
const maxTailLine = 1000

var statusEmoji = map[string]string{
	statusSuccess:     "✅",
	statusFailure:     "🔴",
	statusTimeout:     "⏱️",
	statusInterrupted: "⚠️",
}

var statusWord = map[string]string{
	statusSuccess:     "succeeded",
	statusFailure:     "failed",
	statusTimeout:     "timed out",
	statusInterrupted: "interrupted",
}

// ReportData are the fields of the report content template. They are a
// documented, stable contract (docs/content/guides/templates.md): fields
// may be added, never renamed or removed.
type ReportData struct {
	Emoji      string // ✅ success, 🔴 failure, ⏱️ timeout, ⚠️ interrupted
	Job        string // job name: --name or the command's file name
	JobFull    string // full command line, shell-quoted
	Status     string // "success", "failure", "timeout", or "interrupted"
	StatusWord string // "succeeded", "failed", "timed out", or "interrupted"
	Host       string // host name
	WorkDir    string // working directory
	Start      string // start time, localized
	End        string // end time, localized
	Duration   string // e.g. "850ms", "4s", "3m 12s", "2h 5m", "1d 3h"
	ExitCode   int    // the job's exit code (127/126: could not start; 128+n: signal n)
	ExitDetail string // "killed by signal 9 (KILL)", "could not start: ...", or empty
	Tail       int    // --tail: the number of lines requested
	// StdoutTail and StderrTail are the last lines of each stream,
	// cleaned for display (ANSI sequences removed, \r-overwritten
	// progress collapsed, invalid UTF-8 replaced, lines cut at 1,000
	// characters); empty for an empty stream or --tail 0.
	StdoutTail    string
	StderrTail    string
	StdoutBytes   int64  // bytes the job wrote to stdout
	StderrBytes   int64  // bytes the job wrote to stderr
	StdoutOmitted int64  // stdout bytes dropped by the log cap
	StderrOmitted int64  // stderr bytes dropped by the log cap
	Logs          string // attached log file names, comma separated; empty when none
}

// report is the composed report of one run.
type report struct {
	ReportData
	subject     string
	markdown    string
	err         error // report template execution failure
	attachments []mailgun.File
	job         *jobrun.Result
	attached    [2]bool // stdout, stderr
}

// loadReportTemplate parses the report content template: the built-in
// job-report.md, or the --report-template file. It is executed once with
// sample data, so a template referring to unknown fields fails before
// the job runs.
func loadReportTemplate(path string) (*template.Template, error) {
	name := render.ReportTemplate
	var source []byte
	var err error
	if path == "" {
		source, err = templates.FS.ReadFile(render.ReportTemplate)
	} else {
		name = config.ExpandPath(path)
		source, err = readRegular("report template", name)
	}
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("report template: %w", err)
	}
	sample := ReportData{Emoji: "✅", Job: "job", Status: statusSuccess, StatusWord: "succeeded",
		StdoutTail: "x", StderrTail: "x", ExitDetail: "x", Logs: "x"}
	if err := t.Execute(io.Discard, sample); err != nil {
		return nil, fmt.Errorf("report template: %w", err)
	}
	return t, nil
}

// readRegular reads a regular file (FIFOs and directories are refused).
func readRegular(kind, path string) ([]byte, error) {
	f, err := openRegular(kind, path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", kind, path, err)
	}
	return data, nil
}

// newReport composes the report of a finished job.
func newReport(opts *RunOptions, loc string, tmpl *template.Template, location reportLocation, job *jobrun.Result) *report {
	r := &report{job: job}
	d := &r.ReportData
	d.Status = jobStatus(opts, job)
	d.Emoji, d.StatusWord = statusEmoji[d.Status], statusWord[d.Status]
	d.Job = jobName(opts)
	d.JobFull = quoteCommand(opts.Command)
	d.Host, d.WorkDir = location.host, location.workdir
	d.Start = locale.Format(job.Start, monday.Locale(loc))
	d.End = locale.Format(job.End, monday.Locale(loc))
	d.Duration = formatDuration(job.Duration())
	d.ExitCode = job.ExitCode
	if job.StartErr != nil {
		d.ExitDetail = "could not start: " + job.StartErr.Error()
	} else {
		d.ExitDetail = job.Signal
	}
	d.Tail = opts.Tail

	var logs []string
	for i, s := range []*jobrun.Stream{job.Stdout(), job.Stderr()} {
		label := [2]string{"stdout", "stderr"}[i]
		tail := cleanTail(s.Tail(opts.Tail))
		if i == 0 {
			d.StdoutTail, d.StdoutBytes, d.StdoutOmitted = tail, s.Total(), s.Omitted()
		} else {
			d.StderrTail, d.StderrBytes, d.StderrOmitted = tail, s.Total(), s.Omitted()
		}
		if opts.NoAttachLogs || s.Captured() == 0 {
			continue
		}
		name := fileSafe(d.Job) + "." + label + ".log"
		r.attachments = append(r.attachments, mailgun.File{
			Name: name, ContentType: "text/plain; charset=utf-8", Reader: newLogReader(s.ContentReader())})
		r.attached[i] = true
		logs = append(logs, name)
	}
	d.Logs = strings.Join(logs, ", ")

	subject := opts.Subject
	if subject == "" {
		subject = defaultRunSubject
	}
	var err error
	r.subject, err = expandSubject(subject, map[string]string{
		"emoji": d.Emoji, "job": d.Job, "jobfull": d.JobFull, "status": d.StatusWord,
		"duration": d.Duration, "exitcode": fmt.Sprint(d.ExitCode), "host": d.Host,
	})
	if err != nil {
		r.err = fmt.Errorf("subject template: %w", err)
		return r
	}
	var body strings.Builder
	if err := tmpl.Execute(&body, d); err != nil {
		r.err = fmt.Errorf("report template: %w", err)
	}
	r.markdown = body.String()
	return r
}

// logReader applies the attachment normalization used before task 34 without
// materializing a spilled job log: invalid UTF-8 bytes become U+FFFD and CRLF
// becomes LF. A lone CR is preserved.
type logReader struct {
	r         *bufio.Reader
	pending   [utf8.UTFMax]byte
	start, n  int
	pendingCR bool
	invalid   bool
	eof       bool
}

func newLogReader(r io.Reader) io.Reader {
	return &logReader{r: bufio.NewReaderSize(r, 32<<10)}
}

func (r *logReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	emit := func(b []byte) {
		n := copy(p[written:], b)
		written += n
		if n < len(b) {
			copy(r.pending[:], b[n:])
			r.start, r.n = 0, len(b)-n
		}
	}
	for written < len(p) {
		if r.start < r.n {
			n := copy(p[written:], r.pending[r.start:r.n])
			written += n
			r.start += n
			if r.start < r.n {
				break
			}
			r.start, r.n = 0, 0
			continue
		}
		if r.eof {
			if r.pendingCR {
				r.pendingCR = false
				emit([]byte{'\r'})
				continue
			}
			if written == 0 {
				return 0, io.EOF
			}
			break
		}
		rn, size, err := r.r.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.eof = true
				continue
			}
			if written > 0 {
				return written, nil
			}
			return 0, err
		}
		if r.pendingCR {
			r.pendingCR = false
			if rn == '\n' {
				emit([]byte{'\n'})
				continue
			}
			emit([]byte{'\r'})
			if written == len(p) || r.n > 0 {
				// The current rune still has to be handled next time.
				if err := r.r.UnreadRune(); err != nil {
					return written, err
				}
				continue
			}
		}
		if rn == utf8.RuneError && size == 1 {
			if r.invalid {
				continue
			}
			r.invalid = true
		} else {
			r.invalid = false
		}
		if rn == '\r' {
			r.pendingCR = true
			continue
		}
		var encoded [utf8.UTFMax]byte
		n := utf8.EncodeRune(encoded[:], rn)
		emit(encoded[:n])
	}
	return written, nil
}

type reportLocation struct{ host, workdir string }

// Resolve location before execution: a job may remove its working directory.
func loadReportLocation() (reportLocation, error) {
	host, err := os.Hostname()
	if err != nil {
		return reportLocation{}, fmt.Errorf("job hostname: %w", err)
	}
	workdir, err := os.Getwd()
	if err != nil {
		return reportLocation{}, fmt.Errorf("job working directory: %w", err)
	}
	return reportLocation{host: host, workdir: workdir}, nil
}

// jobStatus classifies the job's outcome.
func jobStatus(opts *RunOptions, job *jobrun.Result) string {
	switch {
	case job.StartErr != nil:
		return statusFailure
	case job.TimedOut:
		return statusTimeout
	case job.Interrupted:
		return statusInterrupted
	case opts.SuccessCodes[job.ExitCode]:
		return statusSuccess
	}
	return statusFailure
}

// jobName is --name, else the command's file name.
func jobName(opts *RunOptions) string {
	if opts.Name != "" {
		return opts.Name
	}
	return filepath.Base(opts.Command[0])
}

// fileSafe makes a job name usable as an attachment file name.
func fileSafe(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
	if strings.Trim(safe, "._") == "" {
		return "job"
	}
	return safe
}

// composeReport builds the report message: the Markdown body rendered
// into the layout template, plus the log attachments. Sender, recipients,
// and subject are validated as for send.
func composeReport(cfg *config.Config, opts *RunOptions, rep *report, rendering *preparedRendering) (*mailgun.Message, error) {
	if rep.err != nil {
		return nil, rep.err
	}
	sendOpts := opts.sendOptions(rep)
	msg, err := composePrepared(cfg, &sendOpts, strings.NewReader(""), rendering)
	if err != nil {
		return nil, err
	}
	msg.Attachments = rep.attachments
	return msg, nil
}

// sendOptions maps the run flags onto send options for the report: a
// Markdown body, the resolved subject, and the shared sending flags.
func (o *RunOptions) sendOptions(rep *report) SendOptions {
	s := SendOptions{
		To: o.To, Cc: o.Cc, Bcc: o.Bcc, From: o.From,
		Format:   "md",
		Template: o.Template, TemplateFile: o.TemplateFile,
		Preheader: o.Preheader, Footer: o.Footer,
		Test: o.Test, NoDefer: o.NoDefer, Timeout: o.Timeout,
		DedupKey: o.DedupKey, Cooldown: o.Cooldown,
		Config: o.Config, JSON: o.JSON,
		MessageSet: true,
	}
	if rep != nil {
		s.Subject, s.Message = rep.subject, rep.markdown
	}
	return s
}

// expandSubject substitutes the {placeholders} of a subject template.
// "{{" and "}}" produce literal braces. With vals == nil it only
// validates: an unknown placeholder or an unbalanced brace is an error.
func expandSubject(tpl string, vals map[string]string) (string, error) {
	known := map[string]bool{"emoji": true, "job": true, "jobfull": true, "status": true,
		"duration": true, "exitcode": true, "host": true}
	var out strings.Builder
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		switch {
		case c == '{' && i+1 < len(tpl) && tpl[i+1] == '{':
			out.WriteByte('{')
			i++
		case c == '}' && i+1 < len(tpl) && tpl[i+1] == '}':
			out.WriteByte('}')
			i++
		case c == '{':
			end := strings.IndexByte(tpl[i+1:], '}')
			if end < 0 {
				return "", fmt.Errorf("unclosed { at position %d (use {{ for a literal brace)", i+1)
			}
			name := tpl[i+1 : i+1+end]
			if !known[name] {
				return "", fmt.Errorf("unknown placeholder {%s}: use {emoji}, {job}, {jobfull}, {status}, {duration}, {exitcode}, or {host}", name)
			}
			out.WriteString(vals[name])
			i += end + 1
		case c == '}':
			return "", fmt.Errorf("unmatched } at position %d (use }} for a literal brace)", i+1)
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), nil
}

// validateSubjectTemplate checks a --subject template before the job
// runs.
func validateSubjectTemplate(tpl string) error {
	_, err := expandSubject(tpl, nil)
	return err
}

// formatDuration renders a duration as "850ms", "4s", "3m 12s",
// "2h 5m", or "1d 3h".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	day := 24 * time.Hour
	return fmt.Sprintf("%dd %dh", int(d/day), int(d%day/time.Hour))
}

// quoteCommand renders the command line for display, quoting arguments
// that need it: POSIX single quotes, on Windows double quotes.
func quoteCommand(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = quoteArg(a, runtime.GOOS == "windows")
	}
	return strings.Join(q, " ")
}

func quoteArg(a string, windows bool) string {
	if windows {
		// Backslashes are path separators on Windows, not escapes.
		if a != "" && !strings.ContainsAny(a, " \t\n\"") {
			return a
		}
		return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	if a != "" && !strings.ContainsAny(a, " \t\n\"'\\$`!*?[]{}()<>|&;#~%") {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// ansiRe matches terminal escape sequences: CSI (colors, cursor moves),
// OSC (titles, hyperlinks), and charset selections.
var ansiRe = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|[()][0-9A-Za-z])")

// cleanTail prepares raw tail lines for the report body.
func cleanTail(lines [][]byte) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = cleanLine(l)
	}
	return strings.Join(out, "\n")
}

// cleanLine replaces invalid UTF-8, removes ANSI escape sequences,
// collapses \r-overwritten progress output to its final state, and cuts
// the line at maxTailLine characters.
func cleanLine(raw []byte) string {
	s := strings.ToValidUTF8(string(raw), "\uFFFD")
	s = ansiRe.ReplaceAllString(s, "")
	if strings.Contains(s, "\r") {
		parts := strings.Split(s, "\r")
		s = ""
		for j := len(parts) - 1; j >= 0; j-- {
			if parts[j] != "" {
				s = parts[j]
				break
			}
		}
	}
	if utf8.RuneCountInString(s) > maxTailLine {
		s = string([]rune(s)[:maxTailLine]) + "…"
	}
	return s
}

// runResult is the JSON document of run: the send result plus the job.
type runResult struct {
	sendResult
	Job *runJobInfo `json:"job,omitempty"`
}

// runJobInfo describes the job in the run document.
type runJobInfo struct {
	Name       string        `json:"name"`
	Command    string        `json:"command"`
	Host       string        `json:"host"`
	WorkDir    string        `json:"workdir"`
	Status     string        `json:"status"`
	ExitCode   int           `json:"exit_code"`
	Detail     string        `json:"detail,omitempty"`
	StartedAt  string        `json:"started_at"`
	EndedAt    string        `json:"ended_at"`
	DurationMS int64         `json:"duration_ms"`
	Stdout     runStreamInfo `json:"stdout"`
	Stderr     runStreamInfo `json:"stderr"`
}

// runStreamInfo describes one output stream.
type runStreamInfo struct {
	Bytes    int64 `json:"bytes"`          // bytes the job wrote
	Captured int64 `json:"captured_bytes"` // bytes kept within the log cap
	Omitted  int64 `json:"omitted_bytes"`  // bytes the log cap dropped
	Attached bool  `json:"attached"`
}

// emitRunResult returns exit and, with --json, prints the run document.
// Unlike other commands, a nonzero exit code without an error is normal:
// it is the job's exit code, and the report may have been sent.
func emitRunResult(stdout io.Writer, opts *RunOptions, res *runResult, rep *report, exit int) int {
	if !opts.JSON {
		return exit
	}
	job := rep.job
	res.Job = &runJobInfo{
		Name: rep.Job, Command: rep.JobFull, Host: rep.Host, WorkDir: rep.WorkDir,
		Status: rep.Status, ExitCode: rep.ExitCode, Detail: rep.ExitDetail,
		StartedAt: rfc3339(job.Start), EndedAt: rfc3339(job.End),
		DurationMS: job.Duration().Milliseconds(),
	}
	for i, s := range []*jobrun.Stream{job.Stdout(), job.Stderr()} {
		info := runStreamInfo{Bytes: s.Total(), Captured: s.Captured(), Omitted: s.Omitted(), Attached: rep.attached[i]}
		if i == 0 {
			res.Job.Stdout = info
		} else {
			res.Job.Stderr = info
		}
	}
	e := res.env()
	e.SchemaVersion = jsonSchemaVersion
	e.ExitCode = exit
	if e.Outcome == "" {
		e.Outcome = "ok"
		if e.Error != nil {
			e.Outcome = "failed"
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		log.Printf("mailfire: writing command output: %v", err)
	}
	return exit
}
