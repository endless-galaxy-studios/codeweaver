#!/usr/bin/env bash
# check-stdout-invariant.sh — enforce the narrowed stdout invariant.
#
# The rule: no fmt.Print* calls that write to os.Stdout outside of:
#   - internal/cli/version.go  (human-readable version output — explicitly exempt)
#   - internal/cli/root.go     (completion output — explicitly exempt)
#   - cmd/codeweaver/main.go   (panic recovery — exempt; panic output goes to stderr)
#   - _test.go files           (tests may print to stdout freely)
#   - cmd/schema-gen/          (build tool, not production binary)
#
# Direct os.Stdout.Write calls in internal/cli/parse.go are ALLOWED — they are the
# single-buffered JSON write path that constitutes the output schema.
#
# This script catches accidental fmt.Println/Printf/Print calls in packages that
# should never touch stdout — the most common way the invariant is accidentally violated.
#
# Exit 0: invariant holds.
# Exit 1: violation found; print offending lines.

set -euo pipefail

VIOLATIONS=0

# Patterns that write to stdout directly.
# We check for bare fmt.Print* (which goes to stdout by default).
# fmt.Fprintf(os.Stdout, ...) is also caught.
check_file() {
    local file="$1"
    local exempt="$2"

    if [[ "$exempt" == "true" ]]; then
        return 0
    fi

    # Check 1: bare fmt.Print / fmt.Printf / fmt.Println — these always write to
    # stdout with no io.Writer argument. Uses POSIX ERE (-E) so (a|b|c) is true
    # alternation — no backslash escaping needed.
    # fmt.Fprint* are excluded here because they take an explicit io.Writer first
    # argument; when that argument is os.Stderr they are legitimate (caught below
    # only when the argument is os.Stdout).
    if grep -nE 'fmt\.(Print|Printf|Println)\(' "$file" 2>/dev/null | grep -v '//.*fmt\.'; then
        echo "VIOLATION: $file contains fmt.Print/Printf/Println (writes to stdout by default)"
        VIOLATIONS=$((VIOLATIONS + 1))
    fi

    # Check 2: fmt.Fprint*(os.Stdout, ...) — explicit stdout writes via the
    # writer-argument family. Uses ERE to cover all three variants in one pass.
    if grep -nE 'fmt\.(Fprint|Fprintf|Fprintln)\(os\.Stdout' "$file" 2>/dev/null | grep -v '//.*fmt\.'; then
        echo "VIOLATION: $file contains fmt.Fprint*(os.Stdout, ...) — use contract package for JSON output"
        VIOLATIONS=$((VIOLATIONS + 1))
    fi
}

# Walk all .go files, applying exemptions.
while IFS= read -r -d '' file; do
    # Skip test files
    if [[ "$file" == *_test.go ]]; then
        continue
    fi

    # Skip exempt files
    case "$file" in
        */internal/cli/version.go) continue ;;  # human-readable version — exempt
        */internal/cli/root.go) continue ;;     # completion — exempt
        */cmd/codeweaver/main.go) continue ;;   # panic recovery stderr — exempt
        */cmd/schema-gen/*) continue ;;         # build tool — exempt
    esac

    check_file "$file" "false"
done < <(find . -name '*.go' -print0 2>/dev/null)

if [[ "$VIOLATIONS" -gt 0 ]]; then
    echo ""
    echo "FAIL: $VIOLATIONS stdout invariant violation(s) found."
    echo "  Stdout is the JSON contract. Use internal/log for stderr logging."
    echo "  Use internal/schema + os.Stdout.Write() for JSON output."
    exit 1
fi

echo "OK: stdout invariant check passed."
exit 0
