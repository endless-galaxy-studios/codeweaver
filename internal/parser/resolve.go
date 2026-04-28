// Package parser provides the language dispatch layer and per-language parse pipelines.
//
// This file implements Pass 3: cross-file import map construction and
// call-edge resolution. It is called after all files in a workspace have
// been parsed (Passes 1 and 2 run per-file in the language-specific packages).
//
// # How cross-file resolution works
//
// Cross-file resolution uses the import map built in Pass 2 to resolve calls like:
//
//	# main.py
//	from utils import helper
//	def process(): helper(x)
//
// After Pass 1+2, main.py has:
//   - localNameIndex: {"process": "main.py::process", "MODULE": "main.py::MODULE"}
//   - importMap: {"helper": "utils::helper"}
//   - rawCalls: [{source: "main.py::process", callee: "helper"}]
//
// Pass 3 resolves "helper" via importMap -> "utils::helper", then checks
// whether "utils::helper" is in the global symbol set. If utils.py was also
// parsed, the edge is emitted: main.py::process -> utils.py::helper.
//
// Python import maps use dotted module names ("utils") not file paths ("utils.py").
// Cross-file resolution only succeeds when the dotted module name matches what
// parseImportMap() in the python package produces. In our fixture corpus, this is
// intentionally the case for utils.py/main.py in the cross_file_calls/ fixture.
//
// # Language-agnostic design
//
// ResolveMultiFile accepts []*FileResult where FileResult is the shared
// producer/consumer contract defined in types.go. This file has no imports
// from any language-specific package (python, typescript, etc.). Adding a new
// language requires only wiring the new parser into cli/parse.go; this file
// is unchanged.
package parser

import (
	"context"
	"path/filepath"
	"strings"

	"codeweaver/internal/schema"
)

// ResolveMultiFile runs Pass 3 on a set of per-file parse results.
//
// ctx is a context.Context that may carry a deadline or cancellation signal.
// By Go convention, context.Context is always the first argument. If ctx is
// cancelled or its deadline expires between files, ResolveMultiFile returns
// whatever edges have been resolved so far — files not yet processed get no
// cross-file edges, but their symbols are still included. Think of it like a
// postal sorting centre: if the shift ends mid-way, packages already sorted
// get delivered; unsorted packages stay in the pile (no edges, but visible).
//
// It builds a global name index from all symbols across all files, then calls
// ResolveCallsForFile for each file using the global index for cross-file
// resolution.
//
// The opts parameter controls cross-file resolution behavior. When
// opts.ModuleNameMap is non-nil and non-empty, cross-file edges are resolved
// using the map to translate import targets (e.g., "utils" or "cross_file/utils")
// to file paths (e.g., "utils.py" or "cross_file/utils.ts"). When opts is the
// zero value (ResolveOptions{}), behavior is identical to the prior implementation:
// same-file-only edges are produced (backward-compatible default).
//
// The returned symbols and edges are sorted for deterministic output (required
// by the output schema v1).
//
// This function is language-agnostic: it accepts []*FileResult which is the
// shared type defined in types.go and implemented by all language parsers.
func ResolveMultiFile(ctx context.Context, results []*FileResult, opts ResolveOptions) (allSymbols []schema.Symbol, allEdges []schema.Edge, parseErrors []schema.ParseError) {
	// Build global symbol set, global name index, and per-file symbol index.
	//
	// allSymbolQNames: set of all qualified names across all files.
	//   Used for the "both endpoints must be known" guard before emitting an edge.
	//
	// globalNameIndex: bare name → qualified name (last-write-wins heuristic).
	//   Used as a fallback when no import map or local index resolves a call.
	//
	// fileSymbolIndex: rel_path → { bare_name → qualified_name }.
	//   Used by cross-file resolution: once the import map tells us "utils",
	//   and ModuleNameMap tells us "utils" → "utils.py", we look up the callee
	//   in fileSymbolIndex["utils.py"] to find the full qualified name.
	allSymbolQNames := map[string]bool{}
	globalNameIndex := map[string]string{}
	fileSymbolIndex := map[string]map[string]string{} // relPath → { name → qualifiedName }

	for _, r := range results {
		if _, ok := fileSymbolIndex[r.RelPath]; !ok {
			fileSymbolIndex[r.RelPath] = make(map[string]string, len(r.Symbols))
		}
		for _, s := range r.Symbols {
			allSymbolQNames[s.QualifiedName] = true
			// Global name index: bare name -> qualified name.
			// Last write wins (acceptable heuristic — matches Python behavior).
			globalNameIndex[s.Name] = s.QualifiedName
			// Per-file symbol index: maps bare name to qualified name within this file.
			fileSymbolIndex[r.RelPath][s.Name] = s.QualifiedName
		}
		allSymbols = append(allSymbols, r.Symbols...)
	}

	// Resolve calls for each file.
	// Check ctx between files: if the deadline expired, return partial edges
	// (already-resolved files keep their edges; remaining files get none).
	for _, r := range results {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Deadline or cancellation during cross-file resolution.
			// Files not yet processed get no edges but their symbols remain in allSymbols.
			break
		}

		edges := resolveCallsForFileWithOpts(r, globalNameIndex, allSymbolQNames, fileSymbolIndex, opts)
		allEdges = append(allEdges, edges...)

		if r.HasError {
			pe := schema.ParseError{
				FilePath:   r.RelPath,
				ErrorCode:  schema.ErrorCodeParseIncomplete,
				Message:    "tree-sitter produced ERROR or MISSING nodes",
				ByteOffset: r.ErrorByteOffset,
			}
			parseErrors = append(parseErrors, pe)
		}
	}

	// Sort for deterministic output.
	SortSymbols(allSymbols)
	SortEdges(allEdges)

	return allSymbols, allEdges, parseErrors
}

