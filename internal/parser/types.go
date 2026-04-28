// Package parser provides the language dispatch layer and per-language parse
// pipelines.
//
// This file defines the shared producer/consumer contract between language-
// specific parse packages (e.g., internal/parser/python) and the cross-file
// resolution layer (resolve.go).
//
// # Why this contract exists
//
// Think of a factory assembly line:
//
//   - Worker stations (python, typescript packages) are the *producers* —
//     each reads a source file and stamps out a FileResult for that file.
//   - The shipping department (resolve.go) is the *consumer* — it takes all
//     the per-file results, stitches cross-file references together, and
//     emits the final sorted JSON document.
//
// Without a shared type, the shipping department would have to import every
// worker-station package directly, creating a dependency loop that grows with
// each new language. By defining FileResult and RawCall here, each language
// package depends on the shared contract, not on each other or on resolve.go.
//
// Dependency graph after this refactor:
//
//	internal/parser/python  →  internal/parser  (imports FileResult/RawCall)
//	internal/parser/typescript  →  internal/parser  (same)
//	internal/parser/resolve.go  →  (no language-specific imports)
//
// Adding a new language requires:
//  1. Create internal/parser/<language>/parser.go
//  2. Implement ParseFile(source []byte, relPath string) (*parser.FileResult, error)
//  3. Wire into cli/parse.go dispatch switch — no changes to resolve.go
package parser

import (
	"errors"
	"fmt"
	"sort"

	"codeweaver/internal/schema"
)

// GrammarLoadError is a sentinel error wrapping a grammar initialization failure.
// When a language grammar fails to load (returns nil or panics during sync.Once init),
// language parsers return a GrammarLoadError so cli/parse.go can emit
// schema.ErrorCodeGrammarLoadFailed instead of the generic E_PARSE_INCOMPLETE.
//
// This distinction matters for consumers (the plugin, the API): E_GRAMMAR_LOAD_FAILED
// signals an environment-level failure (corrupt binary, missing shared library, OOM)
// that affects ALL files for this language, not just one parse problem in one file.
// A consumer that receives E_GRAMMAR_LOAD_FAILED can escalate to an alert rather
// than silently accumulating per-file parse errors.
//
// Real-life analogy: if a translator fails to show up to a conference (grammar load
// failure), every session in that language is affected — it's different from one
// attendee being too tired to listen to a talk (per-file parse error).
type GrammarLoadError struct {
	Language string // "python", "typescript", "tsx"
	Cause    error
}

// Error implements the error interface.
func (e *GrammarLoadError) Error() string {
	return fmt.Sprintf("grammar load failed for language %q: %v", e.Language, e.Cause)
}

// Unwrap allows errors.Is and errors.As to inspect the wrapped cause.
func (e *GrammarLoadError) Unwrap() error { return e.Cause }

// IsGrammarLoadError returns true if err wraps a GrammarLoadError.
// Use this in cli/parse.go to route grammar failures to E_GRAMMAR_LOAD_FAILED.
func IsGrammarLoadError(err error) bool {
	var gle *GrammarLoadError
	return errors.As(err, &gle)
}

// ---------------------------------------------------------------------------
// Shared producer/consumer types
// ---------------------------------------------------------------------------

