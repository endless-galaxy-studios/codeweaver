#!/usr/bin/env python3
"""JSON normalization script for codeweaver parity tests.

Reads a codeweaver output schema v1 JSON document from stdin and writes a normalized,
deterministic form to stdout. The normalized form is used for byte-identical
comparison against committed *.expected.json snapshot fixtures.

Normalization rules:
  1. Sort all JSON object keys (deterministic key ordering).
  2. Remove run-specific envelope fields: parsed_at, parser_version.
     These differ between runs and should not affect fixture comparison.
  3. Sort symbols array by qualified_name.
  4. Sort edges array by (source_qualified_name, target_qualified_name, edge_type).
  5. Sort parse_errors array by file_path, then error_code.
  6. Remove the "metadata" key from any symbol or edge object (schema v1 omits it).
  7. Remove null-valued line_start / line_end from MODULE symbols
     (schema v1 omits these fields; normalization makes both absent).
  8. Compact JSON output (no extra whitespace), LF-terminated.

Usage:
  python3 testdata/normalize.py < raw_output.json > normalized.json
  codeweaver parse --workspace=... file.py | python3 testdata/normalize.py
"""

from __future__ import annotations

import json
import sys


def normalize_symbol(sym: dict) -> dict:
    """Normalize a single symbol object."""
    # Remove metadata key (schema v1 omits it).
    sym.pop("metadata", None)
    # Remove null line_start/line_end — module symbols have null; go omits them.
    if sym.get("line_start") is None:
        sym.pop("line_start", None)
    if sym.get("line_end") is None:
        sym.pop("line_end", None)
    return sym


def normalize_edge(edge: dict) -> dict:
    """Normalize a single edge object."""
    # Remove metadata key (schema v1 omits it).
    edge.pop("metadata", None)
    return edge


def normalize(doc: dict) -> dict:
    """Normalize a codeweaver output schema v1 document for deterministic comparison."""
    # Strip run-specific envelope fields.
    doc.pop("parsed_at", None)
    doc.pop("parser_version", None)

    # Normalize symbols.
    symbols = doc.get("symbols", [])
    symbols = [normalize_symbol(s) for s in symbols]
    symbols.sort(key=lambda s: s.get("qualified_name", ""))
    doc["symbols"] = symbols

    # Normalize edges.
    edges = doc.get("edges", [])
    edges = [normalize_edge(e) for e in edges]
    edges.sort(key=lambda e: (
        e.get("source_qualified_name", ""),
        e.get("target_qualified_name", ""),
        e.get("edge_type", ""),
    ))
    doc["edges"] = edges

    # Normalize parse_errors if present.
    if "parse_errors" in doc and doc["parse_errors"] is not None:
        errs = doc["parse_errors"]
        errs.sort(key=lambda e: (
            e.get("file_path", ""),
            e.get("error_code", ""),
        ))
        doc["parse_errors"] = errs

    # deleted_files is always [] in binary output; normalize to [].
    doc.setdefault("deleted_files", [])

    return doc


def sort_keys_recursive(obj):
    """Recursively sort all dict keys for deterministic JSON key ordering."""
    if isinstance(obj, dict):
        return {k: sort_keys_recursive(v) for k, v in sorted(obj.items())}
    if isinstance(obj, list):
        return [sort_keys_recursive(item) for item in obj]
    return obj


def main() -> None:
    try:
        raw = sys.stdin.read()
        doc = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"normalize.py: invalid JSON on stdin: {e}", file=sys.stderr)
        sys.exit(1)

    normalized = normalize(doc)
    # Sort all keys for deterministic ordering across language runtimes.
    normalized = sort_keys_recursive(normalized)
    # Compact, no extra whitespace, LF-terminated.
    print(json.dumps(normalized, separators=(",", ":")), end="\n")


if __name__ == "__main__":
    main()
