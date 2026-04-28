// Package limits provides configurable resource limits for the codeweaver binary.
//
// # Why have a file size limit?
//
// Tree-sitter is fast, but parsing a very large file (say, a 100 MB auto-generated
// minified JavaScript file or a data file masquerading as source) can:
//  1. Consume significant memory (the parse tree can be 5–10x the input size).
//  2. Take longer than the user's deadline budget.
//  3. Dominate a hot file-edit loop, blocking the editor response.
//
// The size limit is a circuit breaker — it rejects obviously-oversized files
// before handing them to the parser.
//
// # Default limit
//
// 10 MiB (10 * 1024 * 1024 bytes). This is generous for real source files
// (a 10 MiB Python file would be ~250,000 lines) but tight enough to exclude
// accidental inclusion of binary or generated files.
//
// # Escape hatch
//
// Set CODEWEAVER_MAX_FILE_BYTES=0 to disable the limit entirely. This is useful
// for: large auto-generated files that the user genuinely wants to parse, testing
// the parser on large inputs, or batch analysis workflows where memory budget is known.
//
// # Error behavior
//
// Files exceeding the limit are skipped with a parse_errors entry using error code
// E_FILE_TOO_LARGE. Other files in the same parse run continue normally. The limit
// applies per-file, not per-run.
package limits

import (
	"fmt"
	"os"
	"strconv"
)

// DefaultMaxFileBytes is the default maximum file size in bytes (10 MiB).
// This is the limit used when CODEWEAVER_MAX_FILE_BYTES is not set.
const DefaultMaxFileBytes int64 = 10 * 1024 * 1024

// MaxFileBytes returns the configured maximum file size in bytes.
//
// Source of configuration (in precedence order):
//  1. CODEWEAVER_MAX_FILE_BYTES environment variable (if set)
//  2. DefaultMaxFileBytes (10 MiB) when the env var is not set
//
// Return values:
//   - (positive N, nil): apply a limit of N bytes per file
//   - (0, nil): no limit — files of any size are accepted (escape hatch)
//   - (_, error): the env var was set to an invalid or negative value;
//     callers should exit with code 2 (usage error)
//
// Note: the error case must be checked once at startup, before parsing begins.
// Callers should not call MaxFileBytes per-file; call it once and cache the result.
func MaxFileBytes() (int64, error) {
	raw := os.Getenv("CODEWEAVER_MAX_FILE_BYTES")
	if raw == "" {
		// Not set: use the compiled-in default.
		return DefaultMaxFileBytes, nil
	}

	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("CODEWEAVER_MAX_FILE_BYTES=%q is not a valid integer: %w", raw, err)
	}

	if v < 0 {
		// Negative values are a configuration error, not an escape hatch.
		// The escape hatch is 0 (disable the limit). Negative values have no
		// meaningful interpretation and are likely a user mistake.
		return 0, fmt.Errorf("CODEWEAVER_MAX_FILE_BYTES=%d is negative; use 0 to disable the limit", v)
	}

	// v == 0: no limit (valid escape hatch)
	// v > 0: explicit limit in bytes
	return v, nil
}