// FileResult holds the raw outputs of parsing a single source file before
// cross-file resolution is applied.
//
// This type is produced by language-specific parsers (e.g., python.ParseFile)
// and consumed by ResolveMultiFile in resolve.go. It carries only primitive
// types and already-shared contract types so that new language parsers can
// implement the same shape without importing any language-specific package.
//
// Language-specific parsers return *FileResult; ResolveMultiFile accepts
// []*FileResult and is language-agnostic.
type FileResult struct {
	// RelPath is the workspace-relative path with forward slashes.
	// Used in qualified names and JSON output.
	RelPath string

	// Symbols is the full list of extracted symbols for this file.
	// Already sorted by QualifiedName when produced by ParseFile.
	// Format: MODULE + functions + classes + methods.
	Symbols []schema.Symbol

	// RawCalls is the list of unresolved call edges discovered in this file.
	// Produced by Pass 2 (call extraction); consumed by Pass 3 (resolution).
	RawCalls []RawCall

	// ImportMap maps locally-visible names to their source qualified names.
	// Built during Pass 2; consumed by ResolveMultiFile in Pass 3.
	// Example: {"helper": "utils::helper"} for "from utils import helper".
	ImportMap map[string]string

	// LocalNameIndex maps local symbol names to their qualified names within
	// this file. Built during Pass 1; used to resolve same-file calls in Pass 3.
	// Example: {"greet": "main.py::greet"}.
	LocalNameIndex map[string]string

	// BareMethodIndex maps class_name -> {method_name -> qualified_name}.
	// Used for intra-class self/cls call resolution in Pass 3.
	// Example: {"MyClass": {"run": "main.py::MyClass.run"}}.
	BareMethodIndex map[string]map[string]string

	// HasError is true when tree-sitter found ERROR or MISSING nodes in the
	// source file. A partial result is still emitted; the error is surfaced
	// in the document's parse_errors array.
	HasError bool

	// ErrorByteOffset is the byte offset of the first error node in the source,
	// when HasError is true. Nil otherwise.
	ErrorByteOffset *int

	// GrammarName is the name of the grammar used to parse this file.
	// Always populated by language parsers in schema v1: "python", "typescript", or "tsx".
	// Internal-only (no JSON tag) — never serialized to the output schema.
	// Used by tests to assert the correct grammar variant was selected
	// (e.g., "tsx" for .tsx files, "typescript" for .ts/.mts files).
	GrammarName string

	// ModuleName is the importable module name for this file within the workspace.
	//
	// Python: derived by stripping the ".py" extension and replacing "/" with "." —
	// so "utils.py" → "utils", "pkg/sub/mod.py" → "pkg.sub.mod".
	// This matches the dotted name used in "from pkg.sub.mod import symbol" statements.
	//
	// TypeScript: derived by stripping the extension and keeping the relative path —
	// so "cross_file/utils.ts" → "cross_file/utils". Import specifiers like "./utils"
	// from a file in the same directory ("cross_file/main.ts") are resolved against
	// the importing file's directory to produce a workspace-relative path, then
	// matched against this field.
	//
	// Internal-only (no JSON tag) — never serialized to the output schema.
	// Used by ResolveMultiFile when ResolveOptions.ModuleNameMap is populated.
	ModuleName string
}

// ---------------------------------------------------------------------------
// ResolveOptions — cross-file resolution configuration
// ---------------------------------------------------------------------------

// ResolveOptions configures cross-file call resolution behavior.
//
// Optional fields default to the existing same-file-only behavior, preserving
// backward compatibility for callers that pass a zero-value ResolveOptions{}.
//
// Real-life analogy: think of a directory assistance call center. Without a
// phone book (ModuleNameMap is nil), the operator can only connect calls within
// the same building (same-file). With a phone book, the operator can route calls
// across buildings (cross-file) by looking up "utils" → "utils.py".
type ResolveOptions struct {
	// ModuleNameMap maps a dotted module name (the importable name a Python
	// "from X import Y" or a TypeScript "from './X'" would refer to) to the
	// relative file path within the workspace. When non-nil, ResolveMultiFile
	// uses it to resolve cross-file call edges.
	//
	// Example (Python): {"utils": "utils.py", "pkg.sub.mod": "pkg/sub/mod.py"}
	// Example (TypeScript): {"cross_file/utils": "cross_file/utils.ts"}
	//
	// Key: the module name as it would appear in an import (after normalization).
	// Value: the workspace-relative file path (forward slashes).
	//
	// The map is built by cli/parse.go from FileResult.ModuleName fields:
	//   for each result: ModuleNameMap[result.ModuleName] = result.RelPath
	//
	// Last-write-wins for collisions (matches the global name index heuristic).
	ModuleNameMap map[string]string
}

// RawCall is an unresolved call edge discovered during Pass 2 (call extraction).
//
// "Raw" means the callee name has not yet been qualified to a full
// source_qualified_name -> target_qualified_name edge. Resolution happens in
// Pass 3 (ResolveMultiFile / ResolveCallsForFile).
//
// Think of it like a citation in an academic paper that only has the author's
// last name — it becomes a full bibliographic reference (with journal, volume,
// page) only once you look it up in the works-cited list (the import map and
// global name index).
type RawCall struct {
	// SourceQualifiedName is the enclosing function/method qualified name.
	// This is the "from" endpoint of the edge once resolved.
	SourceQualifiedName string

	// CalleeName is the bare callee name (not yet qualified).
	// Example: "helper" for a call like "helper(x)".
	CalleeName string

	// ReceiverText is the receiver text for attribute calls (e.g., "self", "cls",
	// or another variable name). Empty for plain function calls.
	// Example: "self" for "self.run()"; "obj" for "obj.method()".
	ReceiverText string

	// EnclosingClassName is set for calls inside a class body.
	// Used to resolve self/cls calls to the correct class's method index.
	EnclosingClassName string
}

// ---------------------------------------------------------------------------
// Sorting utilities (shared across all language parsers)
// ---------------------------------------------------------------------------

