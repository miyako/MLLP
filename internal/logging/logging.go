// Package logging provides structured diagnostic logging to stderr.
//
// Rules:
//   - All output goes to stderr only. stdout is reserved for NDJSON protocol messages.
//   - PHI must never appear in log output. No raw HL7 payloads, no patient names,
//     no message contents. Only metadata: timestamps, IDs, counts, error codes.
//   - Each line is a JSON object for machine parseability.
//   - The "level" field is one of "debug", "info", "warn", "error".
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level represents the severity of a log entry.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Logger writes structured JSON log lines to an io.Writer (normally stderr).
// It is safe for concurrent use.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
	min Level
}

// New returns a Logger writing to w at minimum level min.
// Pass LevelInfo for production use.
func New(w io.Writer, min Level) *Logger {
	return &Logger{out: w, min: min}
}

// Default returns a Logger writing to stderr at LevelInfo.
func Default() *Logger {
	return New(os.Stderr, LevelInfo)
}

// entry is one log line.
type entry struct {
	Time    string         `json:"time"`
	Level   Level          `json:"level"`
	Message string         `json:"msg"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func levelRank(l Level) int {
	switch l {
	case LevelDebug:
		return 0
	case LevelInfo:
		return 1
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	}
	return 0
}

// Log emits a log entry if its level meets the minimum threshold.
// fields is an optional key-value map for structured context.
func (lg *Logger) Log(level Level, msg string, fields map[string]any) {
	if levelRank(level) < levelRank(lg.min) {
		return
	}
	e := entry{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Level:   level,
		Message: msg,
		Fields:  fields,
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	_, _ = fmt.Fprintf(lg.out, "%s\n", b)
}

// Info logs at INFO level.
func (lg *Logger) Info(msg string, fields map[string]any) {
	lg.Log(LevelInfo, msg, fields)
}

// Warn logs at WARN level.
func (lg *Logger) Warn(msg string, fields map[string]any) {
	lg.Log(LevelWarn, msg, fields)
}

// Error logs at ERROR level.
func (lg *Logger) Error(msg string, fields map[string]any) {
	lg.Log(LevelError, msg, fields)
}

// Debug logs at DEBUG level.
func (lg *Logger) Debug(msg string, fields map[string]any) {
	lg.Log(LevelDebug, msg, fields)
}
