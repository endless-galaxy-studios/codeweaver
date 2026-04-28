// Package log provides structured stderr logging for codeweaver.
//
// # Format selection matrix
//
// The format (JSON vs human) and color behavior are controlled by TTY detection
// and two environment variables. Precedence (high to low):
//
//	Environment override > TTY auto-detect
//
//	| Condition                          | Format  | Colors |
//	|------------------------------------|---------|--------|
//	| Non-TTY, no env override           | JSON    | none   |
//	| TTY, no env override               | human   | yes    |
//	| NO_COLOR=<any>                     | human   | no     |
//	| CLICOLOR_FORCE=1, non-TTY          | human   | yes    |
//	| CLICOLOR_FORCE=0                   | human   | no     |
//
// NO_COLOR: https://no-color.org/ — presence of the env var (any value) disables color.
// CLICOLOR_FORCE: https://bixense.com/clicolors/ — "1" forces color even on non-TTY.
//
// # Output streams
//
// ALL log output goes to stderr. Stdout is reserved exclusively for the JSON
// output schema. Any log line on stdout would break every consumer simultaneously.
//
// # Verbosity levels
//
// Default: emit error events; suppress info and debug events.
// --verbose: also emit info events (per-file file_parsed lines, etc.).
// --quiet: suppress all non-summary stderr output (summary always emitted).
// The two flags are mutually exclusive — passing both exits with code 2.
//
// # Summary line
//
// The RED-style summary is always the final stderr line on every successful or
// deadline-exceeded exit: {"event":"summary","files_parsed":N,"files_errored":M,
// "duration_ms":T,"symbols_out":S,"edges_out":E}. It is NOT suppressed by --quiet.
//
// # Trace context
//
// TRACEPARENT (W3C Trace Context: "00-{trace_id}-{parent_span_id}-{flags}") is
// captured from the environment and threaded through every JSON log line as
// trace_id and parent_span_id, enabling plugin-span + binary-span stitching in
// distributed traces. The binary makes NO outbound network calls.
package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Logger is the structured stderr logger. Create via New().
type Logger struct {
	w          io.Writer
	isTTY      bool
	quiet      bool
	verbose    bool
	noColor    bool
	traceID    string
	parentSpan string
}

// Fields is a map of additional structured key-value pairs for a log line.
type Fields map[string]interface{}

// New creates a Logger writing to the given writer.
//
// isTTY controls the default output format:
//   - true: human-readable lines (respects noColor / CLICOLOR_FORCE)
//   - false: JSON lines (one object per line)
//
// quiet suppresses all non-summary output.
// verbose enables per-file info events (file_parsed lines, etc.).
//
// Both TRACEPARENT parsing and NO_COLOR/CLICOLOR_FORCE detection are done
// at construction time from the process environment. See the package-level
// format matrix in the package comment for the full precedence rules.
//
// Note: TTY detection uses os.Stat(stderr.Fd()) + ModeCharDevice rather than
// golang.org/x/term.IsTerminal. The stdlib approach is pure-Go, avoids an
// additional dependency, and is sufficient for the binary's use case (detecting
// whether stderr is a terminal vs a pipe). The result is identical on macOS,
// Linux, and Windows for the subprocess-vs-TTY distinction this binary cares about.
func New(w io.Writer, isTTY, quiet, verbose bool) *Logger {
	l := &Logger{
		w:       w,
		isTTY:   isTTY,
		quiet:   quiet,
		verbose: verbose,
	}

	// NO_COLOR: https://no-color.org/
	// Presence of NO_COLOR with any value disables ANSI escape sequences.
	// CLICOLOR_FORCE: https://bixense.com/clicolors/
	// CLICOLOR_FORCE=1 enables color even on non-TTY stderr.
	// CLICOLOR_FORCE=0 disables color even on TTY stderr.
	noColorEnv := os.Getenv("NO_COLOR")
	clicolorForce := os.Getenv("CLICOLOR_FORCE")

	switch {
	case noColorEnv != "":
		// NO_COLOR wins over everything: unconditionally disable color.
		l.noColor = true
	case clicolorForce == "1":
		// Force-enable color on non-TTY (e.g., piped output but user wants color).
		// isTTY is set to true so emitHuman is used; noColor remains false.
		l.isTTY = true
	case clicolorForce == "0":
		// Force-disable color even on TTY.
		l.noColor = true
	}

	// TRACEPARENT: W3C Trace Context format "00-{trace_id}-{parent_span_id}-{flags}"
	// We extract trace_id and parent_span_id for structured log inclusion.
	// The binary makes no outbound network calls — this is read-only context propagation.
	if tp := os.Getenv("TRACEPARENT"); tp != "" {
		parts := strings.Split(tp, "-")
		// Minimum valid traceparent: version(2)-trace_id(32)-parent_span_id(16)-flags(2)
		if len(parts) >= 4 {
			l.traceID = parts[1]
			l.parentSpan = parts[2]
		}
	}

	return l
}

// NewFromEnv creates a Logger detecting TTY status of os.Stderr automatically.
// This is the standard constructor for production use.
func NewFromEnv(quiet, verbose bool) *Logger {
	return New(os.Stderr, isattyCheck(os.Stderr), quiet, verbose)
}

