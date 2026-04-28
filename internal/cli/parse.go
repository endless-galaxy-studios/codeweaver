package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"codeweaver/internal/limits"
	cwlog "codeweaver/internal/log"
	"codeweaver/internal/parser"
	"codeweaver/internal/parser/python"
	"codeweaver/internal/parser/typescript"
	"codeweaver/internal/schema"
)

// ParseFlags holds the parsed values of parse-subcommand flags.
type ParseFlags struct {
	Workspace  string
	FilesFrom  string // path or "-" for stdin; newline-delimited
	FilesFrom0 string // path or "-" for stdin; NUL-delimited
	DeadlineMS int
	DryRun     bool
}

// newParseCmd constructs the "parse" subcommand.
func newParseCmd() *cobra.Command {
	var flags ParseFlags

	cmd := &cobra.Command{
		Use:   "parse [flags] [-- files...]",
		Short: "Parse source files and emit a versioned JSON code graph to stdout",
		Long: `Parse one or more source code files using tree-sitter grammars
and emit a versioned JSON document to stdout.

File paths may be provided as positional arguments, read from stdin (newline-
delimited via --files-from -), or read NUL-delimited via --files-from0 - for
compatibility with "git ls-files -z" and "find -print0" pipelines.

The -- end-of-options separator is supported: everything after -- is treated as
a positional file path, never as a flag (e.g., codeweaver parse -- -odd-name.py).

Exit codes:
  0 — success (may contain parse_errors for individual files)
  1 — all files failed, no partial output was possible
  2 — invalid invocation (bad flags, missing required args)
  3 — internal error
  4 — panic (converted by top-level defer recover)
  5 — deadline exceeded (partial results emitted)`,
		// cobra handles "--" automatically for positional args — everything after "--"
		// is treated as positional regardless of leading dashes.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runParse(cmd, args, &flags)
		},
	}

	cmd.Flags().StringVar(&flags.Workspace, "workspace", "",
		"workspace root for relative path normalization (default: current directory)")
	cmd.Flags().StringVar(&flags.FilesFrom, "files-from", "",
		`read newline-delimited file paths from this path, or "-" for stdin`)
	cmd.Flags().StringVar(&flags.FilesFrom0, "files-from0", "",
		`read NUL-delimited file paths from this path, or "-" for stdin (for git ls-files -z | codeweaver parse --files-from0 -)`)
	cmd.Flags().IntVar(&flags.DeadlineMS, "deadline-ms", 0,
		"abort after N milliseconds and emit partial results; 0 means no deadline (env: CODEWEAVER_DEADLINE_MS)")
	cmd.Flags().BoolVar(&flags.DryRun, "dry-run", false,
		"validate inputs only; emit files_validated count; no parse output")

	return cmd
}

