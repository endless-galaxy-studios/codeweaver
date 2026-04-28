package cli

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"codeweaver/internal/discover"
	cwlog "codeweaver/internal/log"
)

// newDiscoverCmd constructs the "discover" subcommand.
//
// # Subcommand: codeweaver discover [root]
//
// Walks root (default: ".") and emits parseable file paths to stdout.
// By default, paths are newline-delimited (human-readable). Use --nul
// for NUL-delimited output, which is safe for paths containing spaces,
// newlines, or other shell-hostile characters.
//
// # NUL-delimiter convention
//
// NUL is a separator, not a terminator. The final path is NOT followed by NUL.
// This matches find -print0 and git ls-files -z conventions. Consumers reading
// NUL-delimited input should split on NUL, not expect a trailing NUL byte.
//
// # Path normalization
//
// Paths in output are relative to the walk root by default. When --workspace
// is provided, paths are normalized relative to the workspace root instead.
// Forward-slash separators are used on all platforms for cross-platform
// diff stability.
//
// # Exit codes
//
//	0 — success (even if zero files found)
//	1 — fatal error (root doesn't exist, can't read root)
//	2 — usage error (--workspace not a directory)
func newDiscoverCmd() *cobra.Command {
	var nul bool
	var workspace string

	cmd := &cobra.Command{
		Use:   "discover [flags] [root]",
		Short: "Walk a directory tree and list parseable source files",
		Long: `Walk a directory tree and emit the list of parseable file paths to stdout.

By default, paths are newline-delimited. Use --nul for NUL-delimited output,
which is safe for paths containing spaces, newlines, or other special characters.

Parseable extensions: .py, .ts, .tsx, .mts

Skip rules (see discover.SkipDirs() for the canonical set):
  node_modules, .next, .nuxt, dist, build, out, .turbo, .cache,
  __pycache__, .git, coverage, .nyc_output, .venv, venv, env, .env,
  .mypy_cache, .ruff_cache, .pytest_cache

--nul output is compatible with:
  codeweaver discover --nul <root> | codeweaver parse --files-from0 -

NUL is a separator, not a terminator. The last path has no trailing NUL.
This matches the convention of find -print0 and git ls-files -z.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runDiscover(cmd, root, nul, workspace)
		},
	}

	cmd.Flags().BoolVar(&nul, "nul", false,
		"emit NUL-delimited output instead of newline-delimited (for pipe to --files-from0 -)")
	cmd.Flags().StringVar(&workspace, "workspace", "",
		"workspace root for relative path normalization (default: <root> argument)")

	return cmd
}

// runDiscover implements the discover subcommand.
//
// It walks root, applies SkipDirs() pruning, and emits matching file paths
// to cmd.OutOrStdout(). Output is newline- or NUL-delimited per the nul flag.
func runDiscover(cmd *cobra.Command, root string, nul bool, workspace string) error {
	logger := cwlog.NewFromEnv(Globals.Quiet, Globals.Verbose)
	start := time.Now()

	// Resolve root to an absolute path for consistent behavior regardless of cwd.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		logger.Error("discover_root_resolve", cwlog.Fields{
			"root":  root,
			"error": err.Error(),
		})
		fmt.Fprintf(os.Stderr, "Error: cannot resolve root %q: %v\n", root, err)
		os.Exit(1)
	}

	// Verify root exists, is not a symlink, and is a directory.
	// We use Lstat (not Stat) because Stat follows symlinks. Lstat tells us whether
	// the path *itself* is a symlink, which we refuse as a conservative codeweaver v1 security
	// boundary — a symlink root could redirect the walk outside the intended workspace.
	fi, err := os.Lstat(absRoot)
	if err != nil {
		logger.Error("discover_root_stat", cwlog.Fields{
			"root":  absRoot,
			"error": err.Error(),
		})
		fmt.Fprintf(os.Stderr, "Error: root directory %q: %v\n", absRoot, err)
		os.Exit(1)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		fmt.Fprintf(os.Stderr, "Error: root %q is a symlink; codeweaver v1 does not follow symlinks (resolve the path manually or set CODEWEAVER_FOLLOW_SYMLINKS)\n", absRoot)
		os.Exit(2)
	}
	if !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: root %q is not a directory\n", absRoot)
		os.Exit(1)
	}

	// Validate --workspace if provided.
	absWorkspace := absRoot // default: workspace == root
	if workspace != "" {
		absWorkspace, err = filepath.Abs(workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot resolve workspace %q: %v\n", workspace, err)
			os.Exit(2)
		}
		var wfi os.FileInfo
		wfi, err = os.Lstat(absWorkspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: --workspace %q: %v\n", absWorkspace, err)
			os.Exit(2)
		}
		if wfi.Mode()&fs.ModeSymlink != 0 {
			fmt.Fprintf(os.Stderr, "Error: --workspace %q is a symlink; codeweaver v1 does not follow symlinks (resolve the path manually or set CODEWEAVER_FOLLOW_SYMLINKS)\n", absWorkspace)
			os.Exit(2)
		}
		if !wfi.IsDir() {
			fmt.Fprintf(os.Stderr, "Error: --workspace %q is not a directory\n", absWorkspace)
			os.Exit(2)
		}
	}

	// Walk the directory tree.
	// Walk returns paths relative to absRoot.
	relPaths, err := discover.Walk(absRoot)
	if err != nil {
		logger.Error("discover_walk", cwlog.Fields{
			"root":  absRoot,
			"error": err.Error(),
		})
		fmt.Fprintf(os.Stderr, "Error: walk failed: %v\n", err)
		os.Exit(1)
	}

	// If workspace != root, re-express paths relative to workspace.
	// This mirrors --workspace semantics in the parse subcommand: paths in
	// output are relative to the workspace root, not the walk root.
	outputPaths := relPaths
	if absWorkspace != absRoot {
		outputPaths = make([]string, 0, len(relPaths))
		for _, rel := range relPaths {
			// The path is currently relative to absRoot. Convert to absolute,
			// then express relative to absWorkspace.
			abs := filepath.Join(absRoot, filepath.FromSlash(rel))
			wrel, err := filepath.Rel(absWorkspace, abs)
			if err != nil {
				// Path can't be expressed relative to workspace — skip it.
				logger.Warn("discover_rel_path", cwlog.Fields{
					"path":      abs,
					"workspace": absWorkspace,
					"error":     err.Error(),
				})
				continue
			}
			outputPaths = append(outputPaths, filepath.ToSlash(wrel))
		}
	}

	// Emit to stdout.
	// We use bufio.Writer to batch writes. All output goes to cmd.OutOrStdout()
	// so integration tests can capture it without redirection.
	out := bufio.NewWriter(cmd.OutOrStdout())
	for i, p := range outputPaths {
		if nul {
			// NUL-delimited: separator between paths, no trailing NUL.
			// "Like a list with commas between items but no trailing comma."
			if i > 0 {
				if err := out.WriteByte(0); err != nil {
					return err
				}
			}
			if _, err := out.WriteString(p); err != nil {
				return err
			}
		} else {
			// Newline-delimited: one path per line.
			if _, err := fmt.Fprintln(out, p); err != nil {
				return err
			}
		}
	}

	// Flush the output buffer to stdout.
	if err := out.Flush(); err != nil {
		return err
	}

	// Emit RED summary to stderr.
	durationMS := time.Since(start).Milliseconds()
	logger.Info("discover_summary", cwlog.Fields{
		"files_found": len(outputPaths),
		"duration_ms": durationMS,
	})

	return nil
}
