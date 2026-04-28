// Package schema_test contains property tests for the output schema v1.
//
// # Property tests vs. example tests
//
// Example tests (like TestDocumentMarshalRoundTrip in types_test.go) pin a
// specific input to a specific expected output. Think of them like a recipe card:
// "given these exact ingredients, produce this exact dish."
//
// Property tests assert invariants that must hold for *any* valid input, not just
// one hand-picked example. Think of them like food-safety rules: "regardless of
// what's in the dish, it must reach 165 °F internally." Property tests don't tell
// you what the output looks like — they tell you what constraints it must satisfy
// no matter what.
//
// The four properties below assert contract invariants that are relied upon by
// downstream consumers (plugins, language servers, agents).
// Violating any of them is a silent breaking change.
//
// Fixtures used: all 11 committed *.expected.json files across both language
// fixture directories:
//   - testdata/fixtures/python/     (5 top-level + 1 cross_file_calls sub-fixture)
//   - testdata/fixtures/typescript/ (4 top-level + 1 cross_file sub-fixture)
//
// Each property iterates all 11; a failure in any fixture is reported per-file
// using subtest names of the form "{language}/{fixture_name}".
package schema_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// moduleRoot returns the absolute path to the codeweaver-go module root.
// Uses runtime.Caller to locate this source file, then walks up three
// directory levels (contract -> internal -> codeweaver-go).
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, callerFile, _, _ := runtime.Caller(0)
	// callerFile is: .../codeweaver/internal/schema/properties_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(callerFile)))
}

// schemaPath returns the absolute path to schema/codeweaver-v1.json.
func schemaPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "schema", "codeweaver-v1.json")
}

// allFixtures returns paths to all 11 committed *.expected.json fixtures
// across both Python (6) and TypeScript (5) fixture directories.
//
// Python fixtures (6):
//   - 5 top-level under testdata/fixtures/python/
//   - 1 in testdata/fixtures/python/cross_file_calls/
//
// TypeScript fixtures (5):
//   - 4 top-level under testdata/fixtures/typescript/
//   - 1 in testdata/fixtures/typescript/cross_file/
//
// Subtest names use the scheme "{language}/{fixture_name}" so failure
// messages are unambiguous (e.g., "typescript/class_methods.expected.json").
func allFixtures(t *testing.T) []string {
	t.Helper()
	root := moduleRoot(t)

	var fixtures []string

	// ---- Python fixtures ----
	pyRoot := filepath.Join(root, "testdata", "fixtures", "python")

	pyTopLevel, err := filepath.Glob(filepath.Join(pyRoot, "*.expected.json"))
	if err != nil {
		t.Fatalf("glob python top-level fixtures: %v", err)
	}
	if len(pyTopLevel) == 0 {
		t.Fatal("no top-level *.expected.json fixtures found — check testdata/fixtures/python/")
	}
	fixtures = append(fixtures, pyTopLevel...)

	pyCrossFile := filepath.Join(pyRoot, "cross_file_calls", "cross_file_calls.expected.json")
	if _, statErr := os.Stat(pyCrossFile); statErr != nil {
		t.Fatalf("python cross_file_calls fixture missing: %v", statErr)
	}
	fixtures = append(fixtures, pyCrossFile)

	// ---- TypeScript fixtures ----
	tsRoot := filepath.Join(root, "testdata", "fixtures", "typescript")

	tsTopLevel, tsGlobErr := filepath.Glob(filepath.Join(tsRoot, "*.expected.json"))
	if tsGlobErr != nil {
		t.Fatalf("glob typescript top-level fixtures: %v", tsGlobErr)
	}
	if len(tsTopLevel) == 0 {
		t.Fatal("no top-level *.expected.json fixtures found — check testdata/fixtures/typescript/")
	}
	fixtures = append(fixtures, tsTopLevel...)

	tsCrossFile := filepath.Join(tsRoot, "cross_file", "cross_file.expected.json")
	if _, statErr := os.Stat(tsCrossFile); statErr != nil {
		t.Fatalf("typescript cross_file fixture missing: %v", statErr)
	}
	fixtures = append(fixtures, tsCrossFile)

	return fixtures
}

// loadFixtureDoc loads a fixture file and returns the raw map and the path-relative
// name used for test naming (just the base name without parent dirs).
func loadFixtureDoc(t *testing.T, fixturePath string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse fixture JSON %s: %v", fixturePath, err)
	}
	return doc
}