// isattyCheck returns true if f is a character device (i.e., a TTY).
// Uses os.Stat on the file descriptor — no CGo, no third-party deps.
func isattyCheck(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// Info emits an INFO-level log line with optional fields.
func (l *Logger) Info(event string, fields Fields) {
	l.emit("info", event, fields)
}

// Warn emits a WARN-level log line with optional fields.
func (l *Logger) Warn(event string, fields Fields) {
	l.emit("warn", event, fields)
}

// Error emits an ERROR-level log line with optional fields.
func (l *Logger) Error(event string, fields Fields) {
	l.emit("error", event, fields)
}

// Debug emits a DEBUG-level log line only when --verbose is set.
func (l *Logger) Debug(event string, fields Fields) {
	if !l.verbose {
		return
	}
	l.emit("debug", event, fields)
}

// Summary emits the final RED-style summary line. Called at exit.
// Always emitted on exit (not suppressed by --quiet) so consumers can extract metrics.
// Format: {"event":"summary","files_parsed":N,"files_errored":M,"duration_ms":T,"symbols_out":S,"edges_out":E}
func (l *Logger) Summary(filesParsed, filesErrored, durationMS, symbolsOut, edgesOut int) {
	// Summary is written even in quiet mode — it is the primary telemetry signal.
	fields := Fields{
		"files_parsed":  filesParsed,
		"files_errored": filesErrored,
		"duration_ms":   durationMS,
		"symbols_out":   symbolsOut,
		"edges_out":     edgesOut,
	}
	// Temporarily bypass quiet for summary
	wasQuiet := l.quiet
	l.quiet = false
	l.emit("info", "summary", fields)
	l.quiet = wasQuiet
}

// emit is the internal log emission function.
//
// Concurrency: a single Logger may be used from multiple goroutines provided
// the underlying writer is os.Stderr in pipe mode (non-TTY). POSIX write(2)
// for payloads under PIPE_BUF (4096 bytes on Linux, 512 bytes on macOS) is
// atomic for pipes, so individual JSON log lines do not interleave when
// stderr is piped. The watchdog goroutine in cli/parse.go relies on this
// guarantee; all log lines it emits are well under 512 bytes (verified:
// deadline_warning ~238 bytes, file_parsed ~461 bytes worst case).
//
// When stderr is a TTY (character device), POSIX provides no atomicity
// guarantee — concurrent writes may interleave cosmetically. This is
// acceptable for the single deadline_warning line that can fire concurrently
// with main-goroutine output; visible interleaving is rare and harmless.
//
// If you switch to a non-os.File writer (e.g., a wrapped io.Writer or a
// buffered writer), wrap it with sync.Mutex first.
func (l *Logger) emit(level, event string, fields Fields) {
	if l.quiet {
		return
	}

	now := time.Now().UTC()

	if l.isTTY {
		l.emitHuman(now, level, event, fields)
	} else {
		l.emitJSON(now, level, event, fields)
	}
}

// emitJSON writes a single JSON log line to stderr.
// Format: {"ts":"...","level":"...","event":"...", ...fields, "trace_id":"...", "parent_span_id":"..."}
func (l *Logger) emitJSON(now time.Time, level, event string, fields Fields) {
	obj := make(map[string]interface{}, len(fields)+5)
	obj["ts"] = now.Format(time.RFC3339)
	obj["level"] = level
	obj["event"] = event
	for k, v := range fields {
		obj[k] = v
	}
	// Thread trace context into every structured log line when present.
	if l.traceID != "" {
		obj["trace_id"] = l.traceID
	}
	if l.parentSpan != "" {
		obj["parent_span_id"] = l.parentSpan
	}

	data, err := json.Marshal(obj)
	if err != nil {
		// Fallback: emit minimal error JSON
		fmt.Fprintf(l.w, `{"level":"error","event":"log_marshal_failed","error":%q}`+"\n", err.Error())
		return
	}
	fmt.Fprintf(l.w, "%s\n", data)
}

// emitHuman writes a human-readable log line to stderr.
// Format: LEVEL  event key=value key=value
// Colors: INFO=cyan, WARN=yellow, ERROR=red, DEBUG=gray (when noColor is false).
func (l *Logger) emitHuman(now time.Time, level, event string, fields Fields) {
	ts := now.Format("15:04:05")
	levelStr := strings.ToUpper(level)

	var line strings.Builder
	if !l.noColor {
		// ANSI colors on TTY stderr only. Never on stdout.
		switch level {
		case "info":
			line.WriteString("\033[36m") // cyan
		case "warn":
			line.WriteString("\033[33m") // yellow
		case "error":
			line.WriteString("\033[31m") // red
		case "debug":
			line.WriteString("\033[90m") // gray
		}
	}

	fmt.Fprintf(&line, "%-5s %s  %s", levelStr, ts, event)

	// Append trace context if present
	if l.traceID != "" {
		fmt.Fprintf(&line, "  trace_id=%s", l.traceID)
	}
	if l.parentSpan != "" {
		fmt.Fprintf(&line, "  parent_span_id=%s", l.parentSpan)
	}

	for k, v := range fields {
		fmt.Fprintf(&line, "  %s=%v", k, v)
	}

	if !l.noColor {
		line.WriteString("\033[0m") // reset
	}

	fmt.Fprintln(l.w, line.String())
	_ = ts // suppress unused warning if we ever drop the timestamp format
}

// TraceID returns the extracted trace ID from TRACEPARENT, or "" if not set.
func (l *Logger) TraceID() string { return l.traceID }

// ParentSpanID returns the extracted parent span ID from TRACEPARENT, or "" if not set.
func (l *Logger) ParentSpanID() string { return l.parentSpan }

// IsVerbose returns true if verbose mode is enabled.
func (l *Logger) IsVerbose() bool { return l.verbose }

// IsQuiet returns true if quiet mode is enabled.
func (l *Logger) IsQuiet() bool { return l.quiet }
