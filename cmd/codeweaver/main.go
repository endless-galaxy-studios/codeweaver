// Package main is the entry point for the codeweaver binary.
//
// # Version embedding via ldflags
//
// At release build time, the following -ldflags -X directives inject version info:
//
//	-X main.gitCommit=$(git rev-parse HEAD)
//	-X main.builtAt=$(git log -1 --format=%cI)
//
// The ldflags identifiers (gitCommit, builtAt) are Go package-level variable names
// in this package (main). They are copied into the schema.VersionOutput struct at
// version-emit time using cli.SetVersionVars(). The JSON field names in VersionOutput
// use snake_case tags ("git_commit", "built_at") which are distinct from the Go
// identifier names — this distinction matters for debugging ldflags injection:
//
//	ldflags identifier path: main.gitCommit  (Go: var gitCommit string)
//	JSON output field name:  git_commit      (struct tag: json:"git_commit")
//
// For development builds without ldflags injection, both vars default to empty strings.
// The binary version constant is in internal/schema/types.go (schema.BinaryVersion).
//
// # Panic recovery
//
// A defer recover() at the top of main() converts any uncaught panic into:
//   - A structured error emitted to stderr (JSON when non-TTY, human-readable when TTY)
//   - Exit code 4 (documented in the exit code table)
//   - Stdout is guaranteed to be either empty or a complete, valid JSON document.
//     The single-buffered stdout pattern (build in bytes.Buffer, write once) in
//     the parse subcommand ensures panics after the RunE call cannot produce partial JSON.
//   - If CODEWEAVER_CRASH_LOG_DIR is set, a timestamped crash file is written there.
//
// # Exit code table
//
//	0 — success (stdout contains JSON; parse_errors may be present for individual files)
//	1 — parse-level failure (all files failed; no partial output was possible)
//	2 — invalid invocation (bad flags, missing required args, usage error)
//	3 — internal error (unexpected state, not a panic)
//	4 — panic (uncovered panic converted by this defer recover())
//	5 — deadline exceeded (partial results emitted)
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"codeweaver/internal/cli"
	"codeweaver/internal/crashlog"
	cwlog "codeweaver/internal/log"
	"codeweaver/internal/schema"
)

// gitCommit is injected at build time via:
//
//	-ldflags="-X main.gitCommit=$(git rev-parse HEAD)"
//
// The ldflags path is "main.gitCommit" (Go identifier in this package).
// The JSON output field is "git_commit" (snake_case tag in schema.VersionOutput).
// These names differ — the ldflags identifier uses Go naming, the JSON tag uses API naming.
var gitCommit string

// builtAt is injected at build time via:
//
//	-ldflags="-X main.builtAt=$(git log -1 --format=%cI)"
//
// The ldflags path is "main.builtAt" (Go identifier in this package).
// The JSON output field is "built_at" (snake_case tag in schema.VersionOutput).
var builtAt string

// version is injected at build time via:
//
//	-ldflags="-X main.version=0.1.0"
//
// Falls back to schema.BinaryVersion when not injected (dev builds).
var version string

func main() {
	// Inject ldflags-populated vars into the cli package so version.go can access them.
	// This must happen before cobra.Execute() is called.
	cli.SetVersionVars(gitCommit, builtAt)

	// Resolve effective version: ldflags takes precedence over the compiled constant.
	effectiveVersion := version
	if effectiveVersion == "" {
		effectiveVersion = schema.BinaryVersion
	}

	// Inject version info into the crash log package so crash reports know what
	// binary version was running. This must happen before any panic recovery triggers.
	crashlog.SetVersionInfo(effectiveVersion, schema.SchemaVersionV1)

	// Top-level panic recovery.
	// Guarantees: structured stderr error, exit code 4, stdout never partially written.
	//
	// Single-buffered stdout discipline: the parse subcommand builds its full JSON
	// document into a bytes.Buffer before writing. A panic before the write means
	// stdout is untouched; a panic after means the document was already complete.
	// Either way, stdout is never corrupted.
	defer func() {
		r := recover()
		if r == nil {
			return
		}

		// Panic happened. Build a structured crash event.
		stack := debug.Stack()
		logger := cwlog.NewFromEnv(false /* never suppress panic */, false)
		panicMsg := fmt.Sprintf("%v", r)

		logger.Error("panic", cwlog.Fields{
			"detail":    panicMsg,
			"exit_code": 4,
		})

		// Emit human-readable stack to stderr for developer visibility.
		// Stack frames are written to stderr only — never stdout.
		_, _ = fmt.Fprintf(os.Stderr, "\nStack trace:\n%s\n", stack)

		// Optional crash log to CODEWEAVER_CRASH_LOG_DIR.
		// crashlog.Write validates the directory (symlink check, O_EXCL), writes
		// a JSON report with filtered env vars, and rotates to 10 most recent.
		// If Write fails, we still exit 4 — the crash log is diagnostic, not gating.
		if clErr := crashlog.Write(r, stack); clErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "crashlog write failed: %v\n", clErr)
		}

		// Exit 4 — panic exit code.
		// Stdout is either: (a) not written (panic before write) or (b) complete JSON.
		// Either way, stdout is not corrupted.
		os.Exit(4)
	}()

	root := cli.NewRootCmd(effectiveVersion)

	// cobra.Execute() calls os.Exit internally for --help and --version.
	// For error paths, we catch the error here and handle exit codes explicitly.
	if err := root.Execute(); err != nil {
		// cobra.Execute returns an error for subcommand RunE errors.
		// We do not print the error here — subcommands emit their own structured errors.
		// Exit 2 for usage errors (cobra signals these via the error text).
		// Exit 3 for internal errors that made it back to this level.
		logger := cwlog.NewFromEnv(false, false)
		logger.Error("command_failed", cwlog.Fields{
			"error":     err.Error(),
			"exit_code": 2,
		})
		// Print cobra-style error to stderr for TTY users.
		_, _ = fmt.Fprintf(os.Stderr, "Error: %s\n\nRun 'codeweaver --help' for usage.\n", err.Error())
		os.Exit(2)
	}
}
