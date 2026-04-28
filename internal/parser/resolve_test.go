// Package parser_test contains unit tests for cross-file call-edge resolution.
//
// These tests exercise ResolveMultiFile and ResolveCallsForFile by constructing
// FileResult structs directly — no parser invocation required. That keeps each
// test millisecond-fast and deterministic independent of tree-sitter grammar
// updates.
//
// # What is cross-file resolution?
//
// Think of multiple Python files like chapters in a book. After reading each
// chapter in isolation (Pass 1 + Pass 2, per-file), Pass 3 (ResolveMultiFile)
// stitches the cross-chapter references together: "when chapter A mentions a
// character introduced in chapter B, create a link between them."
//
// The global name index is the book's index — a flat mapping of character name
// to the chapter where they're first introduced. Last-write-wins when two
// chapters define the same character name (a known heuristic).
//
// # ResolveOptions and ModuleNameMap
//
// The cross-file resolution gap was: the import map uses dotted module names
// ("utils") but the symbol index uses file paths ("utils.py"). With the
// ResolveOptions.ModuleNameMap wired in (added when cross-file resolution shipped), the resolver can
// translate "utils" → "utils.py" and find the correct symbol.
//
// Backward compatibility: callers passing ResolveOptions{} (zero value) get the
// existing same-file-only behavior — no panic, no cross-file edges.
//
// # Language-agnostic tests
//
// These tests construct parser.FileResult and parser.RawCall values directly
// (from the shared types in internal/parser/types.go), not python.FileResult.
// This keeps the test layer language-agnostic: the same test suite exercises
// the resolve logic for any language producer, not just Python.
package parser_test

import (
	"context"
	"sort"
	"testing"

	"codeweaver/internal/schema"
	"codeweaver/internal/parser"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeSymbol creates a schema.Symbol with mandatory fields only.
// Tests that care about line numbers override via direct struct literal.
func makeSymbol(qualifiedName, name, filePath, symbolType string) schema.Symbol {
	return schema.Symbol{
		QualifiedName: qualifiedName,
		Name:          name,
		FilePath:      filePath,
		SymbolType:    symbolType,
		Language:      "python",
	}
}

// edgeTuple produces a sortable string for an Edge, used in order assertions.
func edgeTupleStr(e schema.Edge) string {
	return e.SourceQualifiedName + "|" + e.TargetQualifiedName + "|" + e.EdgeType
}

// ---------------------------------------------------------------------------
// Same-file call resolution
// ---------------------------------------------------------------------------

// TestResolveMultiFile_SameFileCall verifies that a call from function A to
// function B within the same file produces exactly one edge: X::A → X::B.
//
// This is the simplest resolution path: the local name index is sufficient —
// no import map or global index needed.
func TestResolveMultiFile_SameFileCall(t *testing.T) {
	const relPath = "utils.py"

	// Symbol: module, funcA, funcB — all in the same file.
	symbolMODULE := makeSymbol(relPath+"::MODULE", "MODULE", relPath, "module")
	symbolA := makeSymbol(relPath+"::funcA", "funcA", relPath, "function")
	symbolB := makeSymbol(relPath+"::funcB", "funcB", relPath, "function")

	result := &parser.FileResult{
		RelPath:   relPath,
		Symbols:   []schema.Symbol{symbolMODULE, symbolA, symbolB},
		RawCalls:  []parser.RawCall{{SourceQualifiedName: relPath + "::funcA", CalleeName: "funcB"}},
		ImportMap: map[string]string{},
		LocalNameIndex: map[string]string{
			"MODULE": relPath + "::MODULE",
			"funcA":  relPath + "::funcA",
			"funcB":  relPath + "::funcB",
		},
		BareMethodIndex: map[string]map[string]string{},
	}

	symbols, edges, parseErrors := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{result}, parser.ResolveOptions{})

	if len(parseErrors) != 0 {
		t.Fatalf("expected no parse errors, got %d: %v", len(parseErrors), parseErrors)
	}
	if len(symbols) != 3 {
		t.Errorf("expected 3 symbols, got %d", len(symbols))
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %v", len(edges), edges)
	}
	e := edges[0]
	if e.SourceQualifiedName != relPath+"::funcA" {
		t.Errorf("edge source: got %q, want %q", e.SourceQualifiedName, relPath+"::funcA")
	}
	if e.TargetQualifiedName != relPath+"::funcB" {
		t.Errorf("edge target: got %q, want %q", e.TargetQualifiedName, relPath+"::funcB")
	}
	if e.EdgeType != "calls" {
		t.Errorf("edge type: got %q, want %q", e.EdgeType, "calls")
	}
}

