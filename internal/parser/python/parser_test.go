package python_test

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
	"codeweaver/internal/parser/python"
	"codeweaver/internal/schema"
)

// testdataRoot returns the path to testdata/fixtures/python/ relative to the
// module root. Uses runtime.Caller to locate the test file, then walks up.
func testdataRoot(t *testing.T) string {
	t.Helper()
	_, callerFile, _, _ := runtime.Caller(0)
	// callerFile is: .../codeweaver-go/internal/parser/python/parser_test.go
	// testdata is at: .../codeweaver-go/testdata/fixtures/python/
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(callerFile))))
	return filepath.Join(moduleRoot, "testdata", "fixtures", "python")
}

// ---------------------------------------------------------------------------
// Unit tests: symbol extraction
// ---------------------------------------------------------------------------

func TestParseFile_EmptyModule(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "empty_module.py"))
	if err != nil {
		t.Fatalf("read empty_module.py: %v", err)
	}

	result, err := python.ParseFile(context.Background(), source, "empty_module.py")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Must have exactly one symbol: the MODULE symbol.
	if len(result.Symbols) != 1 {
		t.Errorf("expected 1 symbol (MODULE), got %d: %v", len(result.Symbols), result.Symbols)
	}
	if len(result.Symbols) > 0 && result.Symbols[0].SymbolType != "module" {
		t.Errorf("first symbol must be module type, got %q", result.Symbols[0].SymbolType)
	}

	// Must have zero raw calls.
	if len(result.RawCalls) != 0 {
		t.Errorf("expected 0 raw calls, got %d", len(result.RawCalls))
	}

	// Must not have parse errors.
	if result.HasError {
		t.Errorf("empty_module.py should not have parse errors")
	}
}

func TestParseFile_SimpleModule_Symbols(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "simple_module.py"))
	if err != nil {
		t.Fatalf("read simple_module.py: %v", err)
	}

	result, err := python.ParseFile(context.Background(), source, "simple_module.py")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Expected symbols: MODULE, greet, shout, main.
	wantNames := []string{"MODULE", "greet", "main", "shout"}
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
	// This is a contract invariant; consumers rely on it for deterministic output.
	fixturesDir := testdataRoot(t)
	for _, fixture := range []string{"simple_module.py", "class_methods.py", "imports_from.py"} {
		t.Run(fixture, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(fixturesDir, fixture))
			if err != nil {
				t.Fatalf("read %s: %v", fixture, err)
			}

			result, err := python.ParseFile(context.Background(), source, fixture)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}

			// Apply sort and compare.
			sorted := make([]schema.Symbol, len(result.Symbols))
			copy(sorted, result.Symbols)
			parser.SortSymbols(sorted)

			for i, s := range result.Symbols {
				if s.QualifiedName != sorted[i].QualifiedName {
					t.Errorf("symbol[%d] not sorted: got %q, expected sorted %q", i, s.QualifiedName, sorted[i].QualifiedName)
				}
			}
		})
	}
}

func TestParseFile_ClassMethods_BareMethodIndex(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "class_methods.py"))
	if err != nil {
		t.Fatalf("read class_methods.py: %v", err)
	}

	result, err := python.ParseFile(context.Background(), source, "class_methods.py")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// BareMethodIndex must have a "Counter" entry with all its methods.
	counterMethods, ok := result.BareMethodIndex["Counter"]
	if !ok {
		t.Fatalf("BareMethodIndex missing Counter class")
	}

	wantMethods := []string{"__init__", "increment", "decrement", "reset", "get"}
	for _, m := range wantMethods {
		if _, ok := counterMethods[m]; !ok {
			t.Errorf("Counter BareMethodIndex missing method %q", m)
		}
	}
}

func TestParseFile_SyntaxError_EmitsParseError(t *testing.T) {
	fixturesDir := testdataRoot(t)
	source, err := os.ReadFile(filepath.Join(fixturesDir, "syntax_error.py"))
	if err != nil {
		t.Fatalf("read syntax_error.py: %v", err)
	}

	result, err := python.ParseFile(context.Background(), source, "syntax_error.py")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	// Must detect an error.
	if !result.HasError {
		t.Error("syntax_error.py must have HasError=true")
	}

	// Must still extract valid_function.
	var found bool
	for _, s := range result.Symbols {
		if s.Name == "valid_function" {
			found = true
		}
	}
	if !found {
		t.Error("syntax_error.py must still extract valid_function despite parse error")
	}
}

