package typescript_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"codeweaver/internal/parser"
	"codeweaver/internal/parser/typescript"
	"codeweaver/internal/schema"
)

// testdataRoot returns the path to testdata/fixtures/typescript/ relative to the
// module root. Uses runtime.Caller to locate the test file, then walks up.
func testdataRoot(t *testing.T) string {
	t.Helper()
	_, callerFile, _, _ := runtime.Caller(0)
	// callerFile is: .../codeweaver-go/internal/parser/typescript/parser_test.go
	// testdata is at: .../codeweaver-go/testdata/fixtures/typescript/
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(callerFile))))
	return filepath.Join(moduleRoot, "testdata", "fixtures", "typescript")
}

// moduleRoot returns the codeweaver-go module root directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, callerFile, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(callerFile))))
}

// ---------------------------------------------------------------------------
// Unit tests: symbol extraction
// ---------------------------------------------------------------------------

func TestParseFile_EmptyInput(t *testing.T) {
	result, err := typescript.ParseFile(context.Background(), []byte(""), "empty.ts")
	if err != nil {
		t.Fatalf("ParseFile empty: %v", err)
	}
	// Must have exactly one symbol: MODULE.
	if len(result.Symbols) != 1 {
		t.Errorf("expected 1 symbol (MODULE), got %d: %v", len(result.Symbols), result.Symbols)
	}
	if len(result.Symbols) > 0 && result.Symbols[0].SymbolType != "module" {
		t.Errorf("first symbol must be module, got %q", result.Symbols[0].SymbolType)
	}
	if len(result.RawCalls) != 0 {
		t.Errorf("expected 0 raw calls, got %d", len(result.RawCalls))
	}
}

func TestParseFile_SimpleModule_Symbols(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "simple_module.ts"))
	if err != nil {
		t.Fatalf("read simple_module.ts: %v", err)
	}

	result, err := typescript.ParseFile(context.Background(), source, "simple_module.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Expected symbols: MODULE, greet, shout, main
	wantNames := []string{"MODULE", "greet", "shout", "main"}
	gotNames := make([]string, 0, len(result.Symbols))
	for _, s := range result.Symbols {
		gotNames = append(gotNames, s.Name)
	}
	sort.Strings(gotNames)
	sort.Strings(wantNames)

	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Errorf("symbols: got %v, want %v", gotNames, wantNames)
	}
}

func TestParseFile_SymbolsSortedByQualifiedName(t *testing.T) {
	// Assertion: symbol arrays must be sorted by qualified_name.
	// This is a schema v1 invariant.
	fixturesDir := testdataRoot(t)
	for _, fixture := range []string{"simple_module.ts", "class_methods.ts", "arrow_functions.ts", "tsx_component.tsx"} {
		t.Run(fixture, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(fixturesDir, fixture))
			if err != nil {
				t.Fatalf("read %s: %v", fixture, err)
			}
			result, err := typescript.ParseFile(context.Background(), source, fixture)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}

			// Apply sort and compare.
			sorted := make([]schema.Symbol, len(result.Symbols))
			copy(sorted, result.Symbols)
			parser.SortSymbols(sorted)

			for i, s := range result.Symbols {
				if s.QualifiedName != sorted[i].QualifiedName {
					t.Errorf("symbol[%d] not sorted: got %q, expected sorted %q",
						i, s.QualifiedName, sorted[i].QualifiedName)
				}
			}
		})
	}
}

func TestParseFile_ClassMethods_BareMethodIndex(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "class_methods.ts"))
	if err != nil {
		t.Fatalf("read class_methods.ts: %v", err)
	}

	result, err := typescript.ParseFile(context.Background(), source, "class_methods.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// BareMethodIndex must have a "Counter" entry with key methods.
	counterMethods, ok := result.BareMethodIndex["Counter"]
	if !ok {
		t.Fatalf("BareMethodIndex missing Counter class; got keys: %v",
			func() []string {
				var keys []string
				for k := range result.BareMethodIndex {
					keys = append(keys, k)
				}
				return keys
			}())
	}

	wantMethods := []string{"constructor", "increment", "decrement", "reset", "get"}
	for _, m := range wantMethods {
		if _, ok := counterMethods[m]; !ok {
			t.Errorf("Counter BareMethodIndex missing method %q", m)
		}
	}
}