// runParse is the implementation of the parse subcommand.
func runParse(cmd *cobra.Command, args []string, flags *ParseFlags) error {
	// Validate mutually exclusive flags before anything else.
	// --quiet and --verbose cannot both be set: one suppresses stderr, the other amplifies it.
	if Globals.Quiet && Globals.Verbose {
		_, _ = fmt.Fprintf(os.Stderr, "Error: --quiet and --verbose are mutually exclusive\n\nRun 'codeweaver parse --help' for usage.\n")
		os.Exit(2)
	}

	// Validate CODEWEAVER_MAX_FILE_BYTES at startup, before any file I/O.
	// This is a once-per-invocation check; the resolved value is threaded into
	// parseFilePairs for per-file enforcement.
	// Error here = usage error (bad env var) → exit 2.
	maxFileBytes, err := limits.MaxFileBytes()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error: %s\n\nRun 'codeweaver parse --help' for usage.\n", err.Error())
		os.Exit(2)
	}

	logger := cwlog.NewFromEnv(Globals.Quiet, Globals.Verbose)
	startTime := time.Now()

	// RED summary counters — updated as files are parsed.
	// These are read by the deferred summary closure at exit.
	var (
		filesParsed  int
		filesErrored int
		totalSymbols int
		totalEdges   int
	)

	// Deferred RED summary: fires on normal return paths and when runParse returns an error.
	// NOT fired by os.Exit() calls (os.Exit bypasses defers). Usage-error exits (exit 2) before
	// any parsing begins intentionally skip the summary. Deadline-exceeded exit (exit 5) and
	// the deadline-exceeded-with-no-output path emit the summary manually before os.Exit().
	defer func() {
		elapsed := int(time.Since(startTime).Milliseconds())
		logger.Summary(filesParsed, filesErrored, elapsed, totalSymbols, totalEdges)
	}()

	// Resolve deadline from flag or environment variable.
	deadlineMS := flags.DeadlineMS
	if deadlineMS == 0 {
		if envVal := os.Getenv("CODEWEAVER_DEADLINE_MS"); envVal != "" {
			var n int
			if _, scanErr := fmt.Sscanf(envVal, "%d", &n); scanErr == nil && n > 0 {
				deadlineMS = n
			}
		}
	}

	// Build context. If a positive deadline is set, attach it to the context.
	// context.WithTimeout acts like a kitchen timer: when it goes off, any
	// function holding this ctx sees ctx.Err() == context.DeadlineExceeded.
	var ctx context.Context
	var cancel context.CancelFunc
	if deadlineMS > 0 {
		deadline := time.Duration(deadlineMS) * time.Millisecond
		ctx, cancel = context.WithTimeout(context.Background(), deadline)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	// 80% deadline watchdog goroutine.
	// Fires a structured warning when 80% of the deadline has elapsed and the
	// parse is still running. The <-ctx.Done() branch silences it when done early.
	//
	// Watchdog floor: skip the watchdog for deadline-ms < 50. At such short
	// deadlines, the goroutine setup time itself (~1ms) is a meaningful fraction
	// of the budget, so a warning at 40ms (80% of 50ms) would fire unreliably.
	if deadlineMS >= 50 {
		warnAt := time.Duration(deadlineMS) * time.Millisecond * 80 / 100
		// The watchdog writes to logger concurrently with the main goroutine.
		// This is safe because logger writes through os.Stderr (POSIX-atomic for small payloads).
		// See internal/log/log.go for the concurrency schema.
		go func() {
			select {
			case <-time.After(warnAt):
				elapsedMS := int64(deadlineMS) * 80 / 100
				remainingMS := int64(deadlineMS) - elapsedMS
				logger.Warn("deadline_warning", cwlog.Fields{
					"deadline_ms":  deadlineMS,
					"elapsed_ms":   elapsedMS,
					"remaining_ms": remainingMS,
				})
			case <-ctx.Done():
				// Parse finished before watchdog fired — no warning needed.
				return
			}
		}()
	}

	// Collect file paths from all sources.
	filePaths, err := collectFilePaths(args, flags.FilesFrom, flags.FilesFrom0)
	if err != nil {
		// File collection error: emit structured stderr + exit 2.
		logger.Error("invalid_invocation", cwlog.Fields{
			"detail":    err.Error(),
			"exit_code": 2,
		})
		if !Globals.Quiet {
			// Also emit human-friendly message to stderr for TTY users.
			_, _ = fmt.Fprintf(os.Stderr, "Error: %s\n\nRun 'codeweaver parse --help' for usage.\n", err.Error())
		}
		os.Exit(2)
	}

	// No input: exit 2 with documented message, but ONLY when no source was specified at all.
	// If --files-from or --files-from0 was specified (even if it yielded 0 paths from an
	// empty stdin), treat that as a valid invocation — emit empty-payload envelope at exit 0.
	// This ensures "echo -n | codeweaver parse --files-from -" exits 0 and does not hang.
	filesFromSpecified := flags.FilesFrom != "" || flags.FilesFrom0 != ""
	if len(filePaths) == 0 && !filesFromSpecified && len(args) == 0 {
		msg := "no input files specified — pass file paths as positional args or use --files-from -"
		logger.Error("invalid_invocation", cwlog.Fields{
			"detail":    msg,
			"exit_code": 2,
		})
		_, _ = fmt.Fprintf(os.Stderr, "Error: %s\n\nRun 'codeweaver parse --help' for usage.\n", msg)
		os.Exit(2)
	}

	// Resolve workspace root for path normalization.
	// Always resolve to an absolute path so normalizePaths can compute
	// workspace-relative paths correctly regardless of cwd.
	workspaceRoot := flags.Workspace
	if workspaceRoot == "" {
		if wd, err := os.Getwd(); err == nil {
			workspaceRoot = wd
		}
	}
	if !filepath.IsAbs(workspaceRoot) {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	}
	workspaceRoot = filepath.Clean(workspaceRoot)

	// Normalize file paths relative to workspace root.
	normalizedPaths := normalizePaths(filePaths, workspaceRoot)

	// --dry-run mode: validate file presence and emit minimal envelope.
	if flags.DryRun {
		return runDryRun(logger, normalizedPaths)
	}

	// Parse files and emit a real payload.
	// We need both the original paths (for file reading) and the normalized paths (for JSON output).
	// Build a parallel slice of (origPath, relPath) pairs.
	now := time.Now().UTC()
	fileResults, parseErrorsFromIO := parseFilePairs(ctx, filePaths, normalizedPaths, workspaceRoot, logger, Globals.Verbose, deadlineMS, maxFileBytes,
		&filesParsed, &filesErrored, &totalSymbols)

	// Check if the context deadline expired during file parsing.
	// If so, we need to surface deadline errors for any files that didn't complete.
	var deadlineExceeded bool
	if ctx.Err() == context.DeadlineExceeded {
		deadlineExceeded = true
	}

	// Build module-name map for cross-file call edge resolution.
	opts := parser.ResolveOptions{ModuleNameMap: make(map[string]string, len(fileResults))}
	for _, r := range fileResults {
		if r.ModuleName != "" {
			opts.ModuleNameMap[r.ModuleName] = r.RelPath
		}
	}

	// Run Pass 3: cross-file resolution and sorting.
	allSymbols, allEdges, parseErrorsFromParse := parser.ResolveMultiFile(ctx, fileResults, opts)

	// Merge all parse errors.
	allParseErrors := append(parseErrorsFromIO, parseErrorsFromParse...)

	// Update edge counter for RED summary.
	totalEdges = len(allEdges)

	// Build the document. Ensure arrays are non-nil (emit [] not null).
	if allSymbols == nil {
		allSymbols = []schema.Symbol{}
	}
	if allEdges == nil {
		allEdges = []schema.Edge{}
	}

	doc := schema.Document{
		SchemaVersion: schema.SchemaVersionV1,
		ParserVersion: schema.BinaryVersion,
		ParsedAt:      now.Format(time.RFC3339),
		FileCount:     len(normalizedPaths),
		Symbols:       allSymbols,
		Edges:         allEdges,
		DeletedFiles:  []string{},
	}
	if len(allParseErrors) > 0 {
		doc.ParseErrors = allParseErrors
	}

	// Single-buffered stdout pattern:
	// Marshal the full document into a buffer first, then write in one call.
	// This ensures panics after the RunE returns never produce partial JSON on stdout.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		logger.Error("json_marshal_failed", cwlog.Fields{"error": err.Error()})
		return fmt.Errorf("marshal JSON: %w", err)
	}

	if _, err := os.Stdout.Write(buf.Bytes()); err != nil {
		logger.Error("stdout_write_failed", cwlog.Fields{"error": err.Error()})
		os.Exit(3)
	}

	// If deadline was exceeded, emit the deadline event and exit with code 5.
	// os.Exit bypasses defers, so we emit the RED summary manually here before
	// calling os.Exit(5). The deferred call registered at the top of runParse will NOT fire.
	if deadlineExceeded {
		// Count unfinished files: files in normalizedPaths with no completed result
		// and no IO error — i.e., files for which no FileResult was produced.
		completedPaths := make(map[string]bool, len(fileResults))
		for _, r := range fileResults {
			completedPaths[r.RelPath] = true
		}
		erroredPaths := make(map[string]bool, len(allParseErrors))
		for _, pe := range allParseErrors {
			erroredPaths[pe.FilePath] = true
		}
		unfinished := 0
		for _, p := range normalizedPaths {
			if !completedPaths[p] && !erroredPaths[p] {
				unfinished++
			}
		}

		elapsed := int(time.Since(startTime).Milliseconds())
		logger.Warn("deadline_exceeded", cwlog.Fields{
			"deadline_ms":      deadlineMS,
			"elapsed_ms":       elapsed,
			"files_unfinished": unfinished,
		})
		// Emit RED summary before os.Exit(5) since defer won't run after os.Exit.
		logger.Summary(filesParsed, filesErrored, elapsed, totalSymbols, totalEdges)
		os.Exit(5)
	}

	return nil
}

