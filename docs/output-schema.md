# codeweaver Output Schema

`codeweaver parse` writes a single JSON document to stdout on every invocation. This document defines the structure of that output, explains the versioning semantics, and shows how consumers should check compatibility before processing the payload.

The machine-readable schema is [`schema/codeweaver-v1.json`](../schema/codeweaver-v1.json). That schema is the authoritative contract. This document is a consumer guide — it explains the *why* alongside the *what*.

---

## The `schema_version` Field

Every parse output carries a `schema_version` field at the top level:

```json
{
  "schema_version": "1.0",
  "files": [...],
  "symbols": [...],
  "edges": [...],
  "parse_errors": [...]
}
```

The value is a `"<major>.<minor>"` string. It tells a consumer two things:

1. **Which major version of the schema this payload conforms to.** A consumer that understands schema v1 can safely ignore fields it does not recognize — but if the major version changes, the consumer must update its parsing logic before processing the payload.

2. **Which minor revision added the fields the consumer cares about.** If a consumer depends on a field added in v1.3, it can reject payloads where the minor version is below 3.

`schema_version` changes only when the schema changes. A bug fix that brings output into conformance with the existing schema bumps the codeweaver binary's patch version but does not change `schema_version`.

---

## Breaking vs Additive Changes

Not all schema changes are equal. codeweaver distinguishes two categories:

### Breaking changes (major version bump)

A breaking change is any modification that causes a consumer written against the current schema to misparse or reject a valid payload:

- **Removing a field.** A consumer that reads `symbols[].symbol_type` breaks if that field disappears.
- **Renaming a field.** Renaming `edges` to `relationships` breaks every consumer that addresses the old key.
- **Changing the semantics of a field.** If `symbols[].line_start` (or `line_end`) changes from 1-indexed to 0-indexed, consumers that display line numbers will be off by one.

When a breaking change ships, `schema_version` major increments (e.g., `"1.4"` → `"2.0"`) and codeweaver's binary version increments to the next major.

### Additive changes (minor version bump)

An additive change extends the schema without breaking existing consumers. Consumers that do not know about the new field ignore it; consumers that want the new field can opt in by checking the minor version:

- New optional field added to an existing object (e.g., `symbols[].deprecated: bool`).
- New valid value for an enum field (e.g., a new `symbols[].symbol_type` string like `"decorator"`).
- New edge type added to the set of valid `edges[].edge_type` values.

When an additive change ships, `schema_version` minor increments (e.g., `"1.2"` → `"1.3"`).

### Bug fixes (patch only)

If the binary was producing output that did not conform to the existing schema — for example, emitting a field with the wrong type, or omitting a required field for certain inputs — fixing that bug does not change `schema_version`. The schema was already correct; the binary is now correct. The codeweaver binary's patch version increments, but consumers do not need to update their version gates.

---

## Version Negotiation Pattern for Consumers

A consumer should check `schema_version` before processing the payload. Check the major version at minimum; check the minor version if you depend on a field added in a specific revision.

### Python

```python
import json
import subprocess

result = subprocess.run(
    ["codeweaver", "parse", "src/main.py"],
    capture_output=True, text=True, check=True,
)
payload = json.loads(result.stdout)
contract = payload.get("schema_version", "0.0")
major = int(contract.split(".", 1)[0])
if major != 1:
    raise RuntimeError(f"Unsupported codeweaver schema version: {contract}")

# safe to consume payload["symbols"], payload["edges"], etc.
```

**Why `"0.0"` as the default?** If `schema_version` is absent — which should not happen with a correctly-built binary but can occur with very early pre-release builds — the default `"0.0"` causes the major-version check to fail loudly rather than silently consuming a payload of unknown shape.

### Go

```go
var payload struct {
    SchemaVersion string `json:"schema_version"`
}
if err := json.Unmarshal(stdout, &payload); err != nil {
    return err
}
if !strings.HasPrefix(payload.SchemaVersion, "1.") {
    return fmt.Errorf("unsupported schema version: %s", payload.SchemaVersion)
}

// safe to unmarshal full payload struct
```

**Why `strings.HasPrefix`?** Checking for the prefix `"1."` accepts any `1.x` minor version — the consumer works with v1.0, v1.1, v1.3, etc. A strict `== "1.0"` check would break every time codeweaver ships an additive improvement, requiring consumer updates for changes that do not affect the consumer's code path.

---

## Authoritative Schema

`schema/codeweaver-v1.json` is the machine-readable JSON Schema for all v1 payloads. It is generated from the Go struct types via `go generate` (run with `make schema`) and checked into the repository. If the schema file and this document ever disagree, the schema file is correct.

Consumers can use the schema to validate payloads programmatically:

```python
import jsonschema, json, pathlib

schema = json.loads(pathlib.Path("schema/codeweaver-v1.json").read_text())
jsonschema.validate(payload, schema)
```

The schema ships alongside the binary in each GitHub Release archive under `schema/codeweaver-v1.json`.