func TestParseFile_QualifiedNameFormat(t *testing.T) {
	// qualified_name must be exactly "{rel_path}::{symbol_name}"
	source := []byte("def foo(): pass\n")
	result, err := python.ParseFile(context.Background(), source, "subdir/module.py")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	for _, s := range result.Symbols {
		expected := "subdir/module.py::" + s.Name
		if s.QualifiedName != expected {
			t.Errorf("qualified_name %q != expected %q", s.QualifiedName, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// ModuleName computation
// ---------------------------------------------------------------------------

// TestParseFile_ModuleName asserts that ParseFile correctly computes the
// importable dotted module name from the workspace-relative file path.
//
// The ModuleName field is used by cli/parse.go to build the ModuleNameMap for
// cross-file call edge resolution (ResolveOptions.ModuleNameMap).
//
// Transformation rules (Python):
//   - Strip the ".py" suffix.
//   - Replace "/" with "." (dotted package notation).
func TestParseFile_ModuleName(t *testing.T) {
	cases := []struct {
		relPath        string
		wantModuleName string
	}{
		{"utils.py", "utils"},
		{"main.py", "main"},
		{"pkg/sub/mod.py", "pkg.sub.mod"},
		{"cross_file_calls/utils.py", "cross_file_calls.utils"},
		{"cross_file_calls/main.py", "cross_file_calls.main"},
		{"a/b/c/d.py", "a.b.c.d"},
	}

	src := []byte("x = 1\n")
	for _, tc := range cases {
		t.Run(tc.relPath, func(t *testing.T) {
			result, err := python.ParseFile(context.Background(), src, tc.relPath)
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
// Parity test against committed *.expected.json fixtures
// ---------------------------------------------------------------------------

// TestParity is the snapshot parity gate.
// It compares the Go binary's output (via subprocess) against the committed
// *.expected.json fixture files.
//
// This test does NOT require any external tooling — it only reads the committed
// *.expected.json files and runs the Go binary.
//
// Run via: make snapshot-test PY_ONLY=1
func TestParity(t *testing.T) {
	fixturesDir := testdataRoot(t)
	_, callerFile, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(callerFile))))
	// Use t.TempDir() so each TestParity invocation builds to its own directory.
	// If TestParity runs concurrently with the TypeScript parser's TestParity,
	// writing to a shared moduleRoot path would be a write race under -race.
	// t.TempDir() is per-test and automatically cleaned up after the test completes.
	binaryPath := filepath.Join(t.TempDir(), "codeweaver")

	// Build the binary for this test run.
	t.Logf("building codeweaver binary at %s", binaryPath)
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/codeweaver")
	cmd.Dir = moduleRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("build codeweaver: %v\n%s", buildErr, out)
	}

	// Single-file fixtures: each .py file has a corresponding .expected.json.
	singleFileFixtures := []struct {
		name     string
		pyFile   string
		jsonFile string
		// intentionalDiff documents an INTENTIONAL deviation from the committed fixture baseline.
		// Set to non-empty string when the Go binary intentionally differs from the fixture.
		intentionalDiff string
	}{
		{
			name:     "empty_module",
			pyFile:   "empty_module.py",
			jsonFile: "empty_module.expected.json",
		},
		{
			name:     "simple_module",
			pyFile:   "simple_module.py",
			jsonFile: "simple_module.expected.json",
		},
		{
			name:     "class_methods",
			pyFile:   "class_methods.py",
			jsonFile: "class_methods.expected.json",
		},
		{
			name:     "imports_from",
			pyFile:   "imports_from.py",
			jsonFile: "imports_from.expected.json",
		},
		{
			name:     "syntax_error",
			pyFile:   "syntax_error.py",
			jsonFile: "syntax_error.expected.json",
			intentionalDiff: "INTENTIONAL: Go binary emits parse_errors; the reference parser did not. " +
				"The parse_errors field is a codeweaver enhancement (E_PARSE_INCOMPLETE). " +
				"The expected JSON was updated to match Go binary output.",
		},
	}

	for _, tc := range singleFileFixtures {
		t.Run(tc.name, func(t *testing.T) {
			absFixture := filepath.Join(fixturesDir, tc.pyFile)
			expectedPath := filepath.Join(fixturesDir, tc.jsonFile)

			// Read expected JSON.
			expectedBytes, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read expected %s: %v", tc.jsonFile, err)
			}

			// Run Go binary and normalize output.
			goOutput := runBinaryAndNormalize(t, binaryPath, fixturesDir, absFixture, moduleRoot)

			// Compare.
			// Note: intentionalDiff is kept as documentation in the table above but no
			// longer bypasses the snapshot assertion. All fixtures round-trip cleanly
			// against their committed .expected.json snapshots.
			if strings.TrimSpace(goOutput) != strings.TrimSpace(string(expectedBytes)) {
				t.Errorf("parity FAIL for %s:\ngot:  %s\nwant: %s", tc.name, goOutput, string(expectedBytes))
			}
		})
	}

	// Cross-file fixture: parse both files together.
	t.Run("cross_file_calls", func(t *testing.T) {
		crossDir := filepath.Join(fixturesDir, "cross_file_calls")
		expectedPath := filepath.Join(crossDir, "cross_file_calls.expected.json")

		expectedBytes, err := os.ReadFile(expectedPath)
		if err != nil {
			t.Fatalf("read expected cross_file_calls.expected.json: %v", err)
		}

		utilsFile := filepath.Join(crossDir, "utils.py")
		mainFile := filepath.Join(crossDir, "main.py")

		// Run binary with both files.
		cmd := exec.Command(binaryPath, "parse",
			"--workspace="+fixturesDir,
			utilsFile,
			mainFile,
		)
		cmd.Dir = moduleRoot
		rawOutput, cmdErr := cmd.Output()
		if cmdErr != nil {
			t.Fatalf("codeweaver parse cross_file_calls: %v", cmdErr)
		}

		normalized := normalizeJSON(t, rawOutput, moduleRoot)
		if strings.TrimSpace(normalized) != strings.TrimSpace(string(expectedBytes)) {
			t.Errorf("parity FAIL for cross_file_calls:\ngot:  %s\nwant: %s",
				normalized, string(expectedBytes))
		}
	})
}

// runBinaryAndNormalize runs `codeweaver parse` on a single file and returns normalized JSON.
func runBinaryAndNormalize(t *testing.T, binaryPath, workspace, absFile, moduleRoot string) string {
	t.Helper()
	cmd := exec.Command(binaryPath, "parse",
		"--workspace="+workspace,
		absFile,
	)
	cmd.Dir = moduleRoot
	rawOutput, err := cmd.Output()
	if err != nil {
		t.Fatalf("codeweaver parse %s: %v", absFile, err)
	}
	return normalizeJSON(t, rawOutput, moduleRoot)
}

// normalizeJSON runs the normalize.py script on raw JSON bytes and returns the result.
func normalizeJSON(t *testing.T, raw []byte, moduleRoot string) string {
	t.Helper()
	normScript := filepath.Join(moduleRoot, "testdata", "normalize.py")
	cmd := exec.Command("python3", normScript)
	cmd.Dir = moduleRoot
	cmd.Stdin = strings.NewReader(string(raw))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("normalize.py failed: %v\nraw input: %s", err, raw)
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// Property tests: output invariants
// ---------------------------------------------------------------------------

func TestSymbolsSortedByQualifiedName(t *testing.T) {
	// Property: symbol arrays are sorted by qualified_name in all fixture outputs.
	fixturesDir := testdataRoot(t)
	fixtures := []string{
		"simple_module.py",
		"class_methods.py",
		"imports_from.py",
		"empty_module.py",
	}

	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			expectedPath := strings.TrimSuffix(filepath.Join(fixturesDir, fixture), ".py") + ".expected.json"
			data, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatalf("read %s: %v", expectedPath, err)
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

func TestEdgesSortedByTuple(t *testing.T) {
	// Property: edge arrays are sorted by (source_qualified_name, target_qualified_name, edge_type).
	fixturesDir := testdataRoot(t)
	expectedPath := filepath.Join(fixturesDir, "class_methods.expected.json")
	data, err := os.ReadFile(expectedPath)
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

func TestEmptyModuleHasOneSymbolNoEdges(t *testing.T) {
	// Property: empty_module.py produces exactly 1 symbol (MODULE) and 0 edges.
	fixturesDir := testdataRoot(t)
	data, err := os.ReadFile(filepath.Join(fixturesDir, "empty_module.expected.json"))
	if err != nil {
		t.Fatalf("read expected: %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}

	var symbols []interface{}
	if err := json.Unmarshal(doc["symbols"], &symbols); err != nil {
		t.Fatalf("parse symbols: %v", err)
	}
	if len(symbols) != 1 {
		t.Errorf("empty_module.py: expected 1 symbol, got %d", len(symbols))
	}

	var edges []interface{}
	if err := json.Unmarshal(doc["edges"], &edges); err != nil {
		t.Fatalf("parse edges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("empty_module.py: expected 0 edges, got %d", len(edges))
	}
}

// ---------------------------------------------------------------------------
// Context cancellation tests (Wave 4c)
// ---------------------------------------------------------------------------

// TestParseFile_ContextCancelled verifies that a pre-cancelled context causes
// ParseFile to return gracefully (no panic) with a parse_errors-style error.
//
// Think of it like asking a chef to cook a meal after the restaurant has
// already closed — the chef should politely decline, not crash the kitchen.
func TestParseFile_ContextCancelled(t *testing.T) {
	// Pre-cancel the context before calling ParseFile.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	src := []byte("def foo():\n    pass\n")
	result, err := python.ParseFile(ctx, src, "cancelled.py")

	// Must not panic. Must return an error describing the cancellation.
	if err == nil {
		t.Error("expected an error for cancelled context, got nil")
	}
	// Result may be partial (non-nil with empty symbols) or nil, but must not panic.
	// Both outcomes are valid for a pre-cancelled context.
	if result != nil && result.Symbols == nil {
		t.Error("result.Symbols should not be nil when result is non-nil")
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

// BenchmarkParseFilePython1k measures the parse latency for a ~1000-line Python file.
// Target: under 200ms on the dev machine (M3 MacBook Pro).
// This baseline is committed to testdata/benchmarks/baseline.txt via `go test -bench`.
func BenchmarkParseFilePython1k(b *testing.B) {
	source := generatePython1k()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result, err := python.ParseFile(context.Background(), source, "bench_1k.py")
		if err != nil {
			b.Fatalf("ParseFile: %v", err)
		}
		if result == nil {
			b.Fatal("nil result")
		}
	}
}

// generatePython1k generates a synthetic ~1000-line Python file for benchmarking.
// The file has 100 classes with 9 methods each, giving a realistic workload
// that exercises the class/method extraction and bare_method_index building.
func generatePython1k() []byte {
	var sb strings.Builder
	sb.WriteString("# Synthetic 1000-line Python file for BenchmarkParseFilePython1k.\n\n")

	for i := 0; i < 100; i++ {
		sb.WriteString("class BenchClass")
		sb.WriteString(intStr(i))
		sb.WriteString(":\n")
		sb.WriteString("    def __init__(self):\n        self.value = 0\n\n")
		for j := 0; j < 8; j++ {
			sb.WriteString("    def method")
			sb.WriteString(intStr(j))
			sb.WriteString("(self):\n        self.value += ")
			sb.WriteString(intStr(j + 1))
			sb.WriteString("\n\n")
		}
		sb.WriteString("\n")
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

// FuzzParseFilePython fuzzes the python-language parser with random source bytes.
// The invariant: the parser never panics, never hangs, and always returns
// either a valid result or an error — never partial JSON, never a crash.
//
// Run for 30 seconds: go test -fuzz=FuzzParseFilePython -fuzztime=30s
func FuzzParseFilePython(f *testing.F) {
	// Seed corpus: known-good inputs and edge cases.
	f.Add([]byte(""))
	f.Add([]byte("def foo(): pass\n"))
	f.Add([]byte("class Foo:\n    def bar(self): pass\n"))
	f.Add([]byte("x = @@@\n"))          // known to produce HasError=true
	f.Add([]byte("def broken(x, y)\n")) // missing colon
	f.Add([]byte("\x00\x01\x02\x03"))   // binary data
	f.Add([]byte("# just a comment\n"))
	f.Add([]byte("from os import path\ndef f(): return path.join('a', 'b')\n"))

	f.Fuzz(func(t *testing.T, src []byte) {
		// The parser must not panic, and must return either a result or an error.
		// If it returns a result, the result must be non-nil.
		result, err := python.ParseFile(context.Background(), src, "fuzz_input.py")
		if err != nil {
			// Error is acceptable (e.g., nil tree from gotreesitter on extreme input).
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

// TestParse_DeepNesting_Python verifies that parsing a file with 5,000 levels of
// nested function definitions does not cause a goroutine stack overflow.
//
// # Why this matters
//
// Go goroutines start with a small stack (8 KB) that grows dynamically, but
// deeply-nested recursive function calls can still exhaust heap memory or hit
// the runtime's implicit limit in pathological cases. The wave 4d refactor
// replaced all recursive AST walks with iterative DFS (explicit stack in heap
// memory), which has no practical depth limit.
//
// This test generates a Python file with 5,000 nested if-statement levels
// (which produces a deeply-nested AST) and verifies that ParseFile completes
// without crashing.
//
// Why 5,000? Empirically, ~5,000 levels of Python nesting triggers visible
// recursion depth in Python itself, and the AST depth mirrors that structure.
// With the iterative walk, 5,000 levels is trivial. With a recursive walk,
// it would likely cause a stack overflow.
func TestParse_DeepNesting_Python(t *testing.T) {
	// Build a Python file with 5,000 levels of nested if statements.
	// Each level adds 2 lines (if condition: + body), so this is a 10,000-line file.
	var src strings.Builder
	src.WriteString("def deep():\n")
	for i := 0; i < 5000; i++ {
		indent := strings.Repeat("    ", i+1)
		src.WriteString(indent + "if True:\n")
	}
	// Add a trivial innermost statement so the tree is syntactically valid.
	innerIndent := strings.Repeat("    ", 5001)
	src.WriteString(innerIndent + "pass\n")

	source := []byte(src.String())

	// The parse must complete without panic or hang.
	// We do not use a context timeout here because the test is verifying that
	// the parse completes, not that it completes within a deadline.
	result, err := python.ParseFile(context.Background(), source, "deep_nesting.py")
	if err != nil {
		// A parse error (HasError) is acceptable for deeply-nested code.
		// What we're testing is that the binary doesn't panic or hang.
		t.Logf("ParseFile returned error (acceptable for deep nesting): %v", err)
		return
	}
	if result == nil {
		t.Fatal("ParseFile returned nil result with nil error")
	}

	// The MODULE symbol must always be present regardless of nesting depth.
	foundModule := false
	for _, s := range result.Symbols {
		if s.SymbolType == "module" {
			foundModule = true
			break
		}
	}
	if !foundModule {
		t.Errorf("MODULE symbol missing from deeply-nested parse result: %v", result.Symbols)
	}
}

// TestParse_NULByteInImportPath_Python verifies that an import statement containing
// a NUL byte in the module name does not propagate the NUL into the import map.
//
// NUL bytes in source files are always malformed — they indicate either a binary
// file accidentally included as source, or deliberately crafted input. Tree-sitter
// classifies such inputs via ERROR/MISSING nodes (E_PARSE_INCOMPLETE), but our
// sanitizeImportPath function additionally strips NUL bytes from extracted import
// paths to prevent corrupting the module map.
func TestParse_NULByteInImportPath_Python(t *testing.T) {
	// Construct a Python import statement with a NUL byte in the module name.
	// This simulates malformed source that a real parser might encounter.
	//
	// Note: tree-sitter may or may not parse this correctly; what we care about
	// is that whatever module name IS extracted does not contain a NUL byte.
	src := []byte("from mod\x00ule import helper\ndef foo(): helper()\n")

	result, err := python.ParseFile(context.Background(), src, "nul_import.py")
	if err != nil {
		// Parse error is acceptable for malformed source.
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

// TestParse_ControlCharsInImportPath_Python verifies that control characters
// in import paths are stripped by sanitizeImportPath before being stored in the
// import map. The function strips all code points < 0x20 except \t, \n, \r.
//
// This mirrors TestParse_ControlCharsInImportPath_TypeScript — both parsers share
// the same sanitization contract (strip < 0x20 except \t \n \r) so both need this
// coverage. The symmetric addition ensures that a regression in either parser's
// sanitizeImportPath is caught regardless of which test file is run.
//
// See TestParse_NULByteInImportPath_Python for the rationale on NUL-byte handling.
func TestParse_ControlCharsInImportPath_Python(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
	}{
		{
			name: "SOH_at_start",
			// 0x01 (SOH) before the module name in a from-import
			src: []byte("from \x01utils import helper\ndef foo(): helper()\n"),
		},
		{
			name: "BEL_in_middle",
			// 0x07 (BEL) inside the module name
			src: []byte("from ut\x07ils import helper\ndef foo(): helper()\n"),
		},
		{
			name: "US_at_end",
			// 0x1F (Unit Separator) at the end of the module name
			src: []byte("from utils\x1f import helper\ndef foo(): helper()\n"),
		},
		{
			name: "NUL_SOH_BEL_combination",
			// Multiple control chars in one module name
			src: []byte("from mo\x00d\x01ul\x07e import helper\ndef foo(): helper()\n"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := python.ParseFile(context.Background(), tc.src, "ctrl_import.py")
			if err != nil {
				// Parse error is acceptable for malformed source.
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