// resolveCallsForFileWithOpts resolves raw calls in a FileResult, using both the
// standard resolution path (ResolveCallsForFile) for same-file calls and an
// extended cross-file path when opts.ModuleNameMap is populated.
//
// Cross-file resolution works as follows:
//  1. resolveRawCall returns the import-map target, e.g. "utils::helper"
//     or "./utils::helper".
//  2. The import target is split into (moduleRef, symbolName) at "::".
//  3. moduleRef is normalized (TypeScript: strip leading "./" etc.) and looked
//     up in opts.ModuleNameMap to find the target file path (e.g. "utils.py").
//  4. The target qualified name is looked up in fileSymbolIndex[targetFilePath].
//  5. If the qualified name is in allSymbolQNames, the edge is emitted.
//
// When opts.ModuleNameMap is nil or empty, this function falls back to the
// existing ResolveCallsForFile behavior (same-file-only edges).
func resolveCallsForFileWithOpts(
	result *FileResult,
	globalNameIndex map[string]string,
	allSymbolQNames map[string]bool,
	fileSymbolIndex map[string]map[string]string,
	opts ResolveOptions,
) []schema.Edge {
	edges := make([]schema.Edge, 0, len(result.RawCalls))

	for _, rc := range result.RawCalls {
		target := resolveRawCall(rc, result.LocalNameIndex, result.BareMethodIndex, result.ImportMap, globalNameIndex)
		if target == "" {
			continue
		}

		// Check if the resolved target is a known qualified name as-is (same-file or
		// global-index path). If it is, emit the edge directly.
		if allSymbolQNames[target] {
			if rc.SourceQualifiedName != target {
				edges = append(edges, schema.Edge{
					SourceQualifiedName: rc.SourceQualifiedName,
					TargetQualifiedName: target,
					EdgeType:            "calls",
				})
			}
			continue
		}

		// The target is not a known qualified name — it may be a cross-file reference
		// in the form "moduleRef::symbolName" (e.g. "utils::helper" or "./utils::helper").
		// Attempt cross-file resolution only when opts.ModuleNameMap is populated.
		if len(opts.ModuleNameMap) == 0 {
			// No module map — cannot resolve cross-file reference. Drop edge.
			continue
		}

		// Split "moduleRef::symbolName".
		sepIdx := strings.Index(target, "::")
		if sepIdx < 0 {
			// No "::" separator — not a cross-file reference shape. Drop edge.
			continue
		}
		moduleRef := target[:sepIdx]
		symbolName := target[sepIdx+2:]

		// Normalize the module reference for lookup.
		// TypeScript: strip leading "./" (e.g., "./utils" → "utils" relative to the
		// importing file's directory, then reconstruct workspace-relative key).
		// Python: moduleRef is already a dotted name (e.g. "utils", "cross_file_calls.utils").
		normalizedRef := normalizeModuleRef(moduleRef, result.RelPath)

		targetFilePath, ok := opts.ModuleNameMap[normalizedRef]
		if !ok {
			// Module not in the map — external dependency or unknown file. Drop.
			continue
		}

		// Look up the symbol in the target file's symbol index.
		fileIdx, ok := fileSymbolIndex[targetFilePath]
		if !ok {
			// File is in the map but was not actually parsed (shouldn't happen; log-worthy). Drop.
			continue
		}

		resolvedTarget, ok := fileIdx[symbolName]
		if !ok {
			// Symbol not found in the target file. Drop.
			continue
		}

		// Both-endpoints check.
		if !allSymbolQNames[rc.SourceQualifiedName] || !allSymbolQNames[resolvedTarget] {
			continue
		}
		// Self-referential guard.
		if rc.SourceQualifiedName == resolvedTarget {
			continue
		}

		edges = append(edges, schema.Edge{
			SourceQualifiedName: rc.SourceQualifiedName,
			TargetQualifiedName: resolvedTarget,
			EdgeType:            "calls",
		})
	}

	return edges
}

