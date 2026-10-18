package cli

import (
	"encoding/json"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/thorstenkramm/mailfire/internal/deferral"
	"github.com/thorstenkramm/mailfire/internal/mailgun"
)

// JSON output (task 27): with --json, a command prints exactly one JSON
// document on stdout instead of its human-readable output. Diagnostics
// and warnings stay on stderr, exit codes are unchanged, and help is
// always text. The schema is documented in docs/json-output.md; bump
// jsonSchemaVersion on incompatible changes only.
const jsonSchemaVersion = 1

// Stable error codes of the "error" object. Messages may change; codes
// may not.
const (
	errUsage       = "usage_error"
	errUnknownID   = "unknown_id"
	errConfig      = "config_error"
	errAuth        = "auth_failed"
	errTemporary   = "temporary_failure"
	errRejected    = "rejected"
	errSendFailed  = "send_failed"
	errDeferFailed = "defer_failed"
	errDedup       = "dedup_error"
	errQueue       = "queue_error"
	errExport      = "export_failed"
)

type jsonError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newJSONError(code string, err error) *jsonError {
	return &jsonError{Code: code, Message: err.Error()}
}

// envelope holds the fields every document has. Command-specific result
// types embed it; a failure before the command produced data is reported
// with the envelope alone.
type envelope struct {
	SchemaVersion int        `json:"schema_version"`
	Command       string     `json:"command"`
	Outcome       string     `json:"outcome"`
	ExitCode      int        `json:"exit_code"`
	Error         *jsonError `json:"error,omitempty"`
}

func (e *envelope) env() *envelope { return e }

func (e *envelope) setError(code string, err error) { e.Error = newJSONError(code, err) }

type jsonResult interface{ env() *envelope }

// emitJSON completes the common fields of r and writes it to stdout as
// one line. An empty outcome becomes "ok" or "failed" by exit code; a
// failure without a recorded error gets the exit code's generic error.
func emitJSON(stdout io.Writer, r jsonResult, exit int) int {
	e := r.env()
	e.SchemaVersion = jsonSchemaVersion
	e.ExitCode = exit
	if e.Outcome == "" {
		e.Outcome = "ok"
		if exit != ExitOK {
			e.Outcome = "failed"
		}
	}
	if exit != ExitOK && e.Error == nil {
		e.Error = &jsonError{Code: exitErrorCode(exit), Message: "command failed; see stderr for details"}
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		log.Printf("mailfire: writing command output: %v", err)
	}
	return exit
}

// jsonFail reports a failure as an envelope-only document when on is
// set, and returns exit either way.
func jsonFail(stdout io.Writer, on bool, command, code string, err error, exit int) int {
	if !on {
		return exit
	}
	return emitJSON(stdout, &envelope{Command: command, Error: newJSONError(code, err)}, exit)
}

// exitErrorCode is the generic error code of an exit code.
func exitErrorCode(exit int) string {
	switch exit {
	case ExitUsage:
		return errUsage
	case ExitConfig:
		return errConfig
	case ExitAuth:
		return errAuth
	case ExitDeferred:
		return errTemporary
	default:
		return errSendFailed
	}
}

// sendErrorCode classifies a Mailgun client error.
func sendErrorCode(err error) string {
	switch {
	case mailgun.IsAuth(err):
		return errAuth
	case mailgun.IsDeferrable(err):
		return errTemporary
	}
	if merr, ok := mailgun.As(err); ok && merr.StatusCode > 0 {
		return errRejected
	}
	return errSendFailed
}

// jsonRequested reports whether args ask for JSON output before any "--"
// terminator. It decides the output mode for an unknown subcommand, whose
// flags (and hence their values) are unknown. Known commands read the
// mode with their own flag set (scanJSON).
func jsonRequested(args []string) bool {
	on := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if !isJSONFlag(a) {
			continue
		}
		_, val, hasVal := strings.Cut(a, "=")
		b, err := strconv.ParseBool(val)
		on = !hasVal || (err == nil && b)
	}
	return on
}