func TestParseFile_ArrowFunctions_ExtractedAsSymbols(t *testing.T) {
	// Arrow functions assigned to variable declarators must produce function symbols.
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "arrow_functions.ts"))
	if err != nil {
		t.Fatalf("read arrow_functions.ts: %v", err)
	}

	result, err := typescript.ParseFile(context.Background(), source, "arrow_functions.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	wantArrowNames := []string{"add", "multiply", "greet", "run"}
	gotFnNames := map[string]bool{}
	for _, s := range result.Symbols {
		if s.SymbolType == "function" {
			gotFnNames[s.Name] = true
		}
	}
	for _, want := range wantArrowNames {
		if !gotFnNames[want] {
			t.Errorf("arrow function %q not found in symbols", want)
		}
	}
}

func TestParseFile_QualifiedNameFormat(t *testing.T) {
	// qualified_name must be exactly "{rel_path}::{symbol_name}".
	source := []byte("function foo() {}\n")
	result, err := typescript.ParseFile(context.Background(), source, "subdir/module.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	for _, s := range result.Symbols {
		expected := "subdir/module.ts::" + s.Name
		if s.QualifiedName != expected {
			t.Errorf("qualified_name %q != expected %q", s.QualifiedName, expected)
		}
	}
}

// TestParseFile_MtsDispatch verifies that .mts files use the standard TypeScript
// grammar (not TSX). The grammar name must be "typescript", not "tsx".
func TestParseFile_MtsDispatch(t *testing.T) {
	source := []byte("export function hello(): string { return \"hello\"; }\n")
	result, err := typescript.ParseFile(context.Background(), source, "module.mts")
	if err != nil {
		t.Fatalf("ParseFile .mts: %v", err)
	}

	if result.GrammarName != "typescript" {
		t.Errorf(".mts must use 'typescript' grammar, got %q", result.GrammarName)
	}

	// Must extract at least the MODULE and the function symbol.
	if len(result.Symbols) < 2 {
		t.Errorf("expected at least 2 symbols for .mts file, got %d", len(result.Symbols))
	}
}

// TestParseFile_TsxGrammarVariant verifies that .tsx files use the TSX grammar.
// The grammar name in FileResult must be "tsx", not "typescript".
// This exercises the dispatch logic in selectGrammar().
func TestParseFile_TsxGrammarVariant(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "tsx_component.tsx"))
	if err != nil {
		t.Fatalf("read tsx_component.tsx: %v", err)
	}

	result, err := typescript.ParseFile(context.Background(), source, "tsx_component.tsx")
	if err != nil {
		t.Fatalf("ParseFile .tsx: %v", err)
	}

	// The key assertion: TSX grammar must be selected, not the standard TS grammar.
	if result.GrammarName != "tsx" {
		t.Errorf("tsx_component.tsx must use 'tsx' grammar, got %q", result.GrammarName)
	}

	// The file must have real symbols extracted (Greeter, App, formatName).
	funcNames := map[string]bool{}
	for _, s := range result.Symbols {
		if s.SymbolType == "function" {
			funcNames[s.Name] = true
		}
	}
	for _, want := range []string{"Greeter", "App", "formatName"} {
		if !funcNames[want] {
			t.Errorf("tsx_component.tsx missing function %q in symbols", want)
		}
	}
}

// TestParseFile_GrammarSelection_CaseInsensitive verifies that selectGrammar uses a
// case-insensitive suffix check so uppercase extensions route to the same grammar
// as their lowercase equivalents.
//
// Without the fix, "UPPERCASE.TSX" passes DetectLanguage (which lowercases before
// matching) but then falls through to the TypeScript grammar inside ParseFile,
// producing ERROR nodes for any JSX syntax in the file.
func TestParseFile_GrammarSelection_CaseInsensitive(t *testing.T) {
	cases := []struct {
		relPath     string
		wantGrammar string
	}{
		{"UPPERCASE.TSX", "tsx"},
		{"UPPERCASE.TS", "typescript"},
		{"UPPERCASE.MTS", "typescript"},
		{"MixedCase.Tsx", "tsx"},
		{"lower.tsx", "tsx"},
		{"lower.ts", "typescript"},
		{"lower.mts", "typescript"},
	}
	src := []byte("export function hello(): void {}\n")
	for _, tc := range cases {
		t.Run(tc.relPath, func(t *testing.T) {
			result, err := typescript.ParseFile(context.Background(), src, tc.relPath)
			if err != nil {
				t.Fatalf("ParseFile(%q): %v", tc.relPath, err)
			}
			if result.GrammarName != tc.wantGrammar {
				t.Errorf("ParseFile(%q): got grammar %q, want %q",
					tc.relPath, result.GrammarName, tc.wantGrammar)
			}
		})
	}
}

