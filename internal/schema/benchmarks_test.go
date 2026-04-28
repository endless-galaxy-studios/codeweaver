package schema_test

// BenchmarkJSONMarshal measures the cost of marshaling a representative Document
// to JSON. This is the hot path in the parse subcommand: after all files are
// parsed, the entire Document is marshaled into a bytes.Buffer and written to
// stdout in a single call.
//
// The synthetic document uses 100 symbols and 50 edges — representative of a
// medium-sized codebase file (e.g., a 500-line Python module with a mix of
// classes and functions, where each class has a few methods).
//
// # Why this benchmark matters
//
// Tree-sitter parsing is fast. The JSON marshaling step — building Go structs
// from the parsed tree and then calling encoding/json — is where allocations
// pile up. This benchmark isolates the marshal path so we can catch regressions
// (e.g., adding an unintentional allocation per symbol) without noise from the
// parse step.
//
// Baseline committed to testdata/benchmarks/baseline.txt.
// Regression gate: >20% increase in ns/op or B/op triggers CI failure via make bench-check.
//
// Run manually:
//
//	go test -bench=BenchmarkJSONMarshal -benchmem -count=3 ./internal/schema/
import (
	"encoding/json"
	"testing"

	"codeweaver/internal/schema"
)

// buildSyntheticDocument constructs a Document with numSymbols symbols and
// numEdges edges. All fields are populated with realistic-length strings so the
// benchmark exercises the full marshaling path including long field values.
//
// Symbol IDs follow the schema v1 QualifiedName format: "pkg/file.py::SymbolN".
// Edge endpoints are drawn from the symbol set so the document is internally consistent.
func buildSyntheticDocument(numSymbols, numEdges int) *schema.Document {
	symbols := make([]schema.Symbol, 0, numSymbols)
	for i := 0; i < numSymbols; i++ {
		line := i*5 + 1
		lineEnd := line + 4
		symbols = append(symbols, schema.Symbol{
			QualifiedName: benchQualName(i),
			Name:          benchName(i),
			FilePath:      "internal/bench/module.py",
			SymbolType:    benchSymbolType(i),
			Language:      "python",
			LineStart:     &line,
			LineEnd:       &lineEnd,
		})
	}

	edges := make([]schema.Edge, 0, numEdges)
	for i := 0; i < numEdges; i++ {
		// Draw source and target from the symbol pool in a deterministic pattern.
		srcIdx := i % numSymbols
		tgtIdx := (i + numSymbols/2) % numSymbols
		if srcIdx == tgtIdx {
			tgtIdx = (tgtIdx + 1) % numSymbols
		}
		edges = append(edges, schema.Edge{
			SourceQualifiedName: benchQualName(srcIdx),
			TargetQualifiedName: benchQualName(tgtIdx),
			EdgeType:            "calls",
		})
	}

	deleted := []string{}
	return &schema.Document{
		SchemaVersion: schema.SchemaVersionV1,
		ParserVersion:   "0.1.0",
		ParsedAt:        "2026-04-27T00:00:00Z",
		FileCount:       1,
		Symbols:         symbols,
		Edges:           edges,
		DeletedFiles:    deleted,
	}
}

// benchQualName returns a realistic qualified name for symbol index i.
func benchQualName(i int) string {
	// Format: "internal/bench/module.py::ClassName.methodN" or "::topLevelFuncN"
	if i%10 == 0 {
		return "internal/bench/module.py::BenchClass" + itoa(i/10)
	}
	classIdx := (i / 10)
	methodIdx := i%10 - 1
	return "internal/bench/module.py::BenchClass" + itoa(classIdx) + ".method" + itoa(methodIdx)
}

// benchName returns the short symbol name for symbol index i.
func benchName(i int) string {
	if i%10 == 0 {
		return "BenchClass" + itoa(i/10)
	}
	classIdx := i / 10
	methodIdx := i%10 - 1
	return "BenchClass" + itoa(classIdx) + ".method" + itoa(methodIdx)
}

// benchSymbolType returns "class" for class symbols and "function" for methods.
func benchSymbolType(i int) string {
	if i%10 == 0 {
		return "class"
	}
	return "function"
}

// itoa converts an int to a string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 10)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// BenchmarkJSONMarshal measures encoding/json.Marshal throughput for a
// representative Document (100 symbols + 50 edges).
//
// Reading the output:
//   - ns/op: nanoseconds to marshal one Document. Sub-millisecond is fast;
//     above 10ms is a signal to investigate allocation discipline.
//   - B/op: bytes allocated per marshal call. Low allocations mean the
//     JSON encoder is doing minimal copying; high values indicate opportunities
//     for sync.Pool or pre-sized buffers.
//   - allocs/op: number of distinct heap allocations per call.
func BenchmarkJSONMarshal(b *testing.B) {
	doc := buildSyntheticDocument(100, 50)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(doc); err != nil {
			b.Fatal(err)
		}
	}
}