// normalizeModuleRef normalizes an import module reference for lookup in ModuleNameMap.
//
// Python imports use dotted names directly: "utils", "pkg.sub.mod",
// "cross_file_calls.utils". These are used as-is.
//
// TypeScript import specifiers use relative paths: "./utils", "../shared/types".
// Normalization resolves them against the importing file's directory to produce
// a workspace-relative path (without leading "./" or extension).
//
// For example:
//
//	importingFile = "cross_file/main.ts"
//	moduleRef     = "./utils"
//	→ dir("cross_file/main.ts") = "cross_file"
//	→ join("cross_file", "utils") = "cross_file/utils"
//
// If the moduleRef does not start with "./" or "../", it is returned as-is
// (handles both Python dotted names and TypeScript bare-specifier imports).
func normalizeModuleRef(moduleRef, importingFileRelPath string) string {
	if !strings.HasPrefix(moduleRef, "./") && !strings.HasPrefix(moduleRef, "../") {
		// Python dotted name or TypeScript bare specifier — use as-is.
		return moduleRef
	}

	// TypeScript relative path: resolve against the importing file's directory.
	importingDir := filepath.Dir(importingFileRelPath)
	// filepath.Join handles ".." traversal correctly.
	joined := filepath.Join(importingDir, moduleRef)
	// Normalize to forward slashes for cross-platform consistency.
	return filepath.ToSlash(joined)
}

// ResolveSkipDirs returns the set of directory names that should be excluded
// from file discovery.
//
// This list is the canonical union of skip-dirs used by all supported language
// parsers (Python and TypeScript). Future grammars may extend this list.
func ResolveSkipDirs() map[string]bool {
	return map[string]bool{
		"node_modules":  true,
		".next":         true,
		".nuxt":         true,
		"dist":          true,
		"build":         true,
		"out":           true,
		".turbo":        true,
		".cache":        true,
		"__pycache__":   true,
		".git":          true,
		"coverage":      true,
		".nyc_output":   true,
		".venv":         true,
		"venv":          true,
		"env":           true,
		".env":          true,
		".mypy_cache":   true,
		".ruff_cache":   true,
		".pytest_cache": true,
	}
}

// NormalizeRelPath converts an absolute file path to a workspace-relative path
// with forward-slash separators. If the path cannot be made relative to the
// workspace root, it is returned as-is with forward slashes.
//
// This implements the path normalization rule documented in the CLI spec:
//   - Workspace-relative paths
//   - Forward slashes on all platforms
//   - No leading "./"
func NormalizeRelPath(absPath, workspaceRoot string) string {
	rel, err := filepath.Rel(workspaceRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}