func TestParseFile_SyntaxError_EmitsParseError(t *testing.T) {
	// Invalid TypeScript: should still parse with HasError=true
	// and extract any valid symbols found before the error.
	source := []byte("function valid(): void {}\nx = @@@invalid;\n")
	result, err := typescript.ParseFile(context.Background(), source, "syntax_error.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Must detect an error.
	if !result.HasError {
		t.Error("syntax_error.ts must have HasError=true")
	}

	// Must still extract 'valid'.
	var found bool
	for _, s := range result.Symbols {
		if s.Name == "valid" {
			found = true
		}
	}
	if !found {
		t.Error("syntax_error.ts must still extract 'valid' despite parse error")
	}
}

func TestParseFile_ThisMethodCallResolution(t *testing.T) {
	// this.method() calls must resolve via the bare-method index.
	source := []byte(`
class Foo {
    doWork(): void {
        this.helper();
    }
    helper(): void {}
}
`)
	result, err := typescript.ParseFile(context.Background(), source, "foo.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// BareMethodIndex must have "Foo" with "doWork" and "helper".
	fooMethods, ok := result.BareMethodIndex["Foo"]
	if !ok {
		t.Fatalf("BareMethodIndex missing Foo class")
	}
	if _, ok := fooMethods["doWork"]; !ok {
		t.Errorf("BareMethodIndex[Foo] missing 'doWork'")
	}
	if _, ok := fooMethods["helper"]; !ok {
		t.Errorf("BareMethodIndex[Foo] missing 'helper'")
	}

	// Raw calls must include the this.helper() call.
	var foundThisCall bool
	for _, rc := range result.RawCalls {
		if rc.CalleeName == "helper" && rc.ReceiverText == "this" {
			foundThisCall = true
		}
	}
	if !foundThisCall {
		t.Errorf("expected a RawCall with CalleeName=helper ReceiverText=this")
	}
}

func TestParseFile_ImportMapBuilt(t *testing.T) {
	// Named imports must populate the import map.
	source := []byte(`
import { helper, transform } from "./utils";
export function process(): void {
    helper(1);
}
`)
	result, err := typescript.ParseFile(context.Background(), source, "main.ts")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// import map must have entries for "helper" and "transform".
	if _, ok := result.ImportMap["helper"]; !ok {
		t.Errorf("ImportMap missing 'helper'")
	}
	if _, ok := result.ImportMap["transform"]; !ok {
		t.Errorf("ImportMap missing 'transform'")
	}
}

// ---------------------------------------------------------------------------
// Parity test against committed *.expected.json fixtures
// ---------------------------------------------------------------------------

// TestParity is the TypeScript snapshot parity gate.
// It compares the Go binary's output against the committed *.expected.json
// fixture files.
//
// This test does NOT require any external tooling — it only reads committed fixtures.
//
// Run via: make snapshot-test (without PY_ONLY=1)
func TestParity(t *testing.T) {
	fixturesDir := testdataRoot(t)
	modRoot := moduleRoot(t)

	// Use t.TempDir() so each TestParity invocation builds to its own directory.
	// If TestParity runs concurrently with the Python TestParity (different package),
	// writing to a shared path would be a write race under -race.
	// t.TempDir() is per-test and automatically cleaned up after the test.
	binaryPath := filepath.Join(t.TempDir(), "codeweaver")

	// Build the binary for this test run.
	t.Logf("building codeweaver binary at %s", binaryPath)
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/codeweaver")
	cmd.Dir = modRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("build codeweaver: %v\n%s", buildErr, out)
	}

	// Single-file fixtures: each source file has a corresponding .expected.json.
	singleFileFixtures := []struct {
		name     string
		srcFile  string
		jsonFile string
	}{
		{
			name:     "simple_module",
			srcFile:  "simple_module.ts",
			jsonFile: "simple_module.expected.json",
		},
		{
			name:     "class_methods",
			srcFile:  "class_methods.ts",
			jsonFile: "class_methods.expected.json",
		},
		{
			name:     "arrow_functions",
			srcFile:  "arrow_functions.ts",
			jsonFile: "arrow_functions.expected.json",
		},
		{
			name:     "tsx_component",
			srcFile:  "tsx_component.tsx",
			jsonFile: "tsx_component.expected.json",
		},
	}

	for _, tc := range singleFileFixtures {
		t.Run(tc.name, func(t *testing.T) {
			absFixture := filepath.Join(fixturesDir, tc.srcFile)
			expectedPath := filepath.Join(fixturesDir, tc.jsonFile)

			// Read expected JSON.
			expectedBytes, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read expected %s: %v", tc.jsonFile, err)
			}

			// Run Go binary and normalize output.
			goOutput := runBinaryAndNormalize(t, binaryPath, fixturesDir, absFixture, modRoot)

			// Compare.
			if strings.TrimSpace(goOutput) != strings.TrimSpace(string(expectedBytes)) {
				t.Errorf("parity FAIL for %s:\ngot:  %s\nwant: %s",
					tc.name, goOutput, string(expectedBytes))
			}
		})
	}

	// Cross-file fixture: parse both .ts files together.
	t.Run("cross_file", func(t *testing.T) {
		crossDir := filepath.Join(fixturesDir, "cross_file")
		expectedPath := filepath.Join(crossDir, "cross_file.expected.json")

		expectedBytes, err := os.ReadFile(expectedPath)
		if err != nil {
			t.Fatalf("read expected cross_file.expected.json: %v", err)
		}

		utilsFile := filepath.Join(crossDir, "utils.ts")
		mainFile := filepath.Join(crossDir, "main.ts")

		cmd := exec.Command(binaryPath, "parse",
			"--workspace="+fixturesDir,
			utilsFile,
			mainFile,
		)
		cmd.Dir = modRoot
		rawOutput, cmdErr := cmd.Output()
		if cmdErr != nil {
			t.Fatalf("codeweaver parse cross_file: %v", cmdErr)
		}

		normalized := normalizeJSON(t, rawOutput, modRoot)
		if strings.TrimSpace(normalized) != strings.TrimSpace(string(expectedBytes)) {
			t.Errorf("parity FAIL for cross_file:\ngot:  %s\nwant: %s",
				normalized, string(expectedBytes))
		}
	})
}

