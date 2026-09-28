#!/usr/bin/env bash
# fitness-no-cgo.sh — no-CGo architectural fitness function.
#
# What it checks:
#   1. CGO_ENABLED=0 go build ./... succeeds (the binary builds without CGo).
#   2. No transitive Go dependency has CgoFiles or CXXFiles (zero CGo in the closure).
#
# Why this matters:
#   The codeweaver binary's "pure static binary" property depends on CGo being
#   completely absent. One CGo dep anywhere in the transitive graph breaks:
#     - Cross-compilation (each CGo target needs its own C toolchain)
#     - macOS static linking (Apple's linker always pulls in libSystem with CGo)
#     - Race detector coverage (go test -race is blind across the C boundary)
#     - The "no native deps" distribution property
#
# Run manually: ./scripts/fitness-no-cgo.sh
# Run from Makefile: make fitness-no-cgo
#
# Exit 0: satisfied — zero CGo in the transitive closure.
# Exit 1: violated — CGo found; list of offending packages printed.

set -euo pipefail

cd "$(dirname "$0")/.."

# Ensure 'go' is resolvable. When invoked from make, $(GO) is used by the Makefile;
# when invoked directly, the shell's PATH must include the Go toolchain.
# Common locations: /usr/local/go/bin, ~/sdk/go*/bin, ~/go/bin.
if ! command -v go >/dev/null 2>&1; then
    for candidate in "$HOME/sdk/go1.26.2/bin" "$HOME/go/bin" \
                     "/usr/local/go/bin" "/opt/homebrew/bin"; do
        if [ -x "$candidate/go" ]; then
            export PATH="$candidate:$PATH"
            break
        fi
    done
fi
if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: 'go' not found in PATH. Add the Go toolchain to PATH and retry."
    exit 1
fi

echo "no-cgo: building with CGO_ENABLED=0..."
if ! CGO_ENABLED=0 go build ./...; then
    echo "FAIL — CGO_ENABLED=0 build failed."
    exit 1
fi
echo "no-cgo: build OK."

echo "no-cgo: scanning transitive deps for CGo..."

# go list -deps -json emits one JSON object per package in the transitive closure.
# jq 'select(.CgoFiles or .CXXFiles)' filters to packages that use CGo.
# The 'or' means: has at least one C source file OR at least one C++ source file.
# Think of this like checking every brick in a building for asbestos — you have
# to check the whole wall, not just the visible surface.
cgo_deps=$(go list -deps -json ./... 2>/dev/null \
    | jq -r 'select((.CgoFiles | length) > 0 or (.CXXFiles | length) > 0) | .ImportPath' \
    | sort -u \
    || true)

if [ -n "$cgo_deps" ]; then
    echo "FAIL — CGo-enabled packages found in the transitive closure:"
    echo "$cgo_deps"
    echo ""
    echo "Each of these packages will break cross-compilation and static linking."
    echo "Resolve by replacing the dependency with a pure-Go alternative."
    exit 1
fi

echo "no-cgo: clean — zero CGo packages in the transitive closure."
echo "        CGO_ENABLED=0 build will succeed on all five cross-compile targets."
exit 0
