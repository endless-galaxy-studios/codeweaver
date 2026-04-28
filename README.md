# codeweaver

Lightweight code intelligence for AI tooling. Pure-Go static binary. Tree-sitter-based. JSON output schema. No runtime dependencies.

## What it does

`codeweaver parse` analyzes source code files and emits a cross-file symbol + call-edge graph as a JSON document on stdout. Any process that can spawn a subprocess and read stdout can consume the output — no runtime coupling, no dependencies to install.

## Why codeweaver?

The code intelligence space has excellent tools. After surveying 19 open-source projects, we found four distinct categories:

- **Persistent MCP daemons** — codebase-memory-mcp, CodeGraphContext, code-graph-mcp, and others. Powerful, well-built, and designed to run alongside your agent. They maintain state between calls. If your integration model is "spawn a process, read stdout, move on," they don't fit.
- **Embedded tools** — Aider's repomap pioneered tree-sitter for AI context with a PageRank-weighted graph. Excellent engineering, but inseparable from the host product. You can't extract just the parser.
- **Search and lint tools** — ast-grep (13.7k stars) is mature tree-sitter tooling for structural search. It answers "where does this pattern appear?" not "give me the whole-repo symbol graph."
- **Infrastructure protocols** — SCIP and Kythe are enterprise-grade, multi-language, and battle-tested. They also require compiler-grade per-language indexers and emit Protobuf. Not something you call from a shell script.

The gap those categories leave: **a tool that parses files, emits a structured graph to stdout, and exits.** Every project made an integration choice that coupled the parsing layer to a specific consumption model. codeweaver chose not to — it's infrastructure that any integration can consume.

## Design philosophy

- **One-shot subprocess.** Parse files, emit JSON, exit. No daemon. No state. No process to manage. Anything that can spawn a subprocess gets the same output.
- **Zero runtime dependencies.** A single static binary. No Python, no Node, no JVM. Download, chmod, run. Works on a fresh CI runner with nothing installed.
- **Versioned JSON schema.** The output schema is a documented, versioned API surface — not an implementation detail. Consumers can branch on `schema_version` the same way they branch on an HTTP API version.
- **Protocol-agnostic.** Not coupled to MCP, not coupled to any agent framework. An MCP server can call it. A GitHub Action can call it. A shell script can call it. The caller's integration model is the caller's choice.

## Quick start

```bash
# Parse specific files
codeweaver parse src/main.py src/utils.ts | jq '.'
```

`jq` is optional — it is not a codeweaver dependency. Pipe to it for readable output; without it, codeweaver emits compact JSON directly to stdout.

**Sample output (truncated):**

```json
{
  "schema_version": "1.0",
  "parser_version": "0.1.0",
  "parsed_at": "2026-04-28T10:00:00Z",
  "file_count": 2,
  "symbols": [
    {
      "qualified_name": "src/main.py::main",
      "name": "main",
      "file_path": "src/main.py",
      "symbol_type": "function",
      "language": "python",
      "line_start": 24,
      "line_end": 27
    },
    ...
  ],
  "edges": [
    {
      "source_qualified_name": "src/main.py::main",
      "target_qualified_name": "src/utils.py::helper",
      "edge_type": "calls"
    },
    ...
  ],
  "deleted_files": []
}
```

You can also pipe a file list from `codeweaver discover`:

```bash
cd ./my-project && codeweaver discover . | codeweaver parse --files-from -
```

## How people use it

### AI agent plugin

A plugin that needs cross-file call-graph context spawns codeweaver as a subprocess, reads the JSON, and feeds symbols and edges into the agent's context window. No language server required, no persistent process to manage.

```python
import subprocess, json

result = subprocess.run(
    ["codeweaver", "parse", "src/main.py", "src/utils.ts"],
    capture_output=True, text=True, check=True,
)
graph = json.loads(result.stdout)

# Check schema version before processing
major = int(graph.get("schema_version", "0.0").split(".")[0])
if major != 1:
    raise RuntimeError(f"Unsupported codeweaver schema version")

# Feed symbols and edges into your agent's context window
symbols = graph["symbols"]
edges = graph["edges"]
```

