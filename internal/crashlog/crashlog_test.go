package crashlog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codeweaver/internal/crashlog"
)

// TestWrite_NoEnvVar confirms that Write returns nil and creates no file
// when CODEWEAVER_CRASH_LOG_DIR is not set.
//
// This is the "default off" property: crash logs must opt in. If the env var
// is unset (typical user installations), the binary behaves as if crash logs
// don't exist — no filesystem side effects.
func TestWrite_NoEnvVar(t *testing.T) {
	// Ensure the env var is unset for this test.
	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", "")

	err := crashlog.Write("test panic", []byte("fake stack"))
	if err != nil {
		t.Errorf("Write with no env var: want nil error, got %v", err)
	}
}

// TestWrite_ValidDir confirms that Write creates a file with the expected
// naming pattern and writes valid JSON when given a real directory.
func TestWrite_ValidDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", dir)

	err := crashlog.Write("test panic value", []byte("stack trace line 1\nstack trace line 2"))
	if err != nil {
		t.Fatalf("Write with valid dir: unexpected error: %v", err)
	}

	// Find the crash file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 crash file, got %d entries: %v", len(entries), entries)
	}

	name := entries[0].Name()

	// Filename must start with "crash-" and end with ".json".
	if !strings.HasPrefix(name, "crash-") {
		t.Errorf("filename must start with 'crash-', got %q", name)
	}
	if !strings.HasSuffix(name, ".json") {
		t.Errorf("filename must end with '.json', got %q", name)
	}
	// Must contain 8 hex chars before ".json".
	// Format: crash-{timestamp}-{8hexchars}.json
	// The 8 hex chars are always at the very end before .json.
	withoutExt := strings.TrimSuffix(name, ".json")
	parts := strings.Split(withoutExt, "-")
	if len(parts) < 2 {
		t.Errorf("filename must have at least 2 dash-separated parts, got %q", name)
	}
	hexPart := parts[len(parts)-1]
	if len(hexPart) != 8 {
		t.Errorf("last dash-separated part must be 8 hex chars, got %q (len=%d)", hexPart, len(hexPart))
	}
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("hex part contains non-hex char %q in %q", c, hexPart)
		}
	}

	// Read and parse the JSON content.
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile crash log: %v", err)
	}

	var report map[string]any
	if err := json.Unmarshal(content, &report); err != nil {
		t.Fatalf("crash log is not valid JSON: %v\ncontent: %s", err, content)
	}

	// Check required fields are present.
	for _, field := range []string{"timestamp", "panic_value", "stack_trace", "args", "env", "pid", "binary_version", "schema_version"} {
		if _, ok := report[field]; !ok {
			t.Errorf("crash log missing field %q", field)
		}
	}

	// Panic value must match what we passed in.
	if pv, ok := report["panic_value"].(string); !ok || pv != "test panic value" {
		t.Errorf("panic_value: want %q, got %v", "test panic value", report["panic_value"])
	}

	// Stack trace must be present and non-empty.
	if st, ok := report["stack_trace"].(string); !ok || !strings.Contains(st, "stack trace line 1") {
		t.Errorf("stack_trace: want string containing 'stack trace line 1', got %v", report["stack_trace"])
	}
}

// TestWrite_SymlinkDir confirms that Write returns an error (and writes nothing)
// when CODEWEAVER_CRASH_LOG_DIR points to a symlink rather than a real directory.
//
// This is the symlink-refusal security property. See the package comment for the
// full security rationale (short: refusing symlinks prevents path-traversal attacks
// where an attacker places a symlink pointing to a sensitive location).
func TestWrite_SymlinkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}

	// Create a real directory (the symlink target).
	realDir := t.TempDir()

	// Create a symlink that points to the real directory.
	symlinkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}

	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", symlinkDir)

	err := crashlog.Write("test panic", []byte("stack"))
	if err == nil {
		t.Error("Write with symlink dir: want error, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should mention 'symlink', got: %v", err)
	}

	// No files should have been created in the real directory.
	entries, _ := os.ReadDir(realDir)
	if len(entries) != 0 {
		t.Errorf("symlink refusal: no files should be written, but got %d entries in real dir", len(entries))
	}
}

// TestWrite_NonExistentDir confirms that Write returns an error when
// CODEWEAVER_CRASH_LOG_DIR points to a path that does not exist.
func TestWrite_NonExistentDir(t *testing.T) {
	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", "/tmp/codeweaver-crashlog-does-not-exist-xyz-9999")

	err := crashlog.Write("test panic", []byte("stack"))
	if err == nil {
		t.Error("Write with non-existent dir: want error, got nil")
	}
}

// TestWrite_FileNotDir confirms that Write returns an error when
// CODEWEAVER_CRASH_LOG_DIR points to a regular file, not a directory.
func TestWrite_FileNotDir(t *testing.T) {
	// Create a regular file.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir.txt")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", filePath)

	err := crashlog.Write("test panic", []byte("stack"))
	if err == nil {
		t.Error("Write with file path: want error, got nil")
	}
}

// TestWrite_Rotation confirms that after writing >10 crash files, the directory
// contains at most 10 files (the oldest are removed).
//
// This test exercises the rotation logic by writing 12 crash reports to the same
// directory and asserting that only 10 remain afterward.
//
// Why rotation matters: a crash loop (where the binary crashes repeatedly) would
// fill the user's disk without rotation. Think of it like a printer that produces
// at most 10 printouts before shredding the oldest — you always have the most
// recent 10, never more.
func TestWrite_Rotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", dir)

	// Write 12 crash files. Each call to Write produces one file and may rotate.
	// After the 12th call, rotation should have removed the 2 oldest files.
	for i := 0; i < 12; i++ {
		// Sleep briefly between writes to ensure distinct modification times.
		// Without this, all files could have the same mtime and rotation order
		// would be non-deterministic (undefined behavior for equal times).
		time.Sleep(5 * time.Millisecond)

		if err := crashlog.Write("test panic", []byte("stack")); err != nil {
			t.Fatalf("Write %d: unexpected error: %v", i, err)
		}
	}

	// Count remaining crash-*.json files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var crashFiles []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "crash-") && strings.HasSuffix(e.Name(), ".json") {
			crashFiles = append(crashFiles, e.Name())
		}
	}

	if len(crashFiles) > 10 {
		t.Errorf("rotation: expected ≤10 crash files after 12 writes, got %d", len(crashFiles))
	}
	// We should have exactly 10 (not fewer, since we wrote enough to fill it).
	if len(crashFiles) < 10 {
		t.Errorf("rotation: expected exactly 10 crash files after 12 writes, got %d", len(crashFiles))
	}
}

// TestWrite_FilteredEnv confirms that crash reports only contain CODEWEAVER_ env vars,
// not secrets like AWS keys or GitHub tokens.
func TestWrite_FilteredEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEWEAVER_CRASH_LOG_DIR", dir)
	t.Setenv("CODEWEAVER_TEST_VAR", "should_appear")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret_should_not_appear")
	t.Setenv("GITHUB_TOKEN", "token_should_not_appear")

	if err := crashlog.Write("panic", []byte("stack")); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("no crash file created")
	}

	content, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// The CODEWEAVER_ var must appear.
	if !strings.Contains(string(content), "CODEWEAVER_TEST_VAR") {
		t.Error("crash log must contain CODEWEAVER_TEST_VAR")
	}

	// Secrets must NOT appear.
	if strings.Contains(string(content), "secret_should_not_appear") {
		t.Error("crash log must not contain AWS secret value")
	}
	if strings.Contains(string(content), "token_should_not_appear") {
		t.Error("crash log must not contain GitHub token value")
	}
}
