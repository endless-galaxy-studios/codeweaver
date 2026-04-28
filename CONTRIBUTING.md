# Contributing to codeweaver

Thank you for your interest in contributing. This document covers sign-off requirements, development setup, and the key contributor path: adding support for a new language.

---

## Developer Certificate of Origin (DCO)

Every commit to codeweaver must carry a `Signed-off-by:` trailer. By adding that trailer, you certify the following:

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

**What this means in practice:** The DCO is a one-line declaration in every commit message — a signed-off receipt confirming you wrote the code (or have the right to contribute it) and agree it can be distributed under codeweaver's Apache 2.0 license. It's much lower-friction than a CLA (Contributor License Agreement) — no separate form, no approval workflow. Just `git commit -s` adds the trailer automatically.

**Shortcut:**

```
git commit -s -m "your commit message"
```

This produces the trailer automatically:

```
Signed-off-by: Your Name <your@email.com>
```

The DCO check is enforced by a GitHub Actions workflow on every PR. Commits missing the trailer will fail the check.

---

## Development Setup

**Go version:** Go 1.26 with toolchain go1.26.2 (specified in `go.mod`). Standard toolchain distributions may not include Go 1.26 yet — install the correct version from [https://go.dev/dl/](https://go.dev/dl/).

**Common tasks:**

| Command | What it does |
|---------|-------------|
| `make build` | Compile the binary (dev mode, no version injection) |
| `make test` | Run the full test suite (unit + snapshot + schema parity tests) |
| `make fuzz-fast` | Run fuzz corpus targets for 5s each (normal CI gate) |
| `make fuzz` | Run fuzz corpus targets for 30s each (deep run) |
| `make lint` | Run `golangci-lint` plus custom fitness checks (config at `.golangci.yml`) |
| `make schema` | Regenerate `schema/codeweaver-v1.json` via `go generate` and assert idempotency |
| `make snapshot-test` | Run snapshot fixture verification against committed `*.expected.json` files |

See the `Makefile` for the full target list, including `race`, `bench`, `bench-check`, and `release-build`.

**When to run `make schema`:** If you modify any Go types in the output schema (the structs that define what `codeweaver parse` emits), run `make schema` to regenerate `schema/codeweaver-v1.json` and commit the updated schema alongside your type changes. The target regenerates the schema via `go generate` and fails if the resulting file differs from the committed version, ensuring the schema is always in sync with the code.

---

## Independent Semver

codeweaver follows independent semantic versioning. `v1.0.0` of codeweaver is unrelated to any Neuroloom product version.

Version bumps follow the output schema:

- **Major bump** — a breaking change to the JSON output schema (field removed, field renamed, field semantics changed, `schema_version` major incremented).
- **Minor bump** — an additive change to the output schema (new optional field, new `symbols[].kind` value, new edge type) or a new supported language.
- **Patch bump** — a bug fix where the output now matches the existing schema where it previously didn't.

Binary download via GitHub Releases is the only supported install path. External `go install codeweaver@latest` is **not supported** because Go module resolution requires a fully-qualified VCS path (e.g., `github.com/...`), and codeweaver's `go.mod` uses the bare module name `codeweaver`. If `go install` support is needed in the future, the module path would need to change to `github.com/endless-galaxy-studios/codeweaver` — a breaking change to all internal imports.

---

## Adding a New Language (Grammar Addition Guide)

Grammar additions are the primary external contribution type. This guide walks through every step needed to add support for a new language.

**Substrate:** codeweaver uses **hand-walking the AST** — not typed `.scm` query files. The parser traverses the tree-sitter parse tree by calling `gotreesitter.Walk(root, fn)` (depth-first recursive walk) and `node.ChildByFieldName("field_name", lang)` (retrieves a named child field). See [`IMPLEMENTATION_NOTES.md`](./IMPLEMENTATION_NOTES.md) for the full rationale, including why typed queries were not used for codeweaver v1 and what the criteria are for revisiting that decision.

`internal/parser/python/parser.go` is the canonical model for all steps below.

---

### Step 1 — Vendor the grammar

Vendor the `gotreesitter`-compatible grammar for the target language. The existing grammars (`grammars.PythonLanguage()`, `grammars.TypescriptLanguage()`, `grammars.TsxLanguage()`) are provided by `github.com/odvcencio/gotreesitter`. Add the new grammar as a dependency following the same pattern.

Study `internal/parser/python/parser.go` before writing any code — it is the model for every subsequent step.

---

### Step 2 — Implement the parser using hand-walking

Create `internal/parser/<language>/parser.go`.

Implement extraction using:

- `gotreesitter.Walk(root, fn)` for depth-first traversal.
- `node.ChildByFieldName("field_name", lang)` for named field access.

One visitor function per extraction concern (symbol discovery, call collection, import map building). See `internal/parser/python/parser.go` — the package docstring describes the extraction substrate and links to `IMPLEMENTATION_NOTES.md`. The `parseSymbols` and `parseCallsAndImports` functions show the stateful visitor pattern used for Pass 1 and Pass 2 respectively.

**Do not introduce `.scm` query files.** If typed queries are adopted in a future phase, the migration will cover all languages together under a single ADR-level decision. Introducing `.scm` files for a single language creates an inconsistent substrate that the parity gate cannot validate as transparently.

---

### Step 3 — Wire the dispatch

Three touchpoints are required:

1. **`internal/parser/dispatch.go`** — Add a Tier-1 case in `DetectLanguage` mapping the file extension(s) to your new language package.

2. **`internal/cli/parse.go`** — Add a case in the dispatch switch that calls your new parser's `ParseFile` function.

3. **`internal/cli/version.go`** — Add an entry to the `Grammars` map:
   ```go
   grammars["<language>"] = <language>.GrammarVersion()
   ```

All three touchpoints are required. Missing `version.go` means `codeweaver version --json` will not report the new grammar — breaking diagnostic correlation when consumers see output from that language and try to verify which grammar version produced it.

---

### Step 4 — Add snapshot fixtures

Add real source files and their expected JSON output to `testdata/fixtures/<language>/`. These files are committed to git.

The snapshot test gate (`make snapshot-test`) compares actual parser output to the committed `*.expected.json` fixture. A failing snapshot means one of two things:

- **Output changed intentionally** (you added a new extraction concern): update the fixture file to match and commit it.
- **A bug regressed**: the output changed unexpectedly — fix the bug.

Use realistic source files, not toy examples. Fixtures serve as both documentation and a regression net.

---

### Step 5 — Distinguish fixture paths from fuzz corpus paths

These are separate locations with different purposes. Do not conflate them.

| Purpose | Path |
|---------|------|
| Snapshot fixtures | `testdata/fixtures/<language>/` (repo root) |
| Fuzz corpus seeds | `internal/parser/<language>/testdata/fuzz/<FuzzTarget>/` |

For example: Python fuzz corpus seeds live at `internal/parser/python/testdata/fuzz/FuzzParseFilePython/`. The snapshot fixtures for Python live at `testdata/fixtures/python/`. Adding a new language requires entries in both locations — the snapshot fixtures for `make snapshot-test`, and at least one fuzz seed for `make fuzz-fast`.

---

### Step 6 — Write parity tests

Assert that the Go parser output matches the v1 JSON Schema (`schema/codeweaver-v1.json`). This is what `make test` verifies via `internal/schema/properties_test.go`.

If you modified any output schema types to support the new language, run `make schema` to regenerate `schema/codeweaver-v1.json` and commit the updated schema.

---

### Step 7 — Open a PR

The following checks must all pass before review begins:

- DCO check (all commits carry `Signed-off-by:` trailer)
- Snapshot gate (`make snapshot-test`)
- Parity gate (`make test`)
- `golangci-lint` (`make lint`)

**Wire contract schema contributions:** Contributions to `schema/codeweaver-v1.json` fall under the same Apache 2.0 patent grant as grammar contributions — you must have the right to license any patented techniques your schema changes encode.

---

## Code Style

`gofmt` is enforced by CI. `golangci-lint` is enforced by CI (configuration at `.golangci.yml`).

Run `make lint` before pushing to catch issues locally.

---

## Issue Triage SLA

Best-effort. No SLA.

---

## Telemetry

None. The codeweaver binary makes zero outbound network calls at runtime. No telemetry is collected.