// ---------------------------------------------------------------------------
// Backward-compat: zero-value ResolveOptions drops cross-file edges
// ---------------------------------------------------------------------------

// TestResolveMultiFile_ZeroValueOpts_SameFileOnly asserts that passing a zero-value
// ResolveOptions{} (no ModuleNameMap) produces same-file edges only, with no panic.
// This is the backward-compatibility guarantee: existing callers that pass
// ResolveOptions{} see identical behavior to the pre-Phase-4 implementation.
func TestResolveMultiFile_ZeroValueOpts_SameFileOnly(t *testing.T) {
	const mainFile = "main.py"
	const utilsFile = "utils.py"

	// utils.py: defines funcB.
	utilsResult := &parser.FileResult{
		RelPath: utilsFile,
		Symbols: []schema.Symbol{
			makeSymbol(utilsFile+"::MODULE", "MODULE", utilsFile, "module"),
			makeSymbol(utilsFile+"::funcB", "funcB", utilsFile, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": utilsFile + "::MODULE", "funcB": utilsFile + "::funcB"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "utils",
	}

	// main.py: imports funcB from utils (dotted module name "utils"), calls it.
	// Import map uses dotted name "utils" — without ModuleNameMap, this cannot resolve.
	mainResult := &parser.FileResult{
		RelPath: mainFile,
		Symbols: []schema.Symbol{
			makeSymbol(mainFile+"::MODULE", "MODULE", mainFile, "module"),
			makeSymbol(mainFile+"::funcA", "funcA", mainFile, "function"),
		},
		RawCalls:        []parser.RawCall{{SourceQualifiedName: mainFile + "::funcA", CalleeName: "funcB"}},
		ImportMap:       map[string]string{"funcB": "utils::funcB"},
		LocalNameIndex:  map[string]string{"MODULE": mainFile + "::MODULE", "funcA": mainFile + "::funcA"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "main",
	}

	// Zero-value ResolveOptions: no ModuleNameMap — cross-file edges must be dropped.
	_, edges, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{utilsResult, mainResult}, parser.ResolveOptions{})

	for _, e := range edges {
		if e.SourceQualifiedName == mainFile+"::funcA" && e.TargetQualifiedName == utilsFile+"::funcB" {
			t.Errorf("cross-file edge emitted with zero-value ResolveOptions{}: %q → %q; "+
				"backward-compat guarantee requires same-file-only edges when ModuleNameMap is nil",
				e.SourceQualifiedName, e.TargetQualifiedName)
		}
	}
	// Reaching here confirms backward-compat: no cross-file edge produced without map.
}

// ---------------------------------------------------------------------------
// Cross-file call resolution with ModuleNameMap wired
// ---------------------------------------------------------------------------

// TestResolveMultiFile_CrossFileCall_WithModuleMap asserts that cross-file call
// edges are emitted when ResolveOptions.ModuleNameMap is populated.
//
// Cross-file resolution is wired via ResolveOptions.ModuleNameMap.
//
// Scenario (Python-style): main.py imports funcB from utils.py via
// "from utils import funcB". The import map stores "funcB" → "utils::funcB"
// (dotted module name). ModuleNameMap["utils"] = "utils.py" bridges the gap.
func TestResolveMultiFile_CrossFileCall_WithModuleMap(t *testing.T) {
	const mainFile = "main.py"
	const utilsFile = "utils.py"

	// utils.py: defines funcB.
	utilsResult := &parser.FileResult{
		RelPath: utilsFile,
		Symbols: []schema.Symbol{
			makeSymbol(utilsFile+"::MODULE", "MODULE", utilsFile, "module"),
			makeSymbol(utilsFile+"::funcB", "funcB", utilsFile, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": utilsFile + "::MODULE", "funcB": utilsFile + "::funcB"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "utils",
	}

	// main.py: imports funcB from utils, calls it.
	mainResult := &parser.FileResult{
		RelPath: mainFile,
		Symbols: []schema.Symbol{
			makeSymbol(mainFile+"::MODULE", "MODULE", mainFile, "module"),
			makeSymbol(mainFile+"::funcA", "funcA", mainFile, "function"),
		},
		RawCalls:        []parser.RawCall{{SourceQualifiedName: mainFile + "::funcA", CalleeName: "funcB"}},
		ImportMap:       map[string]string{"funcB": "utils::funcB"},
		LocalNameIndex:  map[string]string{"MODULE": mainFile + "::MODULE", "funcA": mainFile + "::funcA"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "main",
	}

	// Wire the module name map: "utils" (dotted module name) → "utils.py" (file path).
	opts := parser.ResolveOptions{
		ModuleNameMap: map[string]string{
			"utils": utilsFile,
			"main":  mainFile,
		},
	}

	_, edges, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{utilsResult, mainResult}, opts)

	// Expect exactly one cross-file edge: main.py::funcA → utils.py::funcB.
	var found bool
	for _, e := range edges {
		if e.SourceQualifiedName == mainFile+"::funcA" && e.TargetQualifiedName == utilsFile+"::funcB" {
			found = true
			if e.EdgeType != "calls" {
				t.Errorf("cross-file edge type: got %q, want %q", e.EdgeType, "calls")
			}
		}
	}
	if !found {
		t.Errorf("expected cross-file edge %q → %q; got edges: %v",
			mainFile+"::funcA", utilsFile+"::funcB", edges)
	}
}

// ---------------------------------------------------------------------------
// Cross-file resolution: TypeScript relative-path specifiers
// ---------------------------------------------------------------------------

// TestResolveMultiFile_CrossFileCall_TypeScriptRelativePath asserts that
// TypeScript-style relative import specifiers ("./utils") are resolved correctly
// when ModuleNameMap is populated.
//
// Scenario: cross_file/main.ts imports helper from "./utils". The import map
// stores "helper" → "./utils::helper". ModuleNameMap["cross_file/utils"] = "cross_file/utils.ts"
// (computed by normalizeModuleRef from the importing file's directory "cross_file").
//
// Cross-file resolution is wired via ResolveOptions.ModuleNameMap.
func TestResolveMultiFile_CrossFileCall_TypeScriptRelativePath(t *testing.T) {
	const mainFile = "cross_file/main.ts"
	const utilsFile = "cross_file/utils.ts"

	// utils.ts: defines helper.
	utilsResult := &parser.FileResult{
		RelPath: utilsFile,
		Symbols: []schema.Symbol{
			makeSymbol(utilsFile+"::MODULE", "MODULE", utilsFile, "module"),
			makeSymbol(utilsFile+"::helper", "helper", utilsFile, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": utilsFile + "::MODULE", "helper": utilsFile + "::helper"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "cross_file/utils",
	}

	// main.ts: imports helper from "./utils", calls it.
	// The import map stores the raw specifier prefix: "./utils::helper".
	mainResult := &parser.FileResult{
		RelPath: mainFile,
		Symbols: []schema.Symbol{
			makeSymbol(mainFile+"::MODULE", "MODULE", mainFile, "module"),
			makeSymbol(mainFile+"::process", "process", mainFile, "function"),
		},
		RawCalls:        []parser.RawCall{{SourceQualifiedName: mainFile + "::process", CalleeName: "helper"}},
		ImportMap:       map[string]string{"helper": "./utils::helper"},
		LocalNameIndex:  map[string]string{"MODULE": mainFile + "::MODULE", "process": mainFile + "::process"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "cross_file/main",
	}

	// Wire the module name map using workspace-relative paths (extension-stripped).
	// normalizeModuleRef("./utils", "cross_file/main.ts") → "cross_file/utils"
	opts := parser.ResolveOptions{
		ModuleNameMap: map[string]string{
			"cross_file/utils": utilsFile,
			"cross_file/main":  mainFile,
		},
	}

	_, edges, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{utilsResult, mainResult}, opts)

	var found bool
	for _, e := range edges {
		if e.SourceQualifiedName == mainFile+"::process" && e.TargetQualifiedName == utilsFile+"::helper" {
			found = true
			if e.EdgeType != "calls" {
				t.Errorf("cross-file edge type: got %q, want %q", e.EdgeType, "calls")
			}
		}
	}
	if !found {
		t.Errorf("expected cross-file edge %q → %q; got edges: %v",
			mainFile+"::process", utilsFile+"::helper", edges)
	}
}

// ---------------------------------------------------------------------------
// ModuleNameMap collision: last-write-wins
// ---------------------------------------------------------------------------

// TestResolveMultiFile_ModuleMapCollision_LastWriteWins asserts the behavior
// when two files map to the same module name. The last entry written to
// ModuleNameMap wins (matches the global name index heuristic).
//
// In practice this shouldn't happen in single-language workspaces, but in
// mixed-language workspaces (Python + TypeScript) it could theoretically occur.
// The behavior is documented and asserted here so that any future change is deliberate.
func TestResolveMultiFile_ModuleMapCollision_LastWriteWins(t *testing.T) {
	const fileA = "utils.py"
	const fileB = "utils.ts"
	const mainFile = "main.py"

	// Both utils.py and utils.ts are assigned module name "utils".
	// fileB is written last into ModuleNameMap → fileB wins.
	resultA := &parser.FileResult{
		RelPath: fileA,
		Symbols: []schema.Symbol{
			makeSymbol(fileA+"::MODULE", "MODULE", fileA, "module"),
			makeSymbol(fileA+"::funcX", "funcX", fileA, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": fileA + "::MODULE", "funcX": fileA + "::funcX"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "utils",
	}
	resultB := &parser.FileResult{
		RelPath: fileB,
		Symbols: []schema.Symbol{
			makeSymbol(fileB+"::MODULE", "MODULE", fileB, "module"),
			makeSymbol(fileB+"::funcX", "funcX", fileB, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": fileB + "::MODULE", "funcX": fileB + "::funcX"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "utils",
	}
	mainResult := &parser.FileResult{
		RelPath: mainFile,
		Symbols: []schema.Symbol{
			makeSymbol(mainFile+"::MODULE", "MODULE", mainFile, "module"),
			makeSymbol(mainFile+"::caller", "caller", mainFile, "function"),
		},
		RawCalls:        []parser.RawCall{{SourceQualifiedName: mainFile + "::caller", CalleeName: "funcX"}},
		ImportMap:       map[string]string{"funcX": "utils::funcX"},
		LocalNameIndex:  map[string]string{"MODULE": mainFile + "::MODULE", "caller": mainFile + "::caller"},
		BareMethodIndex: map[string]map[string]string{},
		ModuleName:      "main",
	}

	// Build the map in order: resultA first, then resultB — resultB writes last.
	opts := parser.ResolveOptions{ModuleNameMap: make(map[string]string)}
	for _, r := range []*parser.FileResult{resultA, resultB, mainResult} {
		if r.ModuleName != "" {
			opts.ModuleNameMap[r.ModuleName] = r.RelPath
		}
	}
	// opts.ModuleNameMap["utils"] should now be fileB (last write wins).
	if opts.ModuleNameMap["utils"] != fileB {
		t.Fatalf("precondition: ModuleNameMap[\"utils\"] should be %q (last write), got %q",
			fileB, opts.ModuleNameMap["utils"])
	}

	_, edges, _ := parser.ResolveMultiFile(
		context.Background(), []*parser.FileResult{resultA, resultB, mainResult}, opts,
	)

	// The edge should resolve to fileB::funcX (the last-write winner).
	var found bool
	for _, e := range edges {
		if e.SourceQualifiedName == mainFile+"::caller" && e.TargetQualifiedName == fileB+"::funcX" {
			found = true
		}
		// Should NOT see fileA::funcX as the target.
		if e.TargetQualifiedName == fileA+"::funcX" {
			t.Errorf("collision last-write-wins failed: got edge to %q (fileA), expected %q (fileB)",
				fileA+"::funcX", fileB+"::funcX")
		}
	}
	if !found {
		t.Errorf("expected edge main.py::caller → utils.ts::funcX (last-write-wins), got: %v", edges)
	}
}

// ---------------------------------------------------------------------------
// Unresolved import is silently dropped
// ---------------------------------------------------------------------------

// TestResolveMultiFile_UnresolvedCallDropped verifies that a call to a name that
// does not appear in any FileResult's import map, local name index, or global
// index produces NO edge — no phantom edges to nonexistent symbols.
//
// Unresolvable calls are silently dropped. The alternative (emitting an edge to a
// nonexistent target) would violate the contract invariant that every edge endpoint
// is a known symbol in the payload.
func TestResolveMultiFile_UnresolvedCallDropped(t *testing.T) {
	const relPath = "app.py"

	result := &parser.FileResult{
		RelPath: relPath,
		Symbols: []schema.Symbol{
			makeSymbol(relPath+"::MODULE", "MODULE", relPath, "module"),
			makeSymbol(relPath+"::funcA", "funcA", relPath, "function"),
		},
		// funcA calls "nonexistent_func" which is not defined anywhere in the payload.
		RawCalls: []parser.RawCall{{
			SourceQualifiedName: relPath + "::funcA",
			CalleeName:          "nonexistent_func",
		}},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": relPath + "::MODULE", "funcA": relPath + "::funcA"},
		BareMethodIndex: map[string]map[string]string{},
	}

	_, edges, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{result}, parser.ResolveOptions{})

	if len(edges) != 0 {
		t.Errorf("expected 0 edges (unresolved call must be dropped), got %d: %v", len(edges), edges)
	}
}

// ---------------------------------------------------------------------------
// Namespace collision: last-write-wins heuristic
// ---------------------------------------------------------------------------

// TestResolveMultiFile_NamespaceCollision_LastWriteWins documents and asserts the
// last-write-wins behavior of the global name index when two files define a symbol
// with the same bare name.
//
// This mirrors resolve.go: "Last write wins (acceptable heuristic — matches
// Python behavior)." The test asserts the ACTUAL current heuristic — not the
// desired one — so that any future change to the heuristic causes a deliberate
// test update.
//
// The order of FileResult slices passed to ResolveMultiFile determines which
// file's symbol "wins" for the global name index. The last FileResult processed
// overwrites earlier entries for the same bare name.
func TestResolveMultiFile_NamespaceCollision_LastWriteWins(t *testing.T) {
	const fileA = "mod_a/foo.py"
	const fileB = "mod_b/foo.py"
	const caller = "caller.py"

	// Both fileA and fileB define a function named "bar".
	resultA := &parser.FileResult{
		RelPath: fileA,
		Symbols: []schema.Symbol{
			makeSymbol(fileA+"::MODULE", "MODULE", fileA, "module"),
			makeSymbol(fileA+"::bar", "bar", fileA, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": fileA + "::MODULE", "bar": fileA + "::bar"},
		BareMethodIndex: map[string]map[string]string{},
	}
	resultB := &parser.FileResult{
		RelPath: fileB,
		Symbols: []schema.Symbol{
			makeSymbol(fileB+"::MODULE", "MODULE", fileB, "module"),
			makeSymbol(fileB+"::bar", "bar", fileB, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": fileB + "::MODULE", "bar": fileB + "::bar"},
		BareMethodIndex: map[string]map[string]string{},
	}
	// caller.py calls bare name "bar" — no import map, no local definition of "bar".
	// Resolution falls through to the global name index where last-write-wins.
	resultCaller := &parser.FileResult{
		RelPath: caller,
		Symbols: []schema.Symbol{
			makeSymbol(caller+"::MODULE", "MODULE", caller, "module"),
			makeSymbol(caller+"::main", "main", caller, "function"),
		},
		RawCalls: []parser.RawCall{{
			SourceQualifiedName: caller + "::main",
			CalleeName:          "bar",
		}},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{"MODULE": caller + "::MODULE", "main": caller + "::main"},
		BareMethodIndex: map[string]map[string]string{},
	}

	// Pass resultA first, then resultB — resultB writes last, so "bar" in the
	// global index will point to fileB::bar.
	_, edges, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{resultA, resultB, resultCaller}, parser.ResolveOptions{})

	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (last-write-wins resolves to fileB::bar), got %d: %v", len(edges), edges)
	}
	// The last-indexed "bar" is from fileB (it was processed after fileA).
	wantTarget := fileB + "::bar"
	if edges[0].TargetQualifiedName != wantTarget {
		t.Errorf("last-write-wins: edge target got %q, want %q (fileB processed last)",
			edges[0].TargetQualifiedName, wantTarget)
	}
}

// ---------------------------------------------------------------------------
// Output is sorted by (source_qualified_name, target_qualified_name, edge_type)
// ---------------------------------------------------------------------------

// TestResolveMultiFile_OutputSorted_Edges asserts that edges returned by
// ResolveMultiFile are sorted by (source_qualified_name, target_qualified_name,
// edge_type). This is the AC from the plan (line 352).
//
// Sorting is a contract invariant: downstream consumers rely on deterministic
// ordering for diffing and deduplication.
func TestResolveMultiFile_OutputSorted_Edges(t *testing.T) {
	const relPath = "multi.py"

	// Four functions: funcA calls funcD, funcB, funcC — unsorted insertion order.
	// After ResolveMultiFile the edges must be sorted by source, then target.
	symbols := []schema.Symbol{
		makeSymbol(relPath+"::MODULE", "MODULE", relPath, "module"),
		makeSymbol(relPath+"::funcA", "funcA", relPath, "function"),
		makeSymbol(relPath+"::funcB", "funcB", relPath, "function"),
		makeSymbol(relPath+"::funcC", "funcC", relPath, "function"),
		makeSymbol(relPath+"::funcD", "funcD", relPath, "function"),
	}
	localIdx := map[string]string{
		"MODULE": relPath + "::MODULE",
		"funcA":  relPath + "::funcA",
		"funcB":  relPath + "::funcB",
		"funcC":  relPath + "::funcC",
		"funcD":  relPath + "::funcD",
	}
	// Deliberately insert raw calls in unsorted order.
	rawCalls := []parser.RawCall{
		{SourceQualifiedName: relPath + "::funcA", CalleeName: "funcD"},
		{SourceQualifiedName: relPath + "::funcA", CalleeName: "funcB"},
		{SourceQualifiedName: relPath + "::funcA", CalleeName: "funcC"},
	}

	result := &parser.FileResult{
		RelPath:         relPath,
		Symbols:         symbols,
		RawCalls:        rawCalls,
		ImportMap:       map[string]string{},
		LocalNameIndex:  localIdx,
		BareMethodIndex: map[string]map[string]string{},
	}

	_, edges, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{result}, parser.ResolveOptions{})

	if len(edges) != 3 {
		t.Fatalf("expected 3 edges, got %d: %v", len(edges), edges)
	}

	// Verify sorted order.
	for i := 1; i < len(edges); i++ {
		prev := edgeTupleStr(edges[i-1])
		curr := edgeTupleStr(edges[i])
		if prev > curr {
			t.Errorf("edges not sorted at index %d: %q > %q", i, prev, curr)
		}
	}
}

// TestResolveMultiFile_OutputSorted_Symbols asserts that symbols returned by
// ResolveMultiFile are sorted by qualified_name. AC plan line 352.
func TestResolveMultiFile_OutputSorted_Symbols(t *testing.T) {
	const fileX = "x.py"
	const fileY = "y.py"

	resultX := &parser.FileResult{
		RelPath: fileX,
		Symbols: []schema.Symbol{
			makeSymbol(fileX+"::MODULE", "MODULE", fileX, "module"),
			makeSymbol(fileX+"::zebra", "zebra", fileX, "function"),
			makeSymbol(fileX+"::apple", "apple", fileX, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{},
		BareMethodIndex: map[string]map[string]string{},
	}
	resultY := &parser.FileResult{
		RelPath: fileY,
		Symbols: []schema.Symbol{
			makeSymbol(fileY+"::MODULE", "MODULE", fileY, "module"),
			makeSymbol(fileY+"::mango", "mango", fileY, "function"),
		},
		RawCalls:        []parser.RawCall{},
		ImportMap:       map[string]string{},
		LocalNameIndex:  map[string]string{},
		BareMethodIndex: map[string]map[string]string{},
	}

	symbols, _, _ := parser.ResolveMultiFile(context.Background(), []*parser.FileResult{resultX, resultY}, parser.ResolveOptions{})

	// Verify sorted order.
	sorted := make([]schema.Symbol, len(symbols))
	copy(sorted, symbols)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].QualifiedName < sorted[j].QualifiedName
	})

	for i, s := range symbols {
		if s.QualifiedName != sorted[i].QualifiedName {
			t.Errorf("symbol[%d] not sorted: got %q, expected sorted %q", i, s.QualifiedName, sorted[i].QualifiedName)
		}
	}
}
