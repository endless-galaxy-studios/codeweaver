# codeweaver/scripts

Utility scripts for local development and CI. All scripts require `bash` and run from the module root (`codeweaver-go/`).

## check-stdout-invariant.sh

**What:** Enforces the narrowed stdout invariant — no `fmt.Print*` calls in production source outside the explicitly exempt files.

**Rule:** stdout is the JSON output schema. `fmt.Print*` and `os.Stdout` references outside the allowed packages are a latent correctness bug (they corrupt consumer output silently).

**Exempt files:**
- `internal/cli/version.go` — human-readable `codeweaver version` output
- `internal/cli/root.go` — shell completion scripts
- `cmd/codeweaver/main.go` — panic recovery stderr

**Run:** `./scripts/check-stdout-invariant.sh` or `make lint`

**CI:** Runs as part of `make lint` on every PR.

---

## fitness-no-cgo.sh

**What:** Verifies that `CGO_ENABLED=0 go build ./...` succeeds AND that zero packages in the transitive dependency closure have C or C++ source files.

**Rule:** The binary must be purely static Go with no CGo anywhere in the transitive graph. CGo breaks cross-compilation, macOS static linking, race detector coverage, and the "no native deps" distribution property.

**Run:** `./scripts/fitness-no-cgo.sh` or `make fitness-no-cgo`

**CI:** Runs on every PR and every release build (all five GOOS/GOARCH targets).

**Requires:** `jq` on PATH.

---

## fitness-no-network.sh

**What:** Two-layer audit for reachable network calls in the production binary.

- **Layer 1** (`govulncheck`): static call-graph analysis for reachable vulnerabilities, including network-path symbols.
- **Layer 2** (grep): fast source scan for `net.Dial`, `net.Listen`, `net.LookupHost`, `net.Resolver`, `http.Get/Post/NewRequest`.

**Rule:** The binary makes zero outbound network calls at runtime. Source bytes must never leave the developer's machine via codeweaver. A binary that phones home silently breaks the no-network trust boundary that consumers rely on.

**Layer 3 (Linux CI only, not in this script):** Run the binary under `unshare -n` to execute in a network-isolated namespace. Wire this into a `linux/amd64` CI runner:

```bash
unshare -n ./codeweaver parse --workspace . testdata/fixtures/python/empty_module.py
```

**Run:** `./scripts/fitness-no-network.sh` or `make fitness-no-network`

**CI:** Runs on every PR. govulncheck also runs independently in the CI pipeline on dependency bumps.

**Requires:** `govulncheck` on PATH (optional; warns if absent); `jq` not required.

---

## fitness-stdout-json-only.sh

**What:** Integration-level check that each subcommand emits the correct type of output to stdout.

| Subcommand | Expected stdout |
|------------|----------------|
| `parse <file>` | Valid JSON (output schema v1) |
| `version --json` | Valid JSON (version schema) |
| `version` | Human-readable text (exempt) |
| `discover .` | Newline-delimited paths (exempt) |
| `completion bash` | Shell script (exempt) |

**Rule:** Consumers (`subprocess.run`, Bun `$`) parse stdout directly. A non-JSON byte on stdout from `parse` or `version --json` is a hard consumer breakage.

**Run:** `./scripts/fitness-stdout-json-only.sh [binary-path]` or `make stdout-json-only`

Binary defaults to `./codeweaver`. Build first: `make build`

**CI:** Runs after each build step in the release pipeline.

**Requires:** `jq` on PATH; the binary at `./codeweaver` (or the path passed as `$1`).

---

## fitness-layering-invariant.sh

**What:** Enforces two layering rules across the entire `internal/` package tree.

**Rule 1 — sibling isolation:** Only `internal/cli/*` may import language-specific packages (`internal/parser/python` or `internal/parser/typescript`). No other internal package (`contract`, `discover`, `log`, `limits`, `crashlog`, etc.) may import a language package directly.

**Rule 2 — cross-language isolation:** `internal/parser/python` and `internal/parser/typescript` must not import each other. Both are leaf nodes. Shared behaviour belongs in the language-agnostic layer (`internal/parser/resolve.go`, `types.go`, `dispatch.go`).

**Why the broader scope (vs. only checking the three layering-layer files):** As `internal/discover/`, `internal/schema/`, and future packages evolve, any of them could accidentally import a language package. The original script only caught violations in three specific files; this version catches them anywhere in `internal/`.

**Run:** `./scripts/fitness-layering-invariant.sh` or `make layering-invariant`

**CI:** Runs on every PR as part of `make lint`.

**Requires:** `grep -E` (POSIX-compatible; works on macOS BSD grep and GNU grep).
