package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"codeweaver/internal/schema"
	cwlog "codeweaver/internal/log"
	"codeweaver/internal/parser/python"
	"codeweaver/internal/parser/typescript"
)

// newVersionCmd constructs the "version" subcommand.
// Accepts the version string from main (which may be injected via -ldflags).
func newVersionCmd(version string) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long: `Show version information for the codeweaver binary.

Without --json: human-readable output to stdout (exempt from JSON-only stdout rule).
With --json: structured JSON to stdout with all fields from the schema v1 version output.

Plugins parse "codeweaver version --json" to enforce minimum-version compatibility.
The "version" field is compared against MIN_CODEWEAVER_VERSION using semver.

Fields in --json output (spec §4):
  version          — binary semver
  git_commit       — git SHA at build time (from -ldflags -X main.gitCommit=...)
  built_at         — ISO 8601 UTC build timestamp (from -ldflags -X main.builtAt=...)
  go_version       — Go toolchain version (from runtime.Version())
  schema_version   — output schema version this binary emits
  grammars         — map of language name to grammar version string`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVersion(version, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false,
		"emit structured JSON version info (consumed by plugins for version negotiation)")

	return cmd
}

// runVersion implements the version subcommand.
// version is the binary semver string (from main, possibly -ldflags injected).
// mainGitCommit and mainBuiltAt are package-level vars set by SetVersionVars().
func runVersion(version string, jsonOutput bool) error {
	logger := cwlog.NewFromEnv(Globals.Quiet, Globals.Verbose)

	// grammars: live version strings from each language parser, embedded at compile time.
	// python.GrammarVersion() and typescript.GrammarVersion() return the
	// gotreesitter module version + grammar name.
	grammars := map[string]string{
		"python":     python.GrammarVersion(),
		"typescript": typescript.GrammarVersion(),
	}

	vo := schema.VersionOutput{
		Version:         version,
		GitCommit:       mainGitCommit,
		BuiltAt:         mainBuiltAt,
		GoVersion:       runtime.Version(),
		SchemaVersion: schema.SchemaVersionV1,
		Grammars:        grammars,
	}

	if jsonOutput {
		// Structured JSON output — part of the output schema.
		// Single-buffered: build in bytes.Buffer, write in one call.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(vo); err != nil {
			logger.Error("json_marshal_failed", cwlog.Fields{"error": err.Error()})
			return fmt.Errorf("marshal version JSON: %w", err)
		}
		_, err := os.Stdout.Write(buf.Bytes())
		return err
	}

	// Human-readable output — exempt from JSON-only stdout invariant.
	fmt.Printf("codeweaver %s\n", version)
	fmt.Printf("  git_commit:       %s\n", vo.GitCommit)
	fmt.Printf("  built_at:         %s\n", vo.BuiltAt)
	fmt.Printf("  go_version:       %s\n", vo.GoVersion)
	fmt.Printf("  schema_version: %s\n", vo.SchemaVersion)
	fmt.Printf("  grammars:\n")
	for lang, ver := range vo.Grammars {
		fmt.Printf("    %-12s %s\n", lang+":", ver)
	}

	return nil
}

// mainGitCommit and mainBuiltAt are set by main() after reading the ldflags-injected
// package vars. They are package-level here so version.go can access them without
// importing main (which would create a cycle).
var mainGitCommit string
var mainBuiltAt string

// SetVersionVars is called by main() to inject the ldflags-populated values into
// the cli package for use by the version subcommand.
func SetVersionVars(gitCommit, builtAt string) {
	mainGitCommit = gitCommit
	mainBuiltAt = builtAt
}