// fixtureName returns a subtest-safe name for a fixture path.
//
// The naming scheme is "{language}/{optional-subdir/}{filename}", e.g.:
//   - "python/simple_module.expected.json"
//   - "python/cross_file_calls/cross_file_calls.expected.json"
//   - "typescript/class_methods.expected.json"
//   - "typescript/cross_file/cross_file.expected.json"
//
// This keeps failure messages unambiguous when the same fixture base name
// exists in both language directories.
func fixtureName(fixturePath string) string {
	slash := filepath.ToSlash(fixturePath)
	parts := strings.Split(slash, "/")
	n := len(parts)
	// Walk up from the file name to find the language anchor ("python" or "typescript").
	// parts[n-1] = filename
	// parts[n-2] = immediate parent (either the language dir or a sub-dir)
	// parts[n-3] = grandparent (either the language dir or "fixtures")
	for i := n - 2; i >= 0; i-- {
		if parts[i] == "python" || parts[i] == "typescript" {
			// Everything from the language dir onward is the meaningful name.
			return strings.Join(parts[i:], "/")
		}
	}
	// Fallback: just the filename.
	return parts[n-1]
}

// ---------------------------------------------------------------------------
// Property: unique qualified_name within payload
// ---------------------------------------------------------------------------

// TestProperty_UniqueQualifiedName asserts that every qualified_name in the
// Symbols array of a fixture document appears at most once.
//
// Why this matters: qualified_name is the stable ID used by edges and
// downstream consumer schemas that index symbols by qualified name.
// A duplicate breaks deduplication, upsert semantics, and graph traversal.
func TestProperty_UniqueQualifiedName(t *testing.T) {
	for _, fixturePath := range allFixtures(t) {
		t.Run(fixtureName(fixturePath), func(t *testing.T) {
			doc := loadFixtureDoc(t, fixturePath)

			var symbols []struct {
				QualifiedName string `json:"qualified_name"`
			}
			if err := json.Unmarshal(doc["symbols"], &symbols); err != nil {
				t.Fatalf("unmarshal symbols: %v", err)
			}

			seen := make(map[string]int, len(symbols))
			for i, s := range symbols {
				if s.QualifiedName == "" {
					t.Errorf("symbol[%d] has empty qualified_name", i)
					continue
				}
				if prev, ok := seen[s.QualifiedName]; ok {
					t.Errorf("duplicate qualified_name %q at indices %d and %d", s.QualifiedName, prev, i)
				}
				seen[s.QualifiedName] = i
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Property: sorted arrays
// ---------------------------------------------------------------------------

// TestProperty_SortedArrays asserts that:
//   - Symbols are sorted by qualified_name (ascending)
//   - Edges are sorted by (source_qualified_name, target_qualified_name, edge_type)
//
// Deterministic ordering is a schema v1 guarantee relied upon by consumers for
// diff stability and deduplication. This property is also asserted per-parser-run in
// internal/parser/python/parser_test.go; the schema package is the canonical location
// because the sort guarantee is a property of the *output contract*, not of any one parser.
func TestProperty_SortedArrays(t *testing.T) {
	for _, fixturePath := range allFixtures(t) {
		t.Run(fixtureName(fixturePath), func(t *testing.T) {
			doc := loadFixtureDoc(t, fixturePath)

			// Symbols sorted by qualified_name.
			var symbols []struct {
				QualifiedName string `json:"qualified_name"`
			}
			if err := json.Unmarshal(doc["symbols"], &symbols); err != nil {
				t.Fatalf("unmarshal symbols: %v", err)
			}
			for i := 1; i < len(symbols); i++ {
				prev := symbols[i-1].QualifiedName
				curr := symbols[i].QualifiedName
				if prev > curr {
					t.Errorf("symbols not sorted at index %d: %q > %q", i, prev, curr)
				}
			}

			// Edges sorted by (source, target, edge_type).
			var edges []struct {
				Source   string `json:"source_qualified_name"`
				Target   string `json:"target_qualified_name"`
				EdgeType string `json:"edge_type"`
			}
			if err := json.Unmarshal(doc["edges"], &edges); err != nil {
				t.Fatalf("unmarshal edges: %v", err)
			}
			for i := 1; i < len(edges); i++ {
				prev := edgeTuple(edges[i-1].Source, edges[i-1].Target, edges[i-1].EdgeType)
				curr := edgeTuple(edges[i].Source, edges[i].Target, edges[i].EdgeType)
				if prev > curr {
					t.Errorf("edges not sorted at index %d: %q > %q", i, prev, curr)
				}
			}
		})
	}
}

// edgeTuple computes a sortable tuple string for an edge.
func edgeTuple(source, target, edgeType string) string {
	return source + "|" + target + "|" + edgeType
}

// ---------------------------------------------------------------------------
// Property: every edge references a known symbol
// ---------------------------------------------------------------------------

// TestProperty_EdgesReferenceKnownSymbols asserts that every edge's
// source_qualified_name and target_qualified_name is present in the Symbols array
// of the same document.
//
// Why this matters: the contract documentation for Edge states:
// "Must reference a symbol present in the same Document's Symbols array."
// A dangling edge reference would break graph rendering, plugin delta tracking,
// and any consumer that builds an adjacency list from the document.
func TestProperty_EdgesReferenceKnownSymbols(t *testing.T) {
	for _, fixturePath := range allFixtures(t) {
		t.Run(fixtureName(fixturePath), func(t *testing.T) {
			doc := loadFixtureDoc(t, fixturePath)

			// Build a set of known qualified names from Symbols.
			var symbols []struct {
				QualifiedName string `json:"qualified_name"`
			}
			if err := json.Unmarshal(doc["symbols"], &symbols); err != nil {
				t.Fatalf("unmarshal symbols: %v", err)
			}
			known := make(map[string]bool, len(symbols))
			for _, s := range symbols {
				known[s.QualifiedName] = true
			}

			// Assert every edge's endpoints are in the known set.
			var edges []struct {
				Source   string `json:"source_qualified_name"`
				Target   string `json:"target_qualified_name"`
				EdgeType string `json:"edge_type"`
			}
			if err := json.Unmarshal(doc["edges"], &edges); err != nil {
				t.Fatalf("unmarshal edges: %v", err)
			}
			for i, e := range edges {
				if !known[e.Source] {
					t.Errorf("edge[%d] source_qualified_name %q not found in symbols", i, e.Source)
				}
				if !known[e.Target] {
					t.Errorf("edge[%d] target_qualified_name %q not found in symbols", i, e.Target)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Property: output validates against the published JSON Schema
// ---------------------------------------------------------------------------

// TestProperty_OutputValidatesAgainstSchema compiles schema/codeweaver-v1.json
// with a real JSON Schema 2020-12 validator (github.com/santhosh-tekuri/jsonschema/v6)
// and runs each committed fixture through it.
//
// This is a genuine schema-validation test — not the partial check in types_test.go
// (TestDocumentValidatesAgainstSchema) which only verifies the schema file is valid
// JSON with the correct $schema URI. This test actually validates documents against
// the compiled schema.
//
// # Why the fixtures need envelope enrichment
//
// The committed *.expected.json fixtures capture the *data payload* (symbols, edges,
// file_count, schema_version, deleted_files) generated by the binary. The schema
// also requires "parser_version" and "parsed_at" which are runtime-injected envelope
// fields not present in the stored fixtures (they'd change on every build/run).
//
// To validate the fixture shape against the schema, this test enriches each fixture
// with placeholder values for the two runtime-only fields before validating. This
// accurately simulates what a real document looks like after the binary runs.
func TestProperty_OutputValidatesAgainstSchema(t *testing.T) {
	spath := schemaPath(t)
	if _, err := os.Stat(spath); os.IsNotExist(err) {
		t.Skipf("schema/codeweaver-v1.json not found (run 'go generate ./...' first): %v", err)
	}

	// Compile the schema once; reuse for all fixtures.
	// NewCompiler() supports JSON Schema 2020-12 natively.
	c := jsonschema.NewCompiler()
	schema, err := c.Compile(spath)
	if err != nil {
		t.Fatalf("compile schema %s: %v", spath, err)
	}

	for _, fixturePath := range allFixtures(t) {
		t.Run(fixtureName(fixturePath), func(t *testing.T) {
			data, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			// Enrich the fixture with runtime-envelope placeholders for the two
			// required fields that the binary injects at runtime but that are not
			// captured in stored fixtures (they change every run).
			enriched, err := enrichFixture(data)
			if err != nil {
				t.Fatalf("enrich fixture: %v", err)
			}

			// jsonschema.UnmarshalJSON parses the document into an any-typed value
			// that the validator can inspect. Think of it like loading a witness
			// into the courtroom: the validator examines the witness (the parsed
			// JSON value) against the schema (the rules).
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(enriched))
			if err != nil {
				t.Fatalf("unmarshal fixture for schema validation: %v", err)
			}

			if err := schema.Validate(inst); err != nil {
				t.Errorf("schema validation FAIL:\n%v", err)
			}
		})
	}
}

// enrichFixture takes a fixture's raw JSON bytes and adds placeholder values for
// the two runtime-only required fields ("parser_version", "parsed_at") if they
// are absent. This allows schema validation of data-only fixtures without altering
// the committed fixture files.
func enrichFixture(data []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if _, ok := doc["parser_version"]; !ok {
		doc["parser_version"] = json.RawMessage(`"0.0.0-test"`)
	}
	if _, ok := doc["parsed_at"]; !ok {
		doc["parsed_at"] = json.RawMessage(`"2026-01-01T00:00:00Z"`)
	}

	return json.Marshal(doc)
}