// isJSONFlag reports whether a is a -json or --json flag, with or
// without a value.
func isJSONFlag(a string) bool {
	name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
	return strings.HasPrefix(a, "-") && name == "json"
}

// humanOut is where human-readable output goes: nowhere in JSON mode.
func humanOut(stdout io.Writer, jsonMode bool) io.Writer {
	if jsonMode {
		return io.Discard
	}
	return stdout
}

// rfc3339 formats t in UTC; the zero time yields "" (omitted field).
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// sendResult is the document of send.
type sendResult struct {
	envelope
	TestMode   bool      `json:"test_mode"`
	MessageID  string    `json:"message_id,omitempty"`
	DeferredID string    `json:"deferred_id,omitempty"`
	Skip       *skipInfo `json:"skip,omitempty"`
}

// skipInfo describes a send skipped by notification deduplication.
type skipInfo struct {
	Reason     string `json:"reason"` // "cooldown", "queued", or "in_doubt"
	DedupKey   string `json:"dedup_key"`
	QueuedID   string `json:"queued_id,omitempty"`
	Since      string `json:"since,omitempty"`
	EligibleAt string `json:"eligible_at,omitempty"`
}

// resendResult is the document of resend (without --list).
type resendResult struct {
	envelope
	Messages []resendMessage `json:"messages"`
	Summary  resendSummary   `json:"summary"`
}

// resendMessage is the outcome of one message in a resend run.
type resendMessage struct {
	ID        string     `json:"id"`
	Outcome   string     `json:"outcome"`          // "sent", "skipped", "failed", or "expired"
	Reason    string     `json:"reason,omitempty"` // skipped: "not_eligible", "permanently_failed", "gone"
	MessageID string     `json:"message_id,omitempty"`
	NextRetry string     `json:"next_retry,omitempty"`
	Warning   string     `json:"warning,omitempty"`
	Error     *jsonError `json:"error,omitempty"`
}

type resendSummary struct {
	Sent    int `json:"sent"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	Expired int `json:"expired"`
}

// queuedMessage is one deferred message in resend --list, queue list,
// and queue show. It never includes the message content.
type queuedMessage struct {
	ID          string   `json:"id"`
	DeferredAt  string   `json:"deferred_at"`
	To          []string `json:"to"`
	Subject     string   `json:"subject"`
	Size        int64    `json:"size"`
	Attempts    int      `json:"attempts"`
	LastAttempt string   `json:"last_attempt,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
	Status      string   `json:"status"` // "pending", "failed", or "expired"
	NextRetry   string   `json:"next_retry,omitempty"`
}

func newQueuedMessage(store *deferral.Store, m deferral.Metadata, cooldown time.Duration, now time.Time) queuedMessage {
	q := queuedMessage{
		ID:          m.ID,
		DeferredAt:  rfc3339(m.Timestamp),
		To:          append([]string{}, m.To...),
		Subject:     m.Subject,
		Size:        m.Size,
		Attempts:    m.Attempts,
		LastAttempt: rfc3339(m.LastAttempt),
		LastError:   m.LastError,
		Status:      "pending",
	}
	switch {
	case m.Failed:
		q.Status = "failed"
	case store.Expired(m, now):
		q.Status = "expired"
	default:
		q.NextRetry = rfc3339(m.NextRetry(cooldown))
	}
	return q
}

// listResult is the document of resend --list and queue list.
type listResult struct {
	envelope
	Messages []queuedMessage `json:"messages"`
}

func emitList(stdout io.Writer, command string, store *deferral.Store, msgs []deferral.Metadata,
	cooldown time.Duration, now time.Time) int {
	res := &listResult{envelope: envelope{Command: command}, Messages: make([]queuedMessage, 0, len(msgs))}
	for _, m := range msgs {
		res.Messages = append(res.Messages, newQueuedMessage(store, m, cooldown, now))
	}
	return emitJSON(stdout, res, ExitOK)
}
