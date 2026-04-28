#!/usr/bin/env bash
# fitness-layering-invariant.sh — layering invariant fitness function.
#
# What it checks (two rules):
#
# Rule 1 — sibling isolation:
#   Only internal/cli/* may import the language-specific packages
#   (internal/parser/python or internal/parser/typescript).
#   No other internal package — not internal/schema, internal/discover,
#   internal/log, internal/limits, internal/crashlog, or any future package —
#   may import a language package directly.
#
#   Think of it like a hospital floor plan: the reception desk (cli/) can call
#   any specialist ward (python, typescript). But the hospital's records system
#   (schema/), pharmacy (limits/), and every other department must not take
#   direct dependencies on the specialist wards — they go through reception.
#
# Rule 2 — inverse (cross-language isolation):
#   internal/parser/python must not import internal/parser/typescript, and
#   vice versa. Today neither does. This rule makes that invariant explicit and
#   caught early, before a future refactor silently couples the two parsers.
#
# Why both rules matter:
#   - Rule 1 prevents "god layer" creep: a single language-agnostic package that
#     knows about every language is hard to extend and hard to test. New languages
#     should cost one new internal/parser/<lang> package, no changes elsewhere.
#   - Rule 2 prevents cross-language coupling that would defeat Rule 1's purpose
#     and complicate independent language-parser testing.
#
# The dependency direction must always be:
#
#   internal/parser/{python,typescript}   — language packages (leaf nodes)
#          ^
#          |  allowed: language packages import root parser types
#          |
#   internal/parser/{resolve,types,dispatch}  — language-agnostic layer
#          ^
#          |  allowed: cli imports root parser layer AND language packages
#          |
#   internal/cli/*                            — CLI layer (ONLY allowed importer
#                                              of language packages)
#
# Run manually: ./scripts/fitness-layering-invariant.sh
# Run from Makefile: make layering-invariant
#
# Exit 0: layering invariant clean.
# Exit 1: violation(s) found; offending lines printed.

set -euo pipefail

cd "$(dirname "$0")/.."

violations=""

# ---------------------------------------------------------------------------
# Rule 1: sibling isolation
# No internal package outside internal/cli/* and the language packages
# themselves may import internal/parser/python or internal/parser/typescript.
#
# grep searches all *.go (excluding *_test.go) under internal/ for lines that
# import a language package, then filters OUT:
#   - internal/cli/      (the permitted importer)
#   - internal/parser/python/     (self-referential: a file within python/ may
#   - internal/parser/typescript/ (self-referential: a file within typescript/ may
#                                  import its own package's sub-files — not applicable
#                                  in Go, but excluding them avoids false positives if
#                                  the layout ever grows nested packages)
# ---------------------------------------------------------------------------
sibling_violations=$(grep -rEn 'codeweaver/internal/parser/(python|typescript)' \
    --include='*.go' --exclude='*_test.go' \
    internal/ \
    | grep -vE '^internal/cli/' \
    | grep -vE '^internal/parser/(python|typescript)/' \
    || true)

if [ -n "$sibling_violations" ]; then
    violations="${violations}Rule 1 (sibling-isolation) VIOLATED:
  Only internal/cli/* may import language packages, but found:

${sibling_violations}

  Fix: language-specific logic must live in internal/cli/ or in the language
  package itself. If language-agnostic behaviour is needed, express it as an
  interface method on parser.FileResult rather than a direct package import.

"
fi

# ---------------------------------------------------------------------------
# Rule 2: inverse (cross-language isolation)
# Neither internal/parser/python nor internal/parser/typescript may import
# the other. Both are leaf nodes in the dependency graph.
# ---------------------------------------------------------------------------
python_to_ts=$(grep -rEn 'codeweaver/internal/parser/typescript' \
    --include='*.go' --exclude='*_test.go' \
    internal/parser/python/ \
    || true)

ts_to_python=$(grep -rEn 'codeweaver/internal/parser/python' \
    --include='*.go' --exclude='*_test.go' \
    internal/parser/typescript/ \
    || true)

if [ -n "$python_to_ts" ] || [ -n "$ts_to_python" ]; then
    violations="${violations}Rule 2 (cross-language coupling) VIOLATED:
  Language packages must not import each other:

${python_to_ts}${ts_to_python}

  Fix: shared behaviour must move to the language-agnostic layer
  (internal/parser/resolve.go, types.go, or dispatch.go) as an interface or
  type, not as a direct cross-language import.

"
fi

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
if [ -n "$violations" ]; then
    echo "layering-invariant: FAIL"
    echo ""
    echo "$violations"
    exit 1
fi

echo "layering-invariant: clean."
echo "  Rule 1 (sibling isolation): internal/cli/* is the only permitted importer"
echo "    of internal/parser/(python|typescript). No other internal package imports"
echo "    a language package."
echo "  Rule 2 (cross-language isolation): internal/parser/python and"
echo "    internal/parser/typescript do not import each other."
exit 0
