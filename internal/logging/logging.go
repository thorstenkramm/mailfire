// Package logging implements mailfire's optional JSONL send log (PRD FR-3).
//
// Logging is disabled unless [logging].logfile is set in the configuration.
// When enabled, every sending attempt (success or failure, any format,
// including --test sends) produces exactly one JSON object per line. The
// field set depends on the configured loglevel:
//
//	error:       timestamp, message_id, to, status — failures only
//	information: timestamp, message_id, to, subject, status
//	debug:       timestamp, message_id, from, to, subject, size, status, response
//
// timestamp is RFC3339 in UTC. message_id is always present; it is the
// empty string when Mailgun returned no ID (failures). status is the HTTP
// status code, or 0 when no response was received (transport error).
// response (debug only) is the redacted API response body on success or
// the redacted error message on failure. Attempts made with --test carry
// an extra "test": true field at every level; the field is omitted
// otherwise.
//
// Truncation: after each append, if the file exceeds max_size, the oldest
// whole lines are dropped in place; the newest line is never dropped, even
// if it alone exceeds max_size.
//
// Concurrency: a Logger is safe for concurrent use within one process, and
// appends use O_APPEND, so lines from concurrent mailfire processes do not
// interleave. Truncation, however, is not coordinated across processes:
// the logfile assumes a single truncating writer.
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level names as accepted by the configuration (PRD FR-2).
const (
	LevelError       = "error"
	LevelInformation = "information"
	LevelDebug       = "debug"
)

// Attempt describes one sending attempt to log. The Response field must
// already be redacted by the caller — the API key must never reach the
// logfile (PRD FR-2).
type Attempt struct {
	Time      time.Time // stamped as RFC3339 in UTC
	MessageID string    // Mailgun message ID; "" when none was returned (failures)
	From      string    // sender (debug level only)
	To        []string  // recipients
	Subject   string    // (information and debug levels)
	Size      int64     // encoded request size in bytes; 0 when encoding failed (debug level only)
	Status    int       // HTTP status code; 0 when no response was received
	Response  string    // redacted API response body / error message (debug level only)
	Success   bool      // whether the send succeeded
	Test      bool      // --test send (o:testmode=yes)
}

// errorEntry is the "error" level field set (PRD FR-3).
type errorEntry struct {
	Timestamp string   `json:"timestamp"`
	MessageID string   `json:"message_id"`
	To        []string `json:"to"`
	Status    int      `json:"status"`
	Test      bool     `json:"test,omitempty"`
}

// informationEntry is the "information" level field set (PRD FR-3).
type informationEntry struct {
	Timestamp string   `json:"timestamp"`
	MessageID string   `json:"message_id"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Status    int      `json:"status"`
	Test      bool     `json:"test,omitempty"`
}

// debugEntry is the "debug" level field set (PRD FR-3).
type debugEntry struct {
	Timestamp string   `json:"timestamp"`
	MessageID string   `json:"message_id"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Size      int64    `json:"size"`
	Status    int      `json:"status"`
	Response  string   `json:"response"`
	Test      bool     `json:"test,omitempty"`
}

// Logger appends JSONL entries to a logfile and truncates it in place when
// it exceeds the configured maximum size.
type Logger struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	level   string
	maxSize int64
}

// Open opens (creating if necessary) the logfile for appending. Missing
// parent directories are created. The log level must be one of LevelError,
// LevelInformation, LevelDebug; the config loader already enforces this, so
// an invalid level here indicates a bug. An unusable path (bad directory,
// permissions) is an error, reported at startup before any send attempt.
func Open(path, level string, maxSize int64) (*Logger, error) {
	switch level {
	case LevelError, LevelInformation, LevelDebug:
	default:
		return nil, fmt.Errorf("logging: invalid loglevel %q: must be %q, %q, or %q",
			level, LevelError, LevelInformation, LevelDebug)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("logging: cannot create log directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("logging: cannot open logfile %s: %w", path, err)
	}
	return &Logger{f: f, path: path, level: level, maxSize: maxSize}, nil
}

// Close closes the logfile.
func (l *Logger) Close() error {
	return l.f.Close()
}

// Log writes one JSON line for the attempt. At the error level, successful
// attempts are not logged. After appending, the file is truncated in place
// when it exceeds the configured maximum size.
func (l *Logger) Log(a Attempt) error {
	if l.level == LevelError && a.Success {
		return nil
	}
	line, err := l.marshal(a)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("logging: writing %s: %w", l.path, err)
	}
	return l.truncateLocked()
}

// marshal encodes the attempt with the field set of the configured level.
func (l *Logger) marshal(a Attempt) ([]byte, error) {
	ts := a.Time.UTC().Format(time.RFC3339)
	switch l.level {
	case LevelError:
		return json.Marshal(errorEntry{
			Timestamp: ts, MessageID: a.MessageID, To: a.To,
			Status: a.Status, Test: a.Test,
		})
	case LevelDebug:
		return json.Marshal(debugEntry{
			Timestamp: ts, MessageID: a.MessageID, From: a.From, To: a.To,
			Subject: a.Subject, Size: a.Size, Status: a.Status,
			Response: a.Response, Test: a.Test,
		})
	default: // LevelInformation
		return json.Marshal(informationEntry{
			Timestamp: ts, MessageID: a.MessageID, To: a.To,
			Subject: a.Subject, Status: a.Status, Test: a.Test,
		})
	}
}

// truncateLocked drops the oldest whole lines while the file exceeds
// maxSize. The file is rewritten in place (truncate to zero, then write
// the surviving tail); with O_APPEND the rewritten content lands at the
// start of the now-empty file.
func (l *Logger) truncateLocked() error {
	fi, err := l.f.Stat()
	if err != nil {
		return fmt.Errorf("logging: stat %s: %w", l.path, err)
	}
	if fi.Size() <= l.maxSize {
		return nil
	}
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("logging: seeking %s: %w", l.path, err)
	}
	data, err := io.ReadAll(l.f)
	if err != nil {
		return fmt.Errorf("logging: reading %s: %w", l.path, err)
	}
	keep := keepNewest(data, l.maxSize)
	if err := l.f.Truncate(0); err != nil {
		return fmt.Errorf("logging: truncating %s: %w", l.path, err)
	}
	if _, err := l.f.Write(keep); err != nil {
		return fmt.Errorf("logging: rewriting %s: %w", l.path, err)
	}
	return nil
}

// keepNewest returns the tail of data consisting of whole lines with a
// total size of at most maxSize. The final line is always kept, even if it
// alone exceeds maxSize: the newest entry is never dropped.
func keepNewest(data []byte, maxSize int64) []byte {
	start := 0
	for int64(len(data)-start) > maxSize {
		nl := bytes.IndexByte(data[start:], '\n')
		if nl < 0 || start+nl+1 >= len(data) {
			break // final line: never dropped, even if oversized
		}
		start += nl + 1
	}
	return data[start:]
}