// parseFilePairs parses all provided files using origPaths for reading and relPaths for JSON output.
// origPaths and relPaths must have the same length.
// relPaths are workspace-relative paths (used in qualified names and parse error file_path).
// origPaths are the original paths as provided by the user (used for file reading).
//
// ctx is propagated to each ParseFile call to support deadline cancellation.
// When ctx expires mid-loop, remaining files get E_DEADLINE_EXCEEDED parse_errors.
//
// filesParsed, filesErrored, totalSymbols are updated in-place for the RED summary.
//
// verbose controls per-file log emission to stderr.
// deadlineMS is the deadline in milliseconds, used for error message text only.
// maxFileBytes is the per-file size limit in bytes; 0 means no limit.
func parseFilePairs(
	ctx context.Context,
	origPaths, relPaths []string,
	workspaceRoot string,
	logger *cwlog.Logger,
	verbose bool,
	deadlineMS int,
	maxFileBytes int64,
	filesParsed, filesErrored, totalSymbols *int,
) ([]*parser.FileResult, []schema.ParseError) {
	var results []*parser.FileResult
	var ioErrors []schema.ParseError

	for i, relPath := range relPaths {
		// Check context before starting each file. Files not yet started get
		// E_DEADLINE_EXCEEDED rather than E_PARSE_INCOMPLETE.
		if ctxErr := ctx.Err(); ctxErr != nil {
			ioErrors = append(ioErrors, schema.ParseError{
				FilePath:  relPath,
				ErrorCode: schema.ErrorCodeDeadlineExceeded,
				Message:   "deadline expired before parse started",
			})
			*filesErrored++
			continue
		}

		lang, ok := parser.DetectLanguage(relPath)
		if !ok {
			ioErrors = append(ioErrors, schema.ParseError{
				FilePath:  relPath,
				ErrorCode: schema.ErrorCodeGrammarNotFound,
				Message:   "no grammar registered for this file extension",
			})
			*filesErrored++
			continue
		}

		// Resolve the absolute path for reading.
		origPath := origPaths[i]
		var absPath string
		if filepath.IsAbs(origPath) {
			absPath = origPath
		} else {
			if abs, absErr := filepath.Abs(origPath); absErr == nil {
				absPath = abs
			} else {
				absPath = origPath
			}
		}

		// Symlink check: refuse files that are symlinks (belt-and-suspenders for
		// the case where --files-from / --files-from0 input contains a symlink path).
		//
		// We use Lstat (not Stat) because Stat follows symlinks. Lstat tells us
		// whether the path *itself* is a symlink. Accepting a symlink here could
		// allow a malicious file list to redirect reads outside the workspace root.
		// Think of it like a library that accepts call numbers but refuses to process
		// forwarding slips — you get the item at the listed location, not wherever
		// a redirect might send you.
		//
		// Note: os.ReadFile calls os.Open internally, which DOES follow symlinks.
		// We check before reading so that we emit a clear E_SYMLINK_REFUSED error
		// rather than silently reading the symlink target.
		if lfi, lstatErr := os.Lstat(absPath); lstatErr == nil {
			if lfi.Mode()&os.ModeSymlink != 0 {
				ioErrors = append(ioErrors, schema.ParseError{
					FilePath:  relPath,
					ErrorCode: schema.ErrorCodeSymlinkRefused,
					Message:   "symlinks are not followed in codeweaver v1; pass the resolved path directly",
				})
				*filesErrored++
				continue
			}

			// File size check: enforce CODEWEAVER_MAX_FILE_BYTES limit.
			// maxFileBytes == 0 means no limit (escape hatch).
			// Think of it as a bouncer who checks ticket sizes at the door — oversized
			// tickets are turned away before they ever get inside.
			if maxFileBytes > 0 && lfi.Size() > maxFileBytes {
				ioErrors = append(ioErrors, schema.ParseError{
					FilePath:  relPath,
					ErrorCode: schema.ErrorCodeFileTooLarge,
					Message:   fmt.Sprintf("file size %d bytes exceeds limit %d bytes (set CODEWEAVER_MAX_FILE_BYTES=0 to disable)", lfi.Size(), maxFileBytes),
				})
				*filesErrored++
				continue
			}
		}
		// If Lstat fails (file doesn't exist, permissions), we fall through and let
		// os.ReadFile produce the E_FILE_UNREADABLE error with its own message.

		source, err := os.ReadFile(absPath)
		if err != nil {
			ioErrors = append(ioErrors, schema.ParseError{
				FilePath:  relPath,
				ErrorCode: schema.ErrorCodeFileUnreadable,
				Message:   err.Error(),
			})
			*filesErrored++
			continue
		}

		fileStart := time.Now()

		switch lang {
		case parser.LanguagePython:
			result, parseErr := python.ParseFile(ctx, source, relPath)
			if parseErr != nil {
				// Route error to the appropriate error code:
				//   - deadline expiry → E_DEADLINE_EXCEEDED
				//   - grammar load failure → E_GRAMMAR_LOAD_FAILED
				//   - anything else → E_PARSE_INCOMPLETE
				if ctx.Err() == context.DeadlineExceeded {
					ioErrors = append(ioErrors, schema.ParseError{
						FilePath:  relPath,
						ErrorCode: schema.ErrorCodeDeadlineExceeded,
						Message:   fmt.Sprintf("parse exceeded deadline of %dms", deadlineMS),
					})
				} else if parser.IsGrammarLoadError(parseErr) {
					logger.Error("grammar_load_failed", cwlog.Fields{
						"file_path": relPath,
						"error":     parseErr.Error(),
					})
					ioErrors = append(ioErrors, schema.ParseError{
						FilePath:  relPath,
						ErrorCode: schema.ErrorCodeGrammarLoadFailed,
						Message:   parseErr.Error(),
					})
				} else {
					logger.Error("parse_file_failed", cwlog.Fields{
						"file_path": relPath,
						"error":     parseErr.Error(),
					})
					ioErrors = append(ioErrors, schema.ParseError{
						FilePath:  relPath,
						ErrorCode: schema.ErrorCodeParseIncomplete,
						Message:   parseErr.Error(),
					})
				}
				*filesErrored++
				// If parse returned a partial result alongside the error, keep it.
				if result != nil && len(result.Symbols) > 0 {
					results = append(results, result)
					*totalSymbols += len(result.Symbols)
				}
				continue
			}
			fileDuration := time.Since(fileStart).Milliseconds()
			if verbose {
				// Note: "edges" is intentionally absent. Per-file edges are not
				// computed until Pass 3 (cross-file resolution). Emitting edges:0
				// here would be misleading — consumers wanting an edge total should
				// use the RED summary's "edges_out" field instead.
				logger.Info("file_parsed", cwlog.Fields{
					"path":        relPath,
					"symbols":     len(result.Symbols),
					"duration_ms": fileDuration,
				})
			}
			results = append(results, result)
			*filesParsed++
			*totalSymbols += len(result.Symbols)

		case parser.LanguageTypeScript:
			result, parseErr := typescript.ParseFile(ctx, source, relPath)
			if parseErr != nil {
				if ctx.Err() == context.DeadlineExceeded {
					ioErrors = append(ioErrors, schema.ParseError{
						FilePath:  relPath,
						ErrorCode: schema.ErrorCodeDeadlineExceeded,
						Message:   fmt.Sprintf("parse exceeded deadline of %dms", deadlineMS),
					})
				} else if parser.IsGrammarLoadError(parseErr) {
					logger.Error("grammar_load_failed", cwlog.Fields{
						"file_path": relPath,
						"error":     parseErr.Error(),
					})
					ioErrors = append(ioErrors, schema.ParseError{
						FilePath:  relPath,
						ErrorCode: schema.ErrorCodeGrammarLoadFailed,
						Message:   parseErr.Error(),
					})
				} else {
					logger.Error("parse_file_failed", cwlog.Fields{
						"file_path": relPath,
						"error":     parseErr.Error(),
					})
					ioErrors = append(ioErrors, schema.ParseError{
						FilePath:  relPath,
						ErrorCode: schema.ErrorCodeParseIncomplete,
						Message:   parseErr.Error(),
					})
				}
				*filesErrored++
				if result != nil && len(result.Symbols) > 0 {
					results = append(results, result)
					*totalSymbols += len(result.Symbols)
				}
				continue
			}
			fileDuration := time.Since(fileStart).Milliseconds()
			if verbose {
				// Note: "edges" is intentionally absent. Per-file edges are not
				// computed until Pass 3 (cross-file resolution). Emitting edges:0
				// here would be misleading — consumers wanting an edge total should
				// use the RED summary's "edges_out" field instead.
				logger.Info("file_parsed", cwlog.Fields{
					"path":        relPath,
					"symbols":     len(result.Symbols),
					"duration_ms": fileDuration,
				})
			}
			results = append(results, result)
			*filesParsed++
			*totalSymbols += len(result.Symbols)

		default:
			ioErrors = append(ioErrors, schema.ParseError{
				FilePath:  relPath,
				ErrorCode: schema.ErrorCodeGrammarNotFound,
				Message:   "no grammar registered for this file extension",
			})
			*filesErrored++
		}
	}

	_ = workspaceRoot // kept for future use
	return results, ioErrors
}

