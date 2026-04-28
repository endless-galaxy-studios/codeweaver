// Package crashlog writes timestamped JSON crash files to a configurable directory.
//
// # What is a crash log?
//
// When codeweaver panics (an unexpected runtime error — think of it like the program
// tripping over its own feet), it catches that panic via a top-level defer/recover and
// converts it into a clean exit with code 4. Before exiting, if CODEWEAVER_CRASH_LOG_DIR
// is set, it writes a structured JSON file describing the crash event. This is analogous
// to an operating system's crash dump, but much smaller and in a format developers can
// read directly.
//
// # Why a separate package from internal/log?
//
// The structured logger (internal/log) writes to stderr as part of the normal run.
// The crash log is different: it is written directly to disk (bypassing the logger)
// and is intended for post-mortem analysis by developers, not for real-time consumption
// by the plugin. Crash logs survive after the binary exits; stderr does not.
//
// # Security properties
//
// The crash log directory is validated with several checks before writing:
//  1. The path must exist and must be a real directory (not a symlink).
//     Refusing to write through symlinks prevents a class of path-traversal attacks
//     where an attacker pre-places a symlink pointing to a sensitive location.
//     Think of it like delivering a package: you only deliver to the actual address on
//     the label, not an address that someone has taped over it with a forwarding note.
//  2. The file is opened with O_EXCL (exclusive create). This means "create this file,
//     but fail if it already exists." A real-world analogy: a notary who will only
//     stamp a document that comes from a blank page, not one that was pre-filled.
//     Without O_EXCL, an attacker who predicts the filename could pre-create a file
//     and cause the crash log to overwrite it.
//  3. Only env vars prefixed with CODEWEAVER_ are included in crash reports. The full
//     process environment often contains secrets (AWS keys, tokens, passwords). Including
//     it all would turn every crash into a potential credential leak.
//
// # Rotation
//
// After writing a new crash file, the directory is capped at 10 crash-*.json files.
// Oldest files (by modification time) are removed first. This prevents the crash log
// directory from filling the user's disk in the event of a crash loop. Think of it
// like a hotel that only keeps the last 10 check-in records — older ones are shredded
// to save space.
//
// Rotation failures are logged to stderr but not propagated: rotation is best-effort.
// A crash log that's not rotated is better than no crash log at all.
package crashlog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// crashReport is the structure written to each crash file.
// All fields use snake_case JSON names for consistency with the rest of the output schema.
type crashReport struct {
	Timestamp       string            `json:"timestamp"`
	PanicValue      string            `json:"panic_value"`
	StackTrace      string            `json:"stack_trace"`
	Args            []string          `json:"args"`
	Env             map[string]string `json:"env"`
	PID             int               `json:"pid"`
	BinaryVersion   string            `json:"binary_version"`
	SchemaVersion   string            `json:"schema_version"`
}

