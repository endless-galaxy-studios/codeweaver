// Package schema defines the output schema (v1) types emitted by codeweaver on stdout.
//
// The shape of this package is a durable public API surface consumed by downstream tools
// (plugins, language servers, agents). Adding fields is a minor change;
// renaming or removing fields is a breaking change that requires a schema_version bump.
//
// Versioning policy:
//   - v1.0 — initial public release
//   - v2.0+ — reserved for future breaking changes
//
// All output arrays are sorted by deterministic keys (see field comments) before marshaling.
// Map iteration order is never relied upon.
package schema

// Document is the top-level JSON object emitted to stdout by "codeweaver parse".
// Consumers branch on SchemaVersion to handle schema evolution.
//
// JSON key order is not guaranteed by encoding/json, but the schema is defined here.
// Consumers must not rely on field ordering in the JSON stream.
type Document struct {
	// SchemaVersion identifies the output schema version.
	// Always present. Consumers branch on this value. Current value: "1.0".
	SchemaVersion string `json:"schema_version"`

	// ParserVersion is the semver of the codeweaver binary that produced this output.
	// Injected at build time via -ldflags. Format: "MAJOR.MINOR.PATCH".
	ParserVersion string `json:"parser_version"`

	// ParsedAt is the ISO 8601 UTC timestamp of when the parse run started.
	// Format: "2006-01-02T15:04:05Z" (RFC3339, always UTC, always Z suffix).
	ParsedAt string `json:"parsed_at"`

	// FileCount is the number of files for which parse was attempted (total input).
	// This is the total input count, including files that resulted in ParseErrors.
	// It equals len(Symbols per file) + len(ParseErrors), not len(Symbols).
	FileCount int `json:"file_count"`

	// Symbols is the array of extracted code symbols. Sorted by QualifiedName.
	// Empty when DryRun is true or when no symbols were found.
	Symbols []Symbol `json:"symbols"`

	// Edges is the array of relationships between symbols.
	// Sorted by (SourceQualifiedName, TargetQualifiedName, EdgeType).
	//
	// NOTE: "inherits" edge_type exists in the API schema but is NOT emitted in schema v1.
	// The API's code_symbol_edges table supports "calls", "imports", and "inherits".
	// Inheritance edge extraction is deferred to a future enrichment deliverable.
	Edges []Edge `json:"edges"`

	// DeletedFiles is always an empty array in binary output. The plugin populates
	// this field before forwarding to the API (it tracks deletions via its SQLite
	// state, not the binary). The binary always emits [].
	DeletedFiles []string `json:"deleted_files"`

	// ParseErrors is an array of per-file errors. Absent (omitted) when all files
	// parsed without errors. Never null — use field-absent vs field-present to
	// distinguish "no errors" from "errors array".
	ParseErrors []ParseError `json:"parse_errors,omitempty"`

	// DryRun is present and true only when --dry-run was passed. Absent otherwise.
	// When true, Symbols and Edges are empty; FilesValidated is populated instead.
	DryRun *bool `json:"dry_run,omitempty"`

	// FilesValidated is the count of files successfully validated in --dry-run mode.
	// Absent when DryRun is false or not set.
	FilesValidated *int `json:"files_validated,omitempty"`
}

// Symbol represents a single code symbol extracted from a source file.
// The QualifiedName is the stable ID for this symbol within a payload.
//
// Symbol arrays are sorted by QualifiedName before emission.
type Symbol struct {
	// QualifiedName is the unique stable ID for this symbol within the payload.
	// Format: "{workspace_relative_file_path}::{symbol_name}"
	// Example: "api/models.py::MyClass.my_method"
	//
	// The ID derivation rule is:
	//   qualified_name = "{workspace_relative_file_path}::{symbol_name}"
	// This is stable across runs on the same input and across binary versions
	// as long as the file path and symbol name are unchanged.
	QualifiedName string `json:"qualified_name"`

	// Name is the short display name of the symbol, without the file path prefix.
	// For methods: "ClassName.method_name". For top-level: "function_name".
	Name string `json:"name"`

	// FilePath is the workspace-relative file path with forward-slash separators.
	// Normalized: relative to --workspace root (or CWD if omitted), forward-slash
	// separators on all platforms for cross-platform diff stability.
	FilePath string `json:"file_path"`

	// SymbolType is the kind of code construct.
	// Enum values: "function" | "class" | "module"
	SymbolType string `json:"symbol_type"`

	// Language is the programming language of the source file.
	// Enum values: "python" | "typescript"
	Language string `json:"language"`

	// LineStart is the 1-based line number where the symbol begins.
	// Absent for module symbols (which span the entire file).
	LineStart *int `json:"line_start,omitempty"`

	// LineEnd is the 1-based line number where the symbol ends (inclusive).
	// Absent for module symbols (which span the entire file).
	LineEnd *int `json:"line_end,omitempty"`

	// Note: "metadata" key is absent in schema v1; reserved for enrichment in schema v2+.
	// The API's code_symbols.metadata_ JSONB column remains empty in schema v1.
}

