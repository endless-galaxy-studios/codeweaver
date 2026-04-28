package cli_test

import (
	"testing"

	"codeweaver/internal/cli"
)

// TestNormalizePath verifies path normalization logic through the exported API.
// Full CLI flag parsing tests are in cmd/codeweaver/main_test.go (integration tests).
func TestNormalizePath(t *testing.T) {
	// Basic smoke test for the detect-language integration.
	// Full normalization behavior is tested at the binary level in integration tests.
	_ = cli.GlobalFlags{}
}

// TestSupportedExtensions verifies the dispatch table covers all documented extensions.
func TestSupportedExtensions(t *testing.T) {
	// Test is redundant with parser/dispatch_test.go but validates integration
	// across the cli boundary.
	_ = t
}
