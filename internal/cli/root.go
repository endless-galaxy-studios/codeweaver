// Package cli provides cobra-based CLI definitions for codeweaver.
//
// Subcommand layout:
//
//	codeweaver parse      — parse files, emit JSON to stdout
//	codeweaver discover   — walk a directory tree, emit file list
//	codeweaver version    — show version information
//	codeweaver completion — generate shell completion scripts (built-in cobra subcommand)
//
// Global persistent flags (apply to all subcommands):
//
//	--quiet    suppress all stderr output
//	--verbose  emit debug-level stderr output
//
// Stdout invariant (applied to parse and version --json only):
//
//	no ANSI codes, no log lines, LF-terminated on all platforms.
//	The completion subcommand and human-readable version are exempt by design.
package cli

import (
	"os"

	"github.com/spf13/cobra"
)

// GlobalFlags holds the parsed values of global persistent flags.
// These are populated before any subcommand Run function executes.
type GlobalFlags struct {
	Quiet   bool
	Verbose bool
}

// Globals is the package-level instance of global flags.
// Subcommands read from this after cobra parses persistent flags.
var Globals GlobalFlags

// NewRootCmd constructs and returns the cobra root command.
// It wires all subcommands and global flags.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "codeweaver",
		Short: "Parse source files and emit a versioned JSON code graph",
		Long: `codeweaver is a static binary that parses source code files
using tree-sitter grammars and emits a versioned JSON document to stdout.

It is designed to be invoked as a subprocess by editor plugins (Claude Code,
OpenCode) and CI pipelines. Stdout is the JSON output; stderr is structured
logging (JSON lines when not a TTY, human-readable when connected to a terminal).

No network calls are made. The binary is stateless — output is a pure function
of the input files and flags.`,
		Version: version,
		// SilenceUsage prevents cobra from printing usage on every error.
		// We emit our own structured error output.
		SilenceUsage: true,
		// SilenceErrors prevents cobra from printing errors; we emit them via the logger.
		SilenceErrors: true,
		// When invoked with no subcommand, print usage to stderr and exit 2.
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmd.Help(); err != nil {
				return err
			}
			os.Exit(2)
			return nil
		},
	}

	// Version template for --version flag (flag form).
	// The subcommand form (codeweaver version) emits richer output.
	// This template controls the cobra-built-in --version flag output.
	root.SetVersionTemplate("codeweaver {{.Version}}\n")

	// Global persistent flags — available on all subcommands.
	root.PersistentFlags().BoolVar(&Globals.Quiet, "quiet", false,
		"suppress all stderr output")
	root.PersistentFlags().BoolVar(&Globals.Verbose, "verbose", false,
		"emit debug-level stderr output (mutually exclusive with --quiet)")

	// Wire subcommands.
	root.AddCommand(newParseCmd())
	root.AddCommand(newDiscoverCmd())
	root.AddCommand(newVersionCmd(version))

	return root
}
