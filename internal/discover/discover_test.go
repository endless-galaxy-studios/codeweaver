package discover_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"codeweaver/internal/discover"
)

// TestSkipDirs_Canonical asserts that SkipDirs() contains exactly the expected
// set of directory names — no more, no fewer.
//
// Ground-truth copy of the SkipDirs() set, hardcoded so a regression in the
// production code (accidental addition or removal of an entry) is caught by
// this test. If SkipDirs() changes, update this ground-truth copy too.
func TestSkipDirs_Canonical(t *testing.T) {
	// Ground-truth copy of the SkipDirs() set, hardcoded so a regression in the
	// production code is caught by this test.
	canonicalSkipDirs := []string{
		"node_modules",
		".next",
		".nuxt",
		"dist",
		"build",
		"out",
		".turbo",
		".cache",
		"__pycache__",
		".git",
		"coverage",
		".nyc_output",
		".venv",
		"venv",
		"env",
		".env",
		".mypy_cache",
		".ruff_cache",
		".pytest_cache",
	}

	goSkipDirs := discover.SkipDirs()

	// Assert same count.
	if len(goSkipDirs) != len(canonicalSkipDirs) {
		t.Errorf("SkipDirs count mismatch: got=%d want=%d\ngot set: %v", len(goSkipDirs), len(canonicalSkipDirs), keysOf(goSkipDirs))
	}

	// Assert every canonical entry is present in SkipDirs().
	for _, name := range canonicalSkipDirs {
		if _, ok := goSkipDirs[name]; !ok {
			t.Errorf("canonical entry %q is missing from SkipDirs()", name)
		}
	}

	// Reverse check: assert every SkipDirs() entry is in the canonical set (catches additions).
	canonicalSet := make(map[string]struct{}, len(canonicalSkipDirs))
	for _, name := range canonicalSkipDirs {
		canonicalSet[name] = struct{}{}
	}
	for name := range goSkipDirs {
		if _, ok := canonicalSet[name]; !ok {
			t.Errorf("SkipDirs() entry %q is not in the ground-truth set defined in this test", name)
		}
	}
}

// TestWalk_Empty verifies that Walk on an empty directory returns an empty slice and no error.
func TestWalk_Empty(t *testing.T) {
	root := t.TempDir()

	paths, err := discover.Walk(root)
	if err != nil {
		t.Fatalf("Walk empty dir: unexpected error: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("Walk empty dir: expected 0 paths, got %d: %v", len(paths), paths)
	}
}

// TestWalk_FlatFiles verifies that Walk returns only files with parseable extensions
// from a flat directory (a.py, b.ts, c.txt, d.tsx, e.mts → expect [a.py, b.ts, d.tsx, e.mts]).
func TestWalk_FlatFiles(t *testing.T) {
	root := t.TempDir()

	mustWrite(t, root, "a.py", "x = 1")
	mustWrite(t, root, "b.ts", "const x = 1")
	mustWrite(t, root, "c.txt", "plain text")
	mustWrite(t, root, "d.tsx", "const x = <div/>")
	mustWrite(t, root, "e.mts", "export const x = 1")

	paths, err := discover.Walk(root)
	if err != nil {
		t.Fatalf("Walk flat files: unexpected error: %v", err)
	}

	want := []string{"a.py", "b.ts", "d.tsx", "e.mts"}
	if !stringSliceEqual(paths, want) {
		t.Errorf("Walk flat files:\n  got:  %v\n  want: %v", paths, want)
	}
}

// TestWalk_SkipsDirs verifies that directories in SkipDirs() are pruned entirely.
// node_modules/x.ts and .git/HEAD must not appear; src/y.ts must appear.
func TestWalk_SkipsDirs(t *testing.T) {
	root := t.TempDir()

	mustWrite(t, filepath.Join(root, "node_modules"), "x.ts", "// skip me")
	mustWrite(t, filepath.Join(root, ".git"), "HEAD", "ref: refs/heads/main")
	mustWrite(t, filepath.Join(root, "src"), "y.ts", "export const y = 1")

	paths, err := discover.Walk(root)
	if err != nil {
		t.Fatalf("Walk skip dirs: unexpected error: %v", err)
	}

	want := []string{"src/y.ts"}
	if !stringSliceEqual(paths, want) {
		t.Errorf("Walk skip dirs:\n  got:  %v\n  want: %v", paths, want)
	}
}

// TestWalk_NestedSkipDir verifies that a skip dir nested inside a non-skip dir is pruned.
// pkg/.venv/x.py must not appear; pkg/main.py must appear.
func TestWalk_NestedSkipDir(t *testing.T) {
	root := t.TempDir()

	mustWrite(t, filepath.Join(root, "pkg", ".venv"), "x.py", "# inside venv")
	mustWrite(t, filepath.Join(root, "pkg"), "main.py", "def main(): pass")

	paths, err := discover.Walk(root)
	if err != nil {
		t.Fatalf("Walk nested skip: unexpected error: %v", err)
	}

	want := []string{"pkg/main.py"}
	if !stringSliceEqual(paths, want) {
		t.Errorf("Walk nested skip:\n  got:  %v\n  want: %v", paths, want)
	}
}

// TestWalk_RelativePaths asserts that returned paths are relative to root, not absolute.
func TestWalk_RelativePaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "a.py", "x = 1")

	paths, err := discover.Walk(root)
	if err != nil {
		t.Fatalf("Walk relative: unexpected error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("Walk relative: expected 1 path, got %d: %v", len(paths), paths)
	}

	p := paths[0]
	// Must not be absolute.
	if filepath.IsAbs(p) {
		t.Errorf("path must be relative to root, got absolute: %q", p)
	}
	// Must not start with the root prefix.
	if len(p) >= len(root) && p[:len(root)] == root {
		t.Errorf("path contains root prefix: %q", p)
	}
	// Expected value.
	if p != "a.py" {
		t.Errorf("expected path %q, got %q", "a.py", p)
	}
}