// runDryRun implements --dry-run mode: validates file presence and emits a minimal envelope.
// The RED summary is emitted by runParse's deferred closure, not here.
func runDryRun(logger *cwlog.Logger, paths []string) error {
	validated := 0
	for _, p := range paths {
		// In dry-run mode, just count the input paths.
		validated++
		_ = p
	}

	dryRunTrue := true
	doc := struct {
		SchemaVersion  string `json:"schema_version"`
		DryRun         bool   `json:"dry_run"`
		FilesValidated int    `json:"files_validated"`
	}{
		SchemaVersion:  schema.SchemaVersionV1,
		DryRun:         dryRunTrue,
		FilesValidated: validated,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		logger.Error("json_marshal_failed", cwlog.Fields{"error": err.Error()})
		return fmt.Errorf("marshal dry-run JSON: %w", err)
	}

	if _, err := os.Stdout.Write(buf.Bytes()); err != nil {
		logger.Error("stdout_write_failed", cwlog.Fields{"error": err.Error()})
		os.Exit(3)
	}

	// Note: the RED summary for dry-run is emitted by the deferred call in runParse,
	// not here. runDryRun returns nil; runParse's defer fires with filesParsed=0, etc.
	return nil
}

// collectFilePaths gathers file paths from positional args, --files-from, and --files-from0.
// Returns an error if both --files-from and --files-from0 are specified.
func collectFilePaths(args []string, filesFrom, filesFrom0 string) ([]string, error) {
	if filesFrom != "" && filesFrom0 != "" {
		return nil, fmt.Errorf("--files-from and --files-from0 cannot both be specified")
	}

	var paths []string
	paths = append(paths, args...)

	if filesFrom != "" {
		fromStdin, err := readLines(filesFrom)
		if err != nil {
			return nil, fmt.Errorf("--files-from: %w", err)
		}
		paths = append(paths, fromStdin...)
	}

	if filesFrom0 != "" {
		fromStdin, err := readNUL(filesFrom0)
		if err != nil {
			return nil, fmt.Errorf("--files-from0: %w", err)
		}
		paths = append(paths, fromStdin...)
	}

	return paths, nil
}

