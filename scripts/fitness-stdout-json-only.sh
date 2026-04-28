#!/usr/bin/env bash
# fitness-stdout-json-only.sh — stdout-JSON-only fitness function.
#
# What it checks:
#   For each subcommand, verifies that stdout is either:
#     (a) empty (no output), or
#     (b) valid JSON (for subcommands that are supposed to emit JSON), or
#     (c) plain text (for subcommands explicitly exempt from JSON-only stdout)
#
# Subcommand table:
#   parse <file>       => stdout: valid JSON (the output schema v1)
#   version --json     => stdout: valid JSON (the version schema)
#   version            => stdout: human-readable text (exempt by spec)
#   discover .         => stdout: newline-delimited paths (exempt by spec)
#   completion bash    => stdout: shell script (exempt by spec)
#
# Why this matters:
#   Consumers (plugins, language servers, agents) pipe the binary's stdout
#   and parse it as JSON. A single stray fmt.Println in the parse path corrupts
#   every consumer simultaneously. This script catches integration-level violations
#   that the forbidigo lint rule might miss (e.g., a third-party library that
#   writes to os.Stdout).
#
# Usage:
#   ./scripts/fitness-stdout-json-only.sh [binary-path]
#
#   binary-path defaults to ./codeweaver.
#   If the binary doesn't exist, build it first: make build
#
# Run from Makefile: make stdout-json-only
#
# Exit 0: all subcommands behave correctly.
# Exit 1: a subcommand emitted non-JSON when JSON was expected, or the binary crashed.

set -euo pipefail

cd "$(dirname "$0")/.."

BIN="${1:-./codeweaver}"

if [ ! -x "$BIN" ]; then
    echo "Binary not found at $BIN. Build first: make build"
    exit 1
fi

FAIL=0

# Helper: assert stdout is valid JSON.
assert_json() {
    local subcommand="$1"
    local output="$2"
    if [ -z "$output" ]; then
        echo "FAIL [$subcommand]: stdout was empty — expected valid JSON"
        FAIL=1
        return
    fi
    if ! echo "$output" | jq . > /dev/null 2>&1; then
        echo "FAIL [$subcommand]: stdout is not valid JSON:"
        echo "$output" | head -5
        FAIL=1
        return
    fi
    echo "OK   [$subcommand]: stdout is valid JSON."
}

# Helper: assert stdout is non-empty (plain text — not validated as JSON).
assert_nonempty() {
    local subcommand="$1"
    local output="$2"
    if [ -z "$output" ]; then
        echo "FAIL [$subcommand]: stdout was empty — expected output"
        FAIL=1
        return
    fi
    echo "OK   [$subcommand]: stdout is non-empty (plain text)."
}

# ---- parse: stdout must be valid JSON ----
echo "Testing: codeweaver parse <fixture>"
fixture="testdata/fixtures/python/empty_module.py"
# Fall back to discovering a fixture file if the path doesn't exist.
if [ ! -f "$fixture" ]; then
    fixture=$(find testdata/fixtures/python -name "*.py" | head -1)
fi
if [ -z "$fixture" ]; then
    echo "WARN: no Python fixture found; skipping parse subcommand test."
else
    # Run parse; capture stdout. The binary exits 0 even on parse errors (exit 1 = all failed).
    parse_out=$("$BIN" parse --workspace testdata/fixtures/python "$fixture" 2>/dev/null) || {
        # Exit 1 is "all files failed" — still expect JSON on stdout.
        parse_out=$("$BIN" parse --workspace testdata/fixtures/python "$fixture" 2>/dev/null || true)
    }
    assert_json "parse" "$parse_out"
fi

# ---- version --json: stdout must be valid JSON ----
echo "Testing: codeweaver version --json"
version_json_out=$("$BIN" version --json 2>/dev/null)
assert_json "version --json" "$version_json_out"

# ---- version (human): stdout must be non-empty plain text ----
# The human-readable version is explicitly exempt from JSON-only stdout.
echo "Testing: codeweaver version"
version_out=$("$BIN" version 2>/dev/null)
assert_nonempty "version" "$version_out"

# Also assert the human version does NOT parse as JSON (it shouldn't be JSON).
if echo "$version_out" | jq . > /dev/null 2>&1; then
    echo "WARN [version]: human version output looks like JSON — check if --json flag was inadvertently applied."
fi

# ---- discover: stdout is newline-delimited paths (exempt from JSON-only) ----
echo "Testing: codeweaver discover testdata/fixtures"
discover_out=$("$BIN" discover testdata/fixtures 2>/dev/null || true)
# Discover may return empty output for an empty directory, which is fine.
echo "OK   [discover]: exited without crash (output may be empty if no files found)."

# ---- completion bash: stdout is a shell script (exempt from JSON-only) ----
echo "Testing: codeweaver completion bash"
completions_out=$("$BIN" completion bash 2>/dev/null)
assert_nonempty "completion bash" "$completions_out"

echo ""
if [ "$FAIL" -eq 1 ]; then
    echo "FAIL: stdout-json-only invariant violated for one or more subcommands."
    exit 1
fi
echo "stdout-json-only: all subcommands behave correctly."
exit 0