// TestWalk_LexicographicallySorted asserts the returned paths are sorted regardless
// of the order in which files were created.
func TestWalk_LexicographicallySorted(t *testing.T) {
	root := t.TempDir()

	// Create files in deliberately non-sorted order.
	files := []string{"z.py", "a.py", "m.ts", "b.tsx"}
	for _, f := range files {
		mustWrite(t, root, f, "# content")
	}

	paths, err := discover.Walk(root)
	if err != nil {
		t.Fatalf("Walk sorted: unexpected error: %v", err)
	}

	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.Strings(sorted)

	if !stringSliceEqual(paths, sorted) {
		t.Errorf("Walk result is not lexicographically sorted:\n  got:  %v\n  want: %v", paths, sorted)
	}
}

// TestWalk_SymlinkNotFollowed asserts that a symlink pointing outside the walk root
// is not followed. The target file must not appear in the output.
//
// This test is skipped on Windows where symlink creation requires elevated privileges
// in many configurations.
func TestWalk_SymlinkNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows; skip policy test")
	}

	// outside_workspace: a separate directory with a Python file.
	outside := t.TempDir()
	mustWrite(t, outside, "secret.py", "# should not appear")

	// inside: the walk root, with a symlink pointing at the outside directory.
	inside := t.TempDir()
	linkPath := filepath.Join(inside, "link")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}

	// Also create a real file in inside so we verify the walk works at all.
	mustWrite(t, inside, "real.py", "# real file")

	paths, err := discover.Walk(inside)
	if err != nil {
		t.Fatalf("Walk symlink: unexpected error: %v", err)
	}

	// The symlink-pointed-to file must not appear.
	for _, p := range paths {
		if p == "link/secret.py" || p == filepath.Join("link", "secret.py") {
			t.Errorf("symlink target file appeared in Walk output: %q", p)
		}
	}

	// The real file must appear.
	if !stringSliceContains(paths, "real.py") {
		t.Errorf("real.py not found in Walk output: %v", paths)
	}
}

// TestWalk_SymlinkOutsideWorkspace_Excluded is the canonical AC test for the
// wave 4d symlink-boundary requirement. It verifies that a symlinked directory
// pointing outside the workspace root is not walked, so files inside the symlink
// target never appear in Walk output.
//
// This test exercises the explicit boundary policy documented in the package godoc:
// "Walk does NOT follow symlinks … a symlink pointing to a file or directory OUTSIDE
// the workspace root cannot cause Walk to discover out-of-scope files."
//
// Skipped on Windows where symlink creation requires elevated privileges.
func TestWalk_SymlinkOutsideWorkspace_Excluded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows; skip boundary test")
	}

	// Create files outside the workspace.
	outside := t.TempDir()
	mustWrite(t, outside, "secret.py", "# outside the workspace; must never appear")
	mustWrite(t, outside, "private.ts", "// also outside")

	// Create the workspace root with:
	//   - a real file (must appear in Walk output)
	//   - a symlink directory pointing outside (must NOT be descended)
	workspace := t.TempDir()
	mustWrite(t, workspace, "legit.py", "# inside workspace; must appear")
	// Create a symlink named "outsideLink" inside workspace pointing to outside.
	linkPath := filepath.Join(workspace, "outsideLink")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}

	paths, err := discover.Walk(workspace)
	if err != nil {
		t.Fatalf("Walk: unexpected error: %v", err)
	}

	// Files reached via the symlink must NOT appear.
	for _, p := range paths {
		if p == "outsideLink/secret.py" || p == filepath.Join("outsideLink", "secret.py") {
			t.Errorf("symlink target file secret.py appeared in Walk output: %q", p)
		}
		if p == "outsideLink/private.ts" || p == filepath.Join("outsideLink", "private.ts") {
			t.Errorf("symlink target file private.ts appeared in Walk output: %q", p)
		}
		// Reject any path that starts with the symlink name.
		if strings.HasPrefix(p, "outsideLink") {
			t.Errorf("path via symlink appeared in Walk output: %q", p)
		}
	}

	// The real file inside the workspace must appear.
	if !stringSliceContains(paths, "legit.py") {
		t.Errorf("expected legit.py in Walk output, got: %v", paths)
	}
}

// --- helpers ---

// mustWrite creates a file at dir/name with the given content,
// creating parent directories as needed.
func mustWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mustWrite MkdirAll %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("mustWrite WriteFile %s: %v", path, err)
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringSliceContains(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}

func keysOf(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