// runBinaryAndNormalize runs `codeweaver parse` on a single file and returns normalized JSON.
func runBinaryAndNormalize(t *testing.T, binaryPath, workspace, absFile, modRoot string) string {
	t.Helper()
	cmd := exec.Command(binaryPath, "parse",
		"--workspace="+workspace,
		absFile,
	)
	cmd.Dir = modRoot
	rawOutput, err := cmd.Output()
	if err != nil {
		t.Fatalf("codeweaver parse %s: %v", absFile, err)
	}
	return normalizeJSON(t, rawOutput, modRoot)
}

// normalizeJSON runs the normalize.py script on raw JSON bytes and returns the result.
func normalizeJSON(t *testing.T, raw []byte, modRoot string) string {
	t.Helper()
	normScript := filepath.Join(modRoot, "testdata", "normalize.py")
	cmd := exec.Command("python3", normScript)
	cmd.Dir = modRoot
	cmd.Stdin = strings.NewReader(string(raw))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("normalize.py failed: %v\nraw input: %s", err, raw)
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// ModuleName computation
// ---------------------------------------------------------------------------

// TestParseFile_ModuleName asserts that ParseFile correctly computes the
// importable module key from the workspace-relative TypeScript file path.
//
// The ModuleName field is used by cli/parse.go to build the ModuleNameMap for
// cross-file call edge resolution (ResolveOptions.ModuleNameMap).
//
// Transformation rules (TypeScript):
//   - Strip the recognized extension (.ts, .tsx, .mts).
//   - Keep the workspace-relative path with forward slashes.
//
// The matching side (normalizeModuleRef in resolve.go) resolves a relative
// specifier like "./utils" from "cross_file/main.ts" to "cross_file/utils"
// and looks it up in the map.
func TestParseFile_ModuleName(t *testing.T) {
	cases := []struct {
		relPath        string
		wantModuleName string
	}{
		{"utils.ts", "utils"},
		{"main.ts", "main"},
		{"cross_file/utils.ts", "cross_file/utils"},
		{"cross_file/main.ts", "cross_file/main"},
		{"pkg/mod.mts", "pkg/mod"},
		{"components/Button.tsx", "components/Button"},
		{"a/b/c/d.ts", "a/b/c/d"},
	}

	src := []byte("export const x = 1;\n")
	for _, tc := range cases {
		t.Run(tc.relPath, func(t *testing.T) {
			result, err := typescript.ParseFile(context.Background(), src, tc.relPath)
			if err != nil {
				t.Fatalf("ParseFile(%q): %v", tc.relPath, err)
			}
			if result.ModuleName != tc.wantModuleName {
				t.Errorf("ModuleName: got %q, want %q", result.ModuleName, tc.wantModuleName)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Property tests: output invariants
// ---------------------------------------------------------------------------

func TestSymbolsSortedByQualifiedName_AllFixtures(t *testing.T) {
	// Property: symbol arrays in expected.json must be sorted by qualified_name.
	fixturesDir := testdataRoot(t)
	fixtures := []string{
		"simple_module.expected.json",
		"class_methods.expected.json",
		"arrow_functions.expected.json",
		"tsx_component.expected.json",
	}

	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(fixturesDir, fixture))
			if err != nil {
				t.Fatalf("read %s: %v", fixture, err)
			}

			var doc map[string]json.RawMessage
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("parse JSON: %v", err)
			}

			var symbols []map[string]interface{}
			if err := json.Unmarshal(doc["symbols"], &symbols); err != nil {
				t.Fatalf("parse symbols: %v", err)
			}

			for i := 1; i < len(symbols); i++ {
				prev := symbols[i-1]["qualified_name"].(string)
				curr := symbols[i]["qualified_name"].(string)
				if prev > curr {
					t.Errorf("symbols not sorted at index %d: %q > %q", i, prev, curr)
				}
			}
		})
	}
}

func TestEdgesSortedByTuple_ClassMethods(t *testing.T) {
	// Property: edge arrays must be sorted by (source, target, edge_type).
	fixturesDir := testdataRoot(t)
	data, err := os.ReadFile(filepath.Join(fixturesDir, "class_methods.expected.json"))
	if err != nil {
		t.Fatalf("read expected: %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	var edges []map[string]interface{}
	if err := json.Unmarshal(doc["edges"], &edges); err != nil {
		t.Fatalf("parse edges: %v", err)
	}

	for i := 1; i < len(edges); i++ {
		prev := edgeTuple(edges[i-1])
		curr := edgeTuple(edges[i])
		if prev > curr {
			t.Errorf("edges not sorted at index %d: %q > %q", i, prev, curr)
		}
	}
}

func edgeTuple(e map[string]interface{}) string {
	return e["source_qualified_name"].(string) + "|" +
		e["target_qualified_name"].(string) + "|" +
		e["edge_type"].(string)
}

func TestModuleSymbolAlwaysPresent(t *testing.T) {
	// Property: every file parse produces exactly one MODULE symbol.
	fixtures := []string{
		"function f(): void {}\n",
		"class C {}\n",
		"const x = () => 1;\n",
		"",
	}
	for i, src := range fixtures {
		result, err := typescript.ParseFile(context.Background(), []byte(src), "test.ts")
		if err != nil {
			t.Fatalf("ParseFile[%d]: %v", i, err)
		}
		var moduleCount int
		for _, s := range result.Symbols {
			if s.SymbolType == "module" {
				moduleCount++
			}
		}
		if moduleCount != 1 {
			t.Errorf("src[%d]: expected 1 MODULE symbol, got %d", i, moduleCount)
		}
	}
}

func TestAllEdgesReferenceKnownSymbols(t *testing.T) {
	// Property: every edge's source and target must be a known symbol in the same payload.
	// This tests the ResolveMultiFile filtering.
	fixturesDir := testdataRoot(t)
	data, err := os.ReadFile(filepath.Join(fixturesDir, "class_methods.expected.json"))
	if err != nil {
		t.Fatalf("read expected: %v", err)
	}

	var doc struct {
		Symbols []struct {
			QualifiedName string `json:"qualified_name"`
		} `json:"symbols"`
		Edges []struct {
			Source string `json:"source_qualified_name"`
			Target string `json:"target_qualified_name"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	known := map[string]bool{}
	for _, s := range doc.Symbols {
		known[s.QualifiedName] = true
	}

	for _, e := range doc.Edges {
		if !known[e.Source] {
			t.Errorf("edge source %q not in symbols", e.Source)
		}
		if !known[e.Target] {
			t.Errorf("edge target %q not in symbols", e.Target)
		}
	}
}

// ---------------------------------------------------------------------------
// Context cancellation tests (Wave 4c)
// ---------------------------------------------------------------------------

// TestParseFile_ContextCancelled verifies that a pre-cancelled context causes
// ParseFile to return gracefully (no panic) with a cancellation error.
func TestParseFile_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	src := []byte("function foo() {}\n")
	result, err := typescript.ParseFile(ctx, src, "cancelled.ts")

	if err == nil {
		t.Error("expected an error for cancelled context, got nil")
	}
	if result != nil && result.Symbols == nil {
		t.Error("result.Symbols should not be nil when result is non-nil")
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

// BenchmarkParseFileTypescript1k measures parse latency for a ~1000-line TypeScript file.
// Target: under 200ms on the dev machine (M3/M1 MacBook Pro).
// Baseline is committed to testdata/benchmarks/baseline.txt.
func BenchmarkParseFileTypescript1k(b *testing.B) {
	source := generateTypescript1k()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result, err := typescript.ParseFile(context.Background(), source, "bench_1k.ts")
		if err != nil {
			b.Fatalf("ParseFile: %v", err)
		}
		if result == nil {
			b.Fatal("nil result")
		}
	}
}

// generateTypescript1k generates a synthetic ~1000-line TypeScript file for benchmarking.
// The file has 60 classes with 12 methods each (plus a private field, constructor,
// and a blank line separator), followed by 20 top-level functions, producing
// approximately 1122 lines. This exercises class/method extraction, bare_method_index
// building, and top-level function detection — the same hot paths as the production parser.
//
// Line budget:
//   - File header:               2 lines
//   - 60 classes × 17 lines:  1020 lines  (1 "class {" + 1 field + 1 ctor + 12 methods + 1 "}" + 1 blank-line separator)
//   - 20 top-level functions ×  5 lines:  100 lines  (fn header + 3 body lines + 1 blank)
//   - Total:                   1122 lines
func generateTypescript1k() []byte {
	var sb strings.Builder
	sb.WriteString("// Synthetic 1000-line TypeScript file for BenchmarkParseFileTypescript1k.\n\n")

	// 60 classes × 17 lines each = 1020 lines.
	for i := 0; i < 60; i++ {
		sb.WriteString("class BenchClass")
		sb.WriteString(intStr(i))
		sb.WriteString(" {\n")
		sb.WriteString("    private value: number = 0;\n")
		sb.WriteString("    constructor() { this.value = 0; }\n")
		for j := 0; j < 12; j++ {
			sb.WriteString("    method")
			sb.WriteString(intStr(j))
			sb.WriteString("(): void { this.value += ")
			sb.WriteString(intStr(j + 1))
			sb.WriteString("; }\n")
		}
		sb.WriteString("}\n\n")
	}

	// 20 top-level functions × 5 lines each = 100 lines.
	for i := 0; i < 20; i++ {
		sb.WriteString("function benchFunc")
		sb.WriteString(intStr(i))
		sb.WriteString("(x: number): number {\n")
		sb.WriteString("    const result = x * ")
		sb.WriteString(intStr(i + 1))
		sb.WriteString(";\n")
		sb.WriteString("    return result;\n")
		sb.WriteString("}\n\n")
	}

	return []byte(sb.String())
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ---------------------------------------------------------------------------
// Fuzz
// ---------------------------------------------------------------------------

// FuzzParseFileTypescript fuzzes the TypeScript parser with random source bytes
// across both grammar variants (TypeScript and TSX).
//
// The invariant: the parser never panics, never hangs, and always returns
// either a valid result or an error — never partial JSON, never a crash.
// This invariant must hold for BOTH grammars: TsxLanguage() (the JSX-aware
// variant) is exercised when isTsx is true; TypescriptLanguage() when false.
//
// Why two-parameter fuzz? A single-parameter fuzz with a fixed relPath of
// "fuzz_input.ts" would never exercise the TSX grammar. Go's fuzz engine
// mutates all parameters independently — by adding a bool, it will explore
// both grammar paths across thousands of iterations, catching panics or hangs
// in either grammar variant.
//
// Run for 30 seconds: go test -fuzz=FuzzParseFileTypescript -fuzztime=30s
// Seeds are in testdata/fuzz/FuzzParseFileTypescript/
func FuzzParseFileTypescript(f *testing.F) {
	// Seed corpus: add each input for both grammar paths (isTsx=false and isTsx=true).
	seeds := [][]byte{
		[]byte(""),
		[]byte("function foo(): void {}\n"),
		[]byte("class Foo {\n    bar(): void {}\n}\n"),
		[]byte("x = @@@\n"),            // known to produce HasError=true
		[]byte("const fn = () => {\n"), // unclosed arrow
		[]byte("\x00\x01\x02\x03"),     // binary data
		[]byte("// just a comment\n"),
		[]byte("import { foo } from './bar';\n"),
		// TSX seed: JSX syntax — exercises TsxLanguage() when isTsx=true,
		// and confirms TypescriptLanguage() does not panic on JSX input.
		[]byte("function App(): JSX.Element { return <div />; }\n"),
	}
	for _, seed := range seeds {
		f.Add(seed, false) // TypeScript grammar
		f.Add(seed, true)  // TSX grammar
	}

	f.Fuzz(func(t *testing.T, src []byte, isTsx bool) {
		// Choose relPath based on the grammar variant flag.
		relPath := "fuzz_input.ts"
		if isTsx {
			relPath = "fuzz_input.tsx"
		}
		// The parser must not panic, and must return either a result or an error.
		result, err := typescript.ParseFile(context.Background(), src, relPath)
		if err != nil {
			return
		}
		if result == nil {
			t.Error("ParseFile returned nil result with nil error")
		}
	})
}

// ---------------------------------------------------------------------------
// Wave 4d: deep-nesting and NUL-byte hardening tests
// ---------------------------------------------------------------------------

// TestParse_DeepNesting_TypeScript verifies that parsing a TypeScript file with
// 5,000 levels of nested if statements does not cause a goroutine stack overflow.
//
// The wave 4d refactor replaced recursive AST walks with iterative DFS. This test
// verifies that the iterative implementation handles pathological nesting depth
// that would overflow the goroutine stack under the old recursive approach.
//
// See TestParse_DeepNesting_Python for the full rationale.
func TestParse_DeepNesting_TypeScript(t *testing.T) {
	// Build a TypeScript file with 5,000 levels of nested if statements.
	var src strings.Builder
	src.WriteString("function deep() {\n")
	for i := 0; i < 5000; i++ {
		indent := strings.Repeat("  ", i+1)
		src.WriteString(indent + "if (true) {\n")
	}
	// Add a trivial innermost statement.
	innerIndent := strings.Repeat("  ", 5001)
	src.WriteString(innerIndent + "const x = 1;\n")
	// Close all braces.
	for i := 4999; i >= 0; i-- {
		indent := strings.Repeat("  ", i+1)
		src.WriteString(indent + "}\n")
	}
	src.WriteString("}\n")

	source := []byte(src.String())

	result, err := typescript.ParseFile(context.Background(), source, "deep_nesting.ts")
	if err != nil {
		t.Logf("ParseFile returned error (acceptable for deep nesting): %v", err)
		return
	}
	if result == nil {
		t.Fatal("ParseFile returned nil result with nil error")
	}

	// The MODULE symbol must always be present.
	foundModule := false
	for _, s := range result.Symbols {
		if s.SymbolType == "module" {
			foundModule = true
			break
		}
	}
	if !foundModule {
		t.Errorf("MODULE symbol missing from deeply-nested TypeScript result")
	}
}

// TestParse_NULByteInImportPath_TypeScript verifies that a TypeScript import
// statement containing a NUL byte in the module path does not propagate the NUL
// into the import map.
func TestParse_NULByteInImportPath_TypeScript(t *testing.T) {
	// Construct TypeScript source with a NUL byte in the import path.
	src := []byte("import { helper } from \"./mod\x00ule\";\nfunction foo() { helper(); }\n")

	result, err := typescript.ParseFile(context.Background(), src, "nul_import.ts")
	if err != nil {
		t.Logf("ParseFile returned error for NUL source (acceptable): %v", err)
	}
	if result == nil {
		return
	}

	// Verify: no import map key or value contains a NUL byte.
	for k, v := range result.ImportMap {
		if strings.ContainsRune(k, '\x00') {
			t.Errorf("import map key contains NUL byte: %q", k)
		}
		if strings.ContainsRune(v, '\x00') {
			t.Errorf("import map value contains NUL byte: %q = %q", k, v)
		}
	}
}

// TestParse_ControlCharsInImportPath_TypeScript verifies that control characters
// in import paths are stripped by sanitizeImportPath before being stored in the
// import map. The function strips all code points < 0x20 except \t, \n, \r.
//
// Background: sanitizeImportPath is the gatekeeper between raw tree-sitter node
// text and the import map. Think of it as a mail sorter that throws away envelopes
// with corrupted addresses — the letters (control chars) would never reach the
// right destination anyway, so we discard them silently rather than corrupt the
// routing table.
//
// Characters tested:
//   - 0x01 SOH (Start of Heading) — at the start of an import path
//   - 0x07 BEL (Bell) — in the middle of an import path
//   - 0x1F US (Unit Separator) — at the end of an import path
//   - Combination: 0x00 NUL + 0x01 SOH + 0x07 BEL in a single import path
//
// These extend the NUL-byte test above to cover the full < 0x20 range
// that sanitizeImportPath is documented to strip.
func TestParse_ControlCharsInImportPath_TypeScript(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
	}{
		{
			name: "SOH_at_start",
			// 0x01 (SOH) before the module path: import from "\x01./utils"
			src: []byte("import { foo } from \"\x01./utils\";\nfunction bar() { foo(); }\n"),
		},
		{
			name: "BEL_in_middle",
			// 0x07 (BEL) inside the module path: import from "./ut\x07ils"
			src: []byte("import { foo } from \"./ut\x07ils\";\nfunction bar() { foo(); }\n"),
		},
		{
			name: "US_at_end",
			// 0x1F (Unit Separator) at the end of the module path: import from "./utils\x1F"
			src: []byte("import { foo } from \"./utils\x1f\";\nfunction bar() { foo(); }\n"),
		},
		{
			name: "NUL_SOH_BEL_combination",
			// Multiple control chars in one path: import from "./mo\x00d\x01ul\x07e"
			src: []byte("import { foo } from \"./mo\x00d\x01ul\x07e\";\nfunction bar() { foo(); }\n"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := typescript.ParseFile(context.Background(), tc.src, "ctrl_import.ts")
			if err != nil {
				// Parse error is acceptable for malformed source — what matters is that
				// the import map (if populated) contains no control characters.
				t.Logf("ParseFile returned error for control-char source (acceptable): %v", err)
			}
			if result == nil {
				return
			}

			// None of the import map keys or values may contain a control character
			// in the range [0x00, 0x20) (excluding \t \n \r, which are permitted).
			for k, v := range result.ImportMap {
				for _, r := range k {
					if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
						t.Errorf("import map key contains control char 0x%02x in test %q: %q",
							r, tc.name, k)
					}
				}
				for _, r := range v {
					if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
						t.Errorf("import map value contains control char 0x%02x in test %q: key=%q val=%q",
							r, tc.name, k, v)
					}
				}
			}
		})
	}
}
