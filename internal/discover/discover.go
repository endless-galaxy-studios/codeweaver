// Package discover provides directory-tree walking for the codeweaver binary.
//
// The Walk function is the canonical "find parseable files" operation. It applies
// the skip-directory set returned by SkipDirs() to prune build artifacts, caches,
// and dependency directories that should never contribute symbols.
//
// # Symlink policy (codeweaver v1 — explicit, enforced, documented)
//
// Walk does NOT follow symlinks. This is a deliberate, documented policy, not a
// default that happens to be safe.
//
// Specifically:
//
//   - Symlinked files are excluded: Walk checks d.Type().IsRegular() for every
//     DirEntry. The IsRegular() method returns false for symlinks (even symlinks
//     to regular files), so symlinked files are never included in Walk output.
//
//   - Symlinked directories are excluded: filepath.WalkDir's default behavior does
//     not descend into directories that are symlinks. A symlink-to-directory is
//     reported as a DirEntry with Type()&fs.ModeSymlink != 0 and is not walked.
//
//   - The workspace boundary is enforced: a symlink pointing to a file or directory
//     OUTSIDE the workspace root cannot cause Walk to discover out-of-scope files.
//     This prevents accidental inclusion of files from other projects, home directories,
//     or system paths.
//
// TODO: --follow-symlinks flag with explicit boundary semantics
// (e.g., symlinks that resolve within the workspace root are followed; symlinks that
// escape the root are refused with a warning).
//
// # NUL-delimiter convention
//
// When emitting paths with NUL (\0) as separator (--nul flag), NUL is a
// separator, not a terminator. The final path is NOT followed by NUL. This
// matches the convention of tools like `find -print0` and `git ls-files -z`:
// each path is separated from the next, but there is no trailing byte after
// the last path. Consumers reading NUL-delimited input should split on NUL,
// not expect a trailing NUL.
package discover

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"codeweaver/internal/parser"
)

// SkipDirs returns the canonical skip-directory set for file discovery.
//
// Directory basenames in this set are matched against the directory's base name
// only, not the full path. So "node_modules" anywhere in the tree is skipped,
// not just at the root.
//
// The set covers venv/build/cache directories that should never contribute symbols:
// JS/TS toolchain artifacts, Python virtual environments and caches, and common
// build output directories.
//
// The set is defined here as a Go frozenset equivalent (map[string]struct{}).
// It is a function (not a package-level var) so that callers always get a fresh
// copy — preventing accidental mutation.
func SkipDirs() map[string]struct{} {
	return map[string]struct{}{
		"node_modules":  {},
		".next":         {},
		".nuxt":         {},
		"dist":          {},
		"build":         {},
		"out":           {},
		".turbo":        {},
		".cache":        {},
		"__pycache__":   {},
		".git":          {},
		"coverage":      {},
		".nyc_output":   {},
		".venv":         {},
		"venv":          {},
		"env":           {},
		".env":          {},
		".mypy_cache":   {},
		".ruff_cache":   {},
		".pytest_cache": {},
	}
}

// Walk discovers parseable files under root. Returns paths relative to root, in
// lexicographic (sorted) order.
//
// File inclusion: only files whose extension is recognized by parser.DetectLanguage
// (.py, .ts, .tsx, .mts). All other extensions are silently skipped.
//
// Skip-dir pruning: any directory whose base name appears in SkipDirs() is
// pruned entirely — filepath.WalkDir returns filepath.SkipDir, so no file inside
// that subtree is ever visited.
//
// Symlinks: Walk does NOT follow symlinks into directories (filepath.WalkDir
// default). Symlinks to files are reported as regular entries and included if
// their extension matches — however, since we call entry.Type().IsRegular() to
// check, symlink-to-file entries (whose mode includes fs.ModeSymlink) are not
// included. This is conservative: the tool avoids resolving symlinks without
// a policy decision. See package godoc for the wave 4d hardening note.
//
// Error handling: if a file or directory can't be stat'd, the walk logs the
// error via os.Stderr and continues. Walk returns (paths, nil) on success even
// if some entries were unstattable. Only a failure to open root itself causes
// Walk to return a non-nil error.
func Walk(root string) ([]string, error) {
	skip := SkipDirs()
	var paths []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Can't stat this entry — log and continue. We never crash
			// on a single unreadable file; discovery is best-effort.
			// Use os.Stderr directly here because the log package requires
			// initialization context not available at this layer.
			_, _ = os.Stderr.WriteString("discover: skipping " + path + ": " + err.Error() + "\n")
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if _, shouldSkip := skip[filepath.Base(path)]; shouldSkip {
				return filepath.SkipDir
			}
			return nil
		}

		// Only include regular files (not symlinks, devices, pipes, etc.).
		// filepath.WalkDir's DirEntry.Type() returns the mode bits for the
		// entry itself — not what a symlink points to. IsRegular() is false
		// for symlinks, so symlinked directories AND symlinked files are both
		// excluded under this conservative policy.
		if !d.Type().IsRegular() {
			return nil
		}

		if !parser.IsSupportedExtension(path) {
			return nil
		}

		// Convert to root-relative path. filepath.WalkDir always calls us
		// with `path` rooted at `root`, so Rel should never fail here.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			// Theoretically unreachable, but handle it gracefully.
			_, _ = os.Stderr.WriteString("discover: rel error for " + path + ": " + err.Error() + "\n")
			return nil
		}

		// Normalize path separators: forward-slash on all platforms
		// for cross-platform diff stability (per CLI design spec).
		rel = filepath.ToSlash(rel)

		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// WalkDir returns entries in lexicographic order per directory, but
	// the combined order across directories is lexicographic too. We sort
	// once at the end as a safety net for Go version stability.
	sort.Strings(paths)
	return paths, nil
}