// Edge represents a directed relationship between two symbols.
// Source and target are identified by their QualifiedName values.
//
// Edge arrays are sorted by (SourceQualifiedName, TargetQualifiedName, EdgeType)
// before emission for deterministic output.
type Edge struct {
	// SourceQualifiedName is the QualifiedName of the calling/importing symbol.
	// Must reference a symbol present in the same Document's Symbols array.
	SourceQualifiedName string `json:"source_qualified_name"`

	// TargetQualifiedName is the QualifiedName of the called/imported symbol.
	// Must reference a symbol present in the same Document's Symbols array.
	TargetQualifiedName string `json:"target_qualified_name"`

	// EdgeType describes the kind of relationship.
	// Enum values: "calls" | "imports"
	//
	// NOTE: "inherits" edge_type exists in the API schema (code_symbol_edges.edge_type)
	// but is NOT emitted in schema v1. Inheritance edge extraction is a follow-on enrichment.
	EdgeType string `json:"edge_type"`

	// Note: "metadata" key is absent in schema v1; reserved for enrichment in schema v2+.
}

// ParseError describes a per-file parse failure or warning.
// These are surfaced in the Document.ParseErrors array rather than as a fatal exit.
// The binary exits with code 0 even when ParseErrors are present (partial results
// are surfaced, not suppressed). Exit code 1 is reserved for when ALL files failed.
//
// ErrorCode values are stable across binary versions — consumers may branch on them.
type ParseError struct {
	// FilePath is the workspace-relative path of the file that failed.
	FilePath string `json:"file_path"`

	// ErrorCode is a stable, machine-readable error classification.
	// See the ErrorCode* constants in this package.
	ErrorCode string `json:"error_code"`

	// Message is a human-readable description of the error.
	// Not stable across binary versions — do not parse this field programmatically.
	Message string `json:"message"`

	// ByteOffset is the byte position within the file where the first error node
	// was detected. Absent when not applicable (e.g., file-level errors like
	// E_FILE_UNREADABLE or E_GRAMMAR_NOT_FOUND).
	ByteOffset *int `json:"byte_offset,omitempty"`
}