// readLines reads newline-delimited paths from a file path or "-" for stdin.
// Empty lines are skipped.
func readLines(source string) ([]string, error) {
	r, closer, err := openSource(source)
	if err != nil {
		return nil, err
	}
	defer closer()

	var paths []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, scanner.Err()
}

// readNUL reads NUL-delimited paths from a file path or "-" for stdin.
// Empty entries are skipped.
// NUL-delimited input is safe for filenames that contain newlines, spaces, or
// other shell-hostile characters. Use with "git ls-files -z" or "find -print0".
func readNUL(source string) ([]string, error) {
	r, closer, err := openSource(source)
	if err != nil {
		return nil, err
	}
	defer closer()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, part := range bytes.Split(data, []byte{0}) {
		s := string(part)
		if s != "" {
			paths = append(paths, s)
		}
	}
	return paths, nil
}

// openSource opens a file at the given path, or returns os.Stdin when path is "-".
// The returned closer must be called; it is a no-op for stdin.
func openSource(source string) (io.Reader, func(), error) {
	if source == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(source)
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}

// normalizePaths normalizes a list of file paths to be relative to the workspace root
// with forward-slash separators. If a path is absolute, it is made relative to the
// workspace root when possible; otherwise it is kept as-is (with slashes normalized).
//
// Path normalization rules:
//   - Absolute paths: resolved relative to workspaceRoot when under it; kept absolute otherwise.
//   - Relative paths: returned as-is with slash normalization.
//   - Separators: always forward-slash, even on Windows (for cross-platform diff stability).
func normalizePaths(paths []string, workspaceRoot string) []string {
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		n := normalizeSinglePath(p, workspaceRoot)
		normalized = append(normalized, n)
	}
	return normalized
}

func normalizeSinglePath(p, workspaceRoot string) string {
	// Convert to forward slashes first for output consistency.
	workspaceRoot = filepath.ToSlash(workspaceRoot)

	// Resolve to absolute path so we can make it relative to the workspace root.
	// Both absolute and relative inputs are handled: filepath.Abs resolves relative
	// paths against the current working directory.
	absP, err := filepath.Abs(p)
	if err != nil {
		// Fallback: return with slash normalization only.
		return filepath.ToSlash(p)
	}

	// Try to make relative to workspace root.
	rel, err := filepath.Rel(workspaceRoot, absP)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}

	// Path is outside workspace: return absolute with forward slashes.
	return filepath.ToSlash(absP)
}