// SortSymbols sorts a symbol slice by qualified_name (ascending).
//
// This is the canonical sort order for schema v1 output. Sorting is a
// contract invariant: consumers (plugins, snapshot fixtures, downstream APIs)
// rely on deterministic ordering for diff stability and deduplication.
// Asserted by property tests in internal/schema/properties_test.go.
func SortSymbols(symbols []schema.Symbol) {
	sort.Slice(symbols, func(i, j int) bool {
		return symbols[i].QualifiedName < symbols[j].QualifiedName
	})
}

// SortEdges sorts an edge slice by (source_qualified_name, target_qualified_name,
// edge_type) — a three-key composite sort for full determinism.
//
// Same contract guarantee as SortSymbols: deterministic ordering across all runs
// on the same input is required by consumers for stable diffs.
func SortEdges(edges []schema.Edge) {
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.SourceQualifiedName != b.SourceQualifiedName {
			return a.SourceQualifiedName < b.SourceQualifiedName
		}
		if a.TargetQualifiedName != b.TargetQualifiedName {
			return a.TargetQualifiedName < b.TargetQualifiedName
		}
		return a.EdgeType < b.EdgeType
	})
}

// ---------------------------------------------------------------------------
// ResolveCallsForFile — resolve calls for a single file result
// ---------------------------------------------------------------------------

// ResolveCallsForFile resolves raw calls in a FileResult using the provided
// global name index (for cross-file resolution) and the file's own import map
// and local name index.
//
// Resolution priority (mirrors Python's resolve_call()):
//  1. Intra-class self/cls call → bare method index lookup for the enclosing class.
//  2. Import-scoped match → import map lookup.
//  3. Same-file match → local name index lookup.
//  4. Global index fallback → global name index.
//
// Only edges where both endpoints are present in allSymbolQNames are emitted.
// Unresolvable calls and edges to unknown symbols are dropped silently — this
// prevents phantom edges to nonexistent targets and keeps the output strictly consistent
// with the symbols present in the payload.
//
// The returned edges are NOT sorted — ResolveMultiFile sorts them after collecting
// edges from all files.
func ResolveCallsForFile(result *FileResult, globalNameIndex map[string]string, allSymbolQNames map[string]bool) []schema.Edge {
	edges := make([]schema.Edge, 0, len(result.RawCalls))

	for _, rc := range result.RawCalls {
		target := resolveRawCall(rc, result.LocalNameIndex, result.BareMethodIndex, result.ImportMap, globalNameIndex)
		if target == "" {
			continue
		}
		// Both endpoints must be known symbols.
		if !allSymbolQNames[rc.SourceQualifiedName] || !allSymbolQNames[target] {
			continue
		}
		// Drop self-referential edges (source == target) — a call within
		// the same symbol does not produce a meaningful graph edge.
		if rc.SourceQualifiedName == target {
			continue
		}
		edges = append(edges, schema.Edge{
			SourceQualifiedName: rc.SourceQualifiedName,
			TargetQualifiedName: target,
			EdgeType:            "calls",
		})
	}

	return edges
}

// resolveRawCall resolves a single RawCall to a target qualified name.
// Returns "" if the call cannot be resolved.
//
// This implements the four-priority resolution strategy shared by all language
// parsers. It is package-private because consumers call ResolveCallsForFile.
func resolveRawCall(
	rc RawCall,
	localNameIndex map[string]string,
	bareMethodIndex map[string]map[string]string,
	importMap map[string]string,
	globalNameIndex map[string]string,
) string {
	// 1. Intra-class self/cls/this calls.
	//
	// Python uses "self" or "cls" as the receiver for intra-class method calls.
	// TypeScript uses "this". Both patterns use the same bare-method index lookup.
	// Real-life analogy: regardless of language, "I'm calling a method on myself"
	// is expressed as self.run() (Python) or this.run() (TypeScript) — both mean
	// the same thing and resolve the same way via the enclosing class context.
	if (rc.ReceiverText == "self" || rc.ReceiverText == "cls" || rc.ReceiverText == "this") && rc.EnclosingClassName != "" {
		if methods, ok := bareMethodIndex[rc.EnclosingClassName]; ok {
			if target, ok := methods[rc.CalleeName]; ok {
				return target
			}
		}
		// Could not resolve self/cls/this call — fall through to other strategies.
	}

	// 2. Import map (for cross-file calls via imported names).
	if target, ok := importMap[rc.CalleeName]; ok {
		return target
	}

	// 3. Local name index (same-file).
	if target, ok := localNameIndex[rc.CalleeName]; ok {
		return target
	}

	// 4. Global name index fallback.
	if globalNameIndex != nil {
		if target, ok := globalNameIndex[rc.CalleeName]; ok {
			return target
		}
	}

	return ""
}