// Error code constants for ParseError.ErrorCode.
// These are stable across binary versions — consumers may branch on them.
// New codes are additive; existing codes are never removed or redefined.
//
// Error code taxonomy (for consumer routing):
//   - E_PARSE_INCOMPLETE:   tree-sitter parse problem; partial output available.
//   - E_FILE_TOO_LARGE:     file-level rejection before parse; no output for this file.
//   - E_FILE_UNREADABLE:    OS-level I/O failure; no output for this file.
//   - E_GRAMMAR_NOT_FOUND:  language not supported; file skipped.
//   - E_GRAMMAR_LOAD_FAILED: grammar init failed (environment/install problem); all
//     files for this language are affected.
//   - E_DEADLINE_EXCEEDED:  timeout before parse started or completed; partial output.
//   - E_SYMLINK_REFUSED:    path is a symlink; codeweaver v1 does not follow symlinks.
const (
	// ErrorCodeParseIncomplete indicates tree-sitter produced ERROR or MISSING nodes.
	// Partial symbols may have been extracted from the file.
	ErrorCodeParseIncomplete = "E_PARSE_INCOMPLETE"

	// ErrorCodeFileTooLarge indicates the file exceeds the configurable size limit.
	// Default limit: 10 MiB (CODEWEAVER_MAX_FILE_BYTES). The file was not parsed.
	// Set CODEWEAVER_MAX_FILE_BYTES=0 to disable the limit (escape hatch).
	ErrorCodeFileTooLarge = "E_FILE_TOO_LARGE"

	// ErrorCodeFileUnreadable indicates the file exists but could not be read.
	// Common causes: permissions errors, I/O errors, symlink loops.
	ErrorCodeFileUnreadable = "E_FILE_UNREADABLE"

	// ErrorCodeGrammarNotFound indicates no grammar is registered for this file's extension.
	// The file was skipped entirely.
	ErrorCodeGrammarNotFound = "E_GRAMMAR_NOT_FOUND"

	// ErrorCodeGrammarLoadFailed indicates the grammar for this language failed to
	// initialize. This is an environment-level failure (corrupt grammar binary, OOM
	// during init, unsupported platform) distinct from a per-file parse problem.
	// All files for the affected language in this invocation will carry this error.
	ErrorCodeGrammarLoadFailed = "E_GRAMMAR_LOAD_FAILED"

	// ErrorCodeDeadlineExceeded indicates the per-invocation deadline expired before
	// this file was fully parsed. Partial results for completed files are still emitted.
	ErrorCodeDeadlineExceeded = "E_DEADLINE_EXCEEDED"

	// ErrorCodeSymlinkRefused indicates the file path is a symlink.
	// codeweaver v1 does not follow symlinks. Pass the resolved (real) path directly.
	// This may be encountered when --files-from / --files-from0 input contains symlink paths.
	ErrorCodeSymlinkRefused = "E_SYMLINK_REFUSED"
)

// VersionOutput is emitted to stdout by "codeweaver version --json".
// Plugins parse this to enforce minimum-version compatibility via semver comparison.
//
// Field naming: the ldflags injection uses Go identifier names (gitCommit, builtAt)
// set via -X main.gitCommit=... and -X main.builtAt=..., which are then copied into
// this struct's fields at version-emit time. The JSON tags use snake_case to match
// the API layer's convention. The distinction matters: ldflags uses the Go identifier
// path (main.gitCommit), while JSON consumers see the snake_case tag (git_commit).
type VersionOutput struct {
	// Version is the semver of this binary. Format: "MAJOR.MINOR.PATCH".
	// Plugins compare this against MIN_CODEWEAVER_VERSION using semver comparison.
	Version string `json:"version"`

	// GitCommit is the full git commit SHA of the source that produced this binary.
	// Injected at build time via -ldflags="-X main.gitCommit=$(git rev-parse HEAD)".
	// The ldflags identifier is "main.gitCommit"; the JSON field name is "git_commit".
	GitCommit string `json:"git_commit"`

	// BuiltAt is the ISO 8601 UTC timestamp of when the binary was built.
	// Injected at build time via -ldflags="-X main.builtAt=$(git log -1 --format=%cI)".
	// The ldflags identifier is "main.builtAt"; the JSON field name is "built_at".
	BuiltAt string `json:"built_at"`

	// GoVersion is the Go toolchain version used to compile this binary.
	// Populated at runtime via runtime.Version() — no ldflags injection needed.
	GoVersion string `json:"go_version"`

	// SchemaVersion is the output schema version this binary emits.
	// Matches Document.SchemaVersion. Current value: "1.0".
	SchemaVersion string `json:"schema_version"`

	// Grammars maps language name to grammar version string.
	// Keys: "python", "typescript". Values are the gotreesitter-internal grammar
	// version strings embedded at compile time.
	// Consumers use this to correlate output diffs with grammar version deltas.
	Grammars map[string]string `json:"grammars"`
}

// DryRunDocument is the full stdout document emitted when --dry-run is passed.
// It is a simplified Document with only envelope fields populated.
// Separate type for clarity in the schema; serialization uses Document with DryRun=true.

// SchemaVersionV1 is the current schema version string.
// Use this constant rather than a bare string literal.
const SchemaVersionV1 = "1.0"

// BinaryVersion is the semver of this binary. Updated on each release.
// This is the canonical version string; ldflags may override it at build time
// but the constant is the fallback for dev builds.
const BinaryVersion = "0.1.0"
