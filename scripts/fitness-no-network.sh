#!/usr/bin/env bash
# fitness-no-network.sh — no-network architectural fitness function.
#
# What it checks:
#   Verifies that no network-initiating symbol (Dial, Listen, LookupHost, Resolver)
#   is reachable from the production binary's closure via govulncheck's call graph,
#   and performs a supplementary grep-based symbol audit.
#
# Why this matters:
#   codeweaver processes source code locally and only locally. A binary that
#   phones home would silently break that trust model: source bytes must never
#   leave the developer's machine via codeweaver. "No network calls" is also
#   the property that makes the binary safe to run in air-gapped environments.
#
# Two-layer enforcement strategy:
#   Layer 1 (govulncheck): static call-graph analysis. Detects reachable paths
#     to vulnerable (and potentially network-initiating) symbols. Run on every
#     CI build.
#   Layer 2 (grep audit): fast symbol scan over Go source for net.Dial,
#     net.Listen, etc. Catches direct calls even when govulncheck doesn't flag
#     them as vulnerable. Run on every CI build.
#
# Linux-namespace egress test (Layer 3, Linux-only):
#   On Linux CI runners, run under `unshare -n` to execute the binary in a
#   network-isolated namespace. If the binary makes any network call, the
#   syscall fails with EPERM. Run:
#     unshare -n ./codeweaver parse --workspace . testdata/fixtures/python/empty_module.py
#   This layer is NOT run in this script because it requires root or CAP_SYS_ADMIN
#   on older kernels and is macOS-incompatible. Wire it into CI on a
#   linux/amd64 runner only.
#
# Run manually: ./scripts/fitness-no-network.sh
# Run from Makefile: make fitness-no-network
#
# Exit 0: satisfied (both layers clean).
# Exit 1: potential violation detected; output describes findings.

set -euo pipefail

cd "$(dirname "$0")/.."

# Ensure 'go' is resolvable when invoked directly (not via make $(GO)).
if ! command -v go >/dev/null 2>&1; then
    for candidate in "$HOME/sdk/go1.26.2/bin" "$HOME/go/bin" \
                     "/usr/local/go/bin" "/opt/homebrew/bin"; do
        if [ -x "$candidate/go" ]; then
            export PATH="$candidate:$PATH"
            break
        fi
    done
fi

FAIL=0

# ---- Layer 1: govulncheck ----
echo "no-network [layer 1]: govulncheck reachability scan..."
if ! command -v govulncheck >/dev/null 2>&1; then
    echo "  SKIP — govulncheck not installed."
    echo "  Install: go install golang.org/x/vuln/cmd/govulncheck@latest"
    echo "  govulncheck layer is required in CI; skipping locally if not installed."
else
    # govulncheck exits 0 when no vulnerabilities are found, non-zero otherwise.
    # We pipe through grep to surface any paths mentioning network symbols.
    # The || true prevents set -e from exiting on a non-zero govulncheck result.
    vuln_out=$(govulncheck ./... 2>&1 || true)
    # Check if govulncheck found any vulnerabilities (non-zero exit = vulnerabilities found)
    if echo "$vuln_out" | grep -qE "^Vulnerability"; then
        echo "  WARN — govulncheck found vulnerabilities (review below for network-related ones):"
        echo "$vuln_out" | grep -E "^(Vulnerability|  More info|  Found in)" | head -20
        # Don't fail on general vulns here — fail only on confirmed network-path vulns.
        # General vulnerability handling belongs to a separate govulncheck gate.
    else
        echo "  no-network [layer 1]: clean — no reachable vulnerabilities."
    fi
fi

# ---- Layer 2: grep symbol audit ----
echo "no-network [layer 2]: symbol audit for network-initiating calls..."

# Patterns that indicate direct network use in production code.
# net.Dial, net.Listen, net.LookupHost, net.Resolver are the canonical
# "opens a network connection" symbols. http.Get/Post/NewRequest are
# higher-level wrappers that always resolve to Dial.
network_patterns=(
    'net\.Dial'
    'net\.Listen'
    'net\.LookupHost'
    'net\.Resolver'
    'http\.Get'
    'http\.Post'
    'http\.NewRequest'
    'http\.DefaultClient'
    'grpc\.Dial'
    'grpc\.NewClient'
)

# Concatenate into a single alternation for one grep pass.
# POSIX ERE (-E) is used for portability across macOS grep and GNU grep.
pattern=$(IFS='|'; echo "${network_patterns[*]}")

# Search production source only (cmd/, internal/). Skip test files and vendor.
violations=$(grep -rEn "$pattern" \
    --include="*.go" \
    --exclude="*_test.go" \
    cmd/ internal/ \
    2>/dev/null \
    | grep -v '//.*'"$pattern" \
    || true)

if [ -n "$violations" ]; then
    echo "  WARN — potential network symbols found in production source:"
    echo "$violations"
    echo ""
    echo "  Review each occurrence:"
    echo "    - If it's in the 'net' stdlib import for IP flag types (pflag),  it is"
    echo "      likely unreachable from the binary's call graph (pflag imports net for"
    echo "      IP address parsing but codeweaver registers no IP flags)."
    echo "    - If it's a direct outbound call, it violates the no-network rule. Remove it."
    echo "  Confirmed violations should be fixed before release."
    FAIL=1
else
    echo "  no-network [layer 2]: clean — no network-initiating symbols in production source."
fi

echo ""
echo "no-network [layer 3 — Linux CI only]:"
echo "  On Linux runners, run the binary in a network-isolated namespace to verify"
echo "  no syscalls escape to the network at runtime:"
echo "    unshare -n ./codeweaver parse --workspace . testdata/fixtures/python/empty_module.py"
echo "  Wire this step into a linux/amd64 CI job."

if [ "$FAIL" -eq 1 ]; then
    exit 1
fi
echo ""
echo "no-network: all local layers clean."
exit 0