### CI pipeline — dead code detection

Find symbols that nothing calls. Parse the whole repo, then use `jq` to compute the set of symbol names that never appear as a call target.

```bash
codeweaver discover . | codeweaver parse --files-from - | \
  jq '[.symbols[].qualified_name] as $all |
      [.edges[] | .target_qualified_name] as $called |
      $all - $called'
```

### MCP server built on top

An MCP server that needs code structure doesn't have to embed a parser — it can shell out to codeweaver and serve the result over MCP protocol. The server handles the protocol; codeweaver handles the parsing. Each does one thing.

```python
# Inside an MCP tool handler
result = subprocess.run(
    ["codeweaver", "parse", "--files-from", "-"],
    input="\n".join(workspace_files),
    capture_output=True, text=True,
)
graph = json.loads(result.stdout)
# Return graph data as MCP tool response
```

### Code review bot

A GitHub Actions bot runs codeweaver on a PR's changed files, builds a diff-aware call graph, and surfaces which functions are affected by the change.

```bash
# Get changed files from git, parse them, extract affected symbols
git diff --name-only HEAD~1 | grep -E '\.(py|ts)$' | \
  codeweaver parse --files-from - | \
  jq '.edges[] | select(.edge_type == "calls") | .source_qualified_name' | \
  sort -u
```

### Shell scripting

Pipe `discover` into `parse` for whole-repo analysis in one line.

```bash
# All function symbols in the repo, sorted
codeweaver discover . | codeweaver parse --files-from - | \
  jq -r '.symbols[] | select(.symbol_type == "function") | .qualified_name' | sort
```

## Install

Download a prebuilt binary from [GitHub Releases](https://github.com/endless-galaxy-studios/codeweaver/releases) for your OS and architecture.

`go install` is not supported. The `go.mod` module path is a bare local name (`module codeweaver`), not a fully-qualified VCS path, so Go module resolution cannot fetch it. Binary download is the only supported install path.

## Supported languages

| Extension | Language |
|-----------|----------|
| `.py` | Python |
| `.ts` | TypeScript |
| `.tsx` | TypeScript (TSX/JSX variant) |
| `.mts` | TypeScript (ES module variant) |

More languages are in progress. See the roadmap below.

## Output schema

`codeweaver parse` always emits a JSON document with a `schema_version` field set to `"1.0"`. Consumers can read that field at parse time to gate compatibility before processing `symbols[]` and `edges[]`.

The output schema — what constitutes a breaking vs. additive change, version negotiation patterns, and code examples in Python and Go — is documented in [`docs/output-schema.md`](docs/output-schema.md). The machine-readable JSON Schema is at `schema/codeweaver-v1.json`.

## Roadmap

The current output schema (v1.0) covers symbols and call edges. Planned additive extensions include:

- **Inheritance edges** — `class Foo(Bar)` and `class Foo extends Bar` as typed `inherits` edges
- **Decorator extraction** — `@router.get`, `@pytest.fixture`, and other decorator names stored in `metadata_`
- **Docstrings and JSDoc** — first-line docstring text as `metadata_.docstring` for semantic context
- **Type annotations** — return types and parameter types as `metadata_` fields
- **Go grammar** — Go as a first supported language beyond Python and TypeScript
- **Additional languages** — Rust, Java, and C# are planned; community grammar contributions are welcome

All planned extensions are additive (minor version bumps to `schema_version`). Breaking changes require a major version bump and will be called out explicitly. Track progress and file issues on [GitHub](https://github.com/endless-galaxy-studios/codeweaver/issues).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, the grammar-addition guide, DCO sign-off requirements, and the snapshot-test discipline.

---

Codeweaver is developed and maintained by [Neuroloom](https://neuroloom.com). Apache 2.0 licensed.