// Write writes a JSON crash report to the directory specified by the
// CODEWEAVER_CRASH_LOG_DIR environment variable.
//
// Returns nil and writes nothing if the environment variable is unset or empty.
// Returns an error if the directory exists but is invalid (symlink, regular file,
// or unwritable).
//
// The panicValue is the value recovered from a panic (any type — converted to string).
// The stack is the raw goroutine stack trace bytes from runtime/debug.Stack().
//
// Filename format: "crash-{RFC3339Nano}-{8hexchars}.json"
// The RFC3339Nano timestamp provides chronological sort order; the 8 hex chars
// (from crypto/rand) provide uniqueness within the same nanosecond and prevent
// filename collisions in concurrent-crash scenarios.
//
// After writing, rotation is attempted: if the directory contains >10 crash-*.json
// files, the oldest by modification time are deleted until 10 remain.
// Rotation failures are logged to os.Stderr but not returned as errors.
func Write(panicValue any, stack []byte) error {
	// Step 1: check if crash log dir is configured.
	crashDir := os.Getenv("CODEWEAVER_CRASH_LOG_DIR")
	if crashDir == "" {
		// Default off: no env var, no crash log. This is intentional.
		return nil
	}

	// Step 2: validate the directory.
	// We use Lstat (not Stat) because Stat follows symlinks — Lstat gives us the
	// mode bits of the path itself. If the path is a symlink, Lstat tells us that;
	// Stat would silently follow it to the target and we'd never know.
	fi, err := os.Lstat(crashDir)
	if err != nil {
		return fmt.Errorf("crashlog: stat crash log dir %q: %w", crashDir, err)
	}

	// Reject symlinks. See package comment for the security rationale.
	// fs.ModeSymlink is the mode bit indicating "this entry is a symlink."
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("crashlog: crash log dir %q is a symlink; refusing to write (mitigates path-traversal)", crashDir)
	}

	// Must be a directory, not a regular file or other special type.
	if !fi.IsDir() {
		return fmt.Errorf("crashlog: crash log dir %q is not a directory (mode: %s)", crashDir, fi.Mode())
	}

	// Step 3: generate the filename.
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	// Replace colons and dots in the timestamp with safe filename chars.
	// On macOS/Linux colons are fine in filenames, but dots-in-nanoseconds and
	// "+" timezone offsets could confuse some tools. Replace: → - and . → -
	// to produce a clean filename like crash-2026-04-27T12-34-56-123456789Z-a1b2c3d4.json
	safeTS := strings.NewReplacer(":", "-", ".", "-", "+", "plus").Replace(ts)

	randBytes := make([]byte, 4) // 4 bytes = 8 hex chars
	if _, randErr := rand.Read(randBytes); randErr != nil {
		// If crypto/rand fails (extremely rare), use a timestamp-based fallback.
		// We still want a crash log even if random generation fails.
		randBytes = []byte{
			byte(time.Now().UnixNano() & 0xff),
			byte((time.Now().UnixNano() >> 8) & 0xff),
			byte((time.Now().UnixNano() >> 16) & 0xff),
			byte((time.Now().UnixNano() >> 24) & 0xff),
		}
	}
	randHex := hex.EncodeToString(randBytes)
	filename := fmt.Sprintf("crash-%s-%s.json", safeTS, randHex)
	fullPath := filepath.Join(crashDir, filename)

	// Step 4: open the file with O_EXCL (exclusive create).
	// O_CREATE: create the file if it doesn't exist.
	// O_EXCL:   fail if the file already exists (prevents pre-creation races).
	// O_WRONLY: we only write, never read.
	// 0o600:    owner-readable/writable only; crash reports may contain sensitive info.
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("crashlog: create crash log file %q: %w", fullPath, err)
	}
	defer f.Close()

	// Step 5: build and write the crash report.
	report := crashReport{
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		PanicValue:      fmt.Sprintf("%v", panicValue),
		StackTrace:      string(stack),
		Args:            os.Args,
		Env:             filteredEnv(),
		PID:             os.Getpid(),
		BinaryVersion:   binaryVersion,
		SchemaVersion: schemaVersion,
	}

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(report); err != nil {
		// We already opened the file; attempt to remove it to avoid leaving a corrupt file.
		_ = os.Remove(fullPath)
		return fmt.Errorf("crashlog: encode crash report: %w", err)
	}

	// Step 6: rotate — keep at most 10 crash files, remove oldest first.
	// This is best-effort: rotation failures are logged to stderr but not returned.
	if rotErr := rotateCrashFiles(crashDir, 10); rotErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "crashlog: rotation failed: %v\n", rotErr)
	}

	return nil
}

// filteredEnv returns environment variables whose names start with "CODEWEAVER_".
// This deliberately excludes secrets like AWS_*, GITHUB_TOKEN, OPENAI_API_KEY, etc.
// The result is sorted by key for deterministic output (avoids map-ordering surprises).
//
// Real-life analogy: if you file a police report about your stolen car, you include
// the car's make and model (relevant), not your home safe combination (irrelevant and
// dangerous to include).
func filteredEnv() map[string]string {
	env := os.Environ()
	result := make(map[string]string, 8) // most invocations have <10 CODEWEAVER_ vars
	for _, kv := range env {
		if strings.HasPrefix(kv, "CODEWEAVER_") {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				result[parts[0]] = parts[1]
			} else {
				// Key with no value (e.g. "CODEWEAVER_FLAG=").
				result[parts[0]] = ""
			}
		}
	}
	return result
}

// rotateCrashFiles removes the oldest crash-*.json files from dir if there are
// more than max. Files are sorted by modification time; the oldest are removed first.
//
// Returns nil if rotation was not needed or completed successfully.
// Returns an error only if listing the directory itself fails.
func rotateCrashFiles(dir string, max int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read crash log dir: %w", err)
	}

	// Filter to crash-*.json files only.
	type crashFile struct {
		name    string
		modTime time.Time
	}
	var files []crashFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "crash-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue // skip unreadable entries
		}
		files = append(files, crashFile{name: name, modTime: info.ModTime()})
	}

	if len(files) <= max {
		return nil
	}

	// Sort by modification time, oldest first.
	// This is like sorting photographs by date to decide which ones to discard.
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	// Remove oldest files until count <= max.
	toRemove := len(files) - max
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(dir, files[i].name)
		if removeErr := os.Remove(path); removeErr != nil {
			// Log but don't fail — partial rotation is better than no rotation.
			_, _ = fmt.Fprintf(os.Stderr, "crashlog: remove old crash file %q: %v\n", path, removeErr)
		}
	}

	return nil
}

// binaryVersion and schemaVersion are package-level vars that can be set by
// the main package at startup via SetVersionInfo. This avoids an import cycle
// (main → crashlog → schema would be fine, but crashlog → schema is a
// legitimate dependency; we use explicit setters to keep the package testable
// without needing schema's constants in the test binary).
var (
	binaryVersion = "dev"
	schemaVersion = "1.0"
)

// SetVersionInfo injects version strings into the crash report.
// Call this from main() before any panic recovery is possible.
// This is how the crash report knows which binary version was running.
func SetVersionInfo(binary, sv string) {
	binaryVersion = binary
	schemaVersion = sv
}
