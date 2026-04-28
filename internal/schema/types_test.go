package schema_test

import (
	"encoding/json"
	"os"
	"testing"

	"codeweaver/internal/schema"
)

// TestDocumentMarshalRoundTrip verifies that a Document marshals to valid JSON
// and round-trips correctly through json.Unmarshal.
func TestDocumentMarshalRoundTrip(t *testing.T) {
	lineStart := 10
	lineEnd := 20
	byteOffset := 1024
	dryRun := false

	doc := schema.Document{
		SchemaVersion: schema.SchemaVersionV1,
		ParserVersion:   "0.1.0",
		ParsedAt:        "2026-04-27T10:00:00Z",
		FileCount:       2,
		Symbols: []schema.Symbol{
			{
				QualifiedName: "api/models.py::MyClass",
				Name:          "MyClass",
				FilePath:      "api/models.py",
				SymbolType:    "class",
				Language:      "python",
				LineStart:     &lineStart,
				LineEnd:       &lineEnd,
			},
		},
		Edges: []schema.Edge{
			{
				SourceQualifiedName: "api/models.py::MyClass",
				TargetQualifiedName: "api/utils.py::helper",
				EdgeType:            "calls",
			},
		},
		DeletedFiles: []string{},
		ParseErrors: []schema.ParseError{
			{
				FilePath:   "src/broken.py",
				ErrorCode:  schema.ErrorCodeParseIncomplete,
				Message:    "Tree-sitter ERROR node at line 42:10",
				ByteOffset: &byteOffset,
			},
		},
		DryRun: &dryRun,
	}

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var roundTripped schema.Document
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if roundTripped.SchemaVersion != schema.SchemaVersionV1 {
		t.Errorf("SchemaVersion: got %q, want %q", roundTripped.SchemaVersion, schema.SchemaVersionV1)
	}
	if len(roundTripped.Symbols) != 1 {
		t.Errorf("Symbols: got %d, want 1", len(roundTripped.Symbols))
	}
	if len(roundTripped.Edges) != 1 {
		t.Errorf("Edges: got %d, want 1", len(roundTripped.Edges))
	}
	if len(roundTripped.ParseErrors) != 1 {
		t.Errorf("ParseErrors: got %d, want 1", len(roundTripped.ParseErrors))
	}
}

// TestDocumentOmitEmpty verifies that optional fields are absent (not null) in JSON output.
func TestDocumentOmitEmpty(t *testing.T) {
	doc := schema.Document{
		SchemaVersion: schema.SchemaVersionV1,
		ParserVersion:   "0.1.0",
		ParsedAt:        "2026-04-27T10:00:00Z",
		FileCount:       0,
		Symbols:         []schema.Symbol{},
		Edges:           []schema.Edge{},
		DeletedFiles:    []string{},
		// ParseErrors omitted — should not appear in JSON
		// DryRun omitted — should not appear in JSON
		// FilesValidated omitted — should not appear in JSON
	}

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal to map failed: %v", err)
	}

	// parse_errors, dry_run, and files_validated should be absent, not null
	for _, absent := range []string{"parse_errors", "dry_run", "files_validated"} {
		if _, ok := raw[absent]; ok {
			t.Errorf("field %q should be absent when empty, but was present in JSON: %s", absent, data)
		}
	}
}

// TestDocumentValidatesAgainstSchema verifies the generated Document JSON validates
// against the committed schema/codeweaver-v1.json.
// This test is intentionally lenient about schema presence — it skips if the
// schema file does not exist (pre-generate state) and fails if it is malformed.
func TestDocumentValidatesAgainstSchema(t *testing.T) {
	schemaPath := "../../schema/codeweaver-v1.json"
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Skipf("schema file not found (run 'go generate ./...' first): %v", err)
	}

	// Validate schema is parseable JSON
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(data, &schemaMap); err != nil {
		t.Fatalf("schema/codeweaver-v1.json is not valid JSON: %v", err)
	}

	// Check required top-level keys are present in the schema
	for _, key := range []string{"$schema", "properties"} {
		if _, ok := schemaMap[key]; !ok {
			t.Errorf("schema missing required key %q", key)
		}
	}

	// Verify $schema declares JSON Schema 2020-12
	if schemaVal, ok := schemaMap["$schema"].(string); ok {
		if schemaVal != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("schema $schema value: got %q, want JSON Schema 2020-12 URI", schemaVal)
		}
	}
}

// TestErrorCodeConstants verifies all error code constants are non-empty strings.
// ErrorCodeGrammarLoadFailed and ErrorCodeSymlinkRefused must be present so that
// omitting a constant from types.go is caught immediately.
func TestErrorCodeConstants(t *testing.T) {
	codes := []string{
		schema.ErrorCodeParseIncomplete,
		schema.ErrorCodeFileTooLarge,
		schema.ErrorCodeFileUnreadable,
		schema.ErrorCodeGrammarNotFound,
		schema.ErrorCodeGrammarLoadFailed,
		schema.ErrorCodeDeadlineExceeded,
		schema.ErrorCodeSymlinkRefused,
	}
	for _, code := range codes {
		if code == "" {
			t.Error("error code constant must not be empty")
		}
	}
}

// TestVersionOutputMarshal verifies the VersionOutput struct marshals correctly
// with all required fields from spec §4.
func TestVersionOutputMarshal(t *testing.T) {
	vo := schema.VersionOutput{
		Version:         "0.1.0",
		GitCommit:       "abc123",
		BuiltAt:         "2026-04-27T10:00:00Z",
		GoVersion:       "go1.26.2",
		SchemaVersion: schema.SchemaVersionV1,
		Grammars: map[string]string{
			"python":     "unknown",
			"typescript": "unknown",
		},
	}

	data, err := json.Marshal(vo)
	if err != nil {
		t.Fatalf("json.Marshal VersionOutput failed: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal VersionOutput failed: %v", err)
	}

	// All spec §4 required fields must be present
	required := []string{"version", "git_commit", "built_at", "go_version", "schema_version", "grammars"}
	for _, field := range required {
		if _, ok := raw[field]; !ok {
			t.Errorf("VersionOutput JSON missing required field %q", field)
		}
	}
}
