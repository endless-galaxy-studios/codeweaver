// Command schema-gen generates the JSON Schema 2020-12 document for the codeweaver v1
// output schema from Go struct reflection. It is invoked by "go generate ./..."
// via the directive in internal/schema/schema_gen.go.
//
// Usage (via go generate):
//
//	go run ../cmd/schema-gen/main.go
//
// Output: schema/codeweaver-v1.json (relative to codeweaver-go/ module root)
//
// The generator reflects on schema.Document (the top-level JSON type) and emits
// a JSON Schema 2020-12 document. Struct field json: tags drive property names.
// jsonschema: tags provide description annotations.
//
// Run "go generate ./..." from the codeweaver-go/ module root to regenerate.
// The output is checked into git. Treat a diff in schema/codeweaver-v1.json
// as a contract change that requires review.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/invopop/jsonschema"

	"codeweaver/internal/schema"
)

func main() {
	// Locate the schema output directory relative to this generator's source file.
	// When invoked via "go run ./cmd/schema-gen/main.go" from the module root,
	// __FILE__ is the source path. We use runtime.Caller to get the source path
	// and navigate to the module root's schema/ directory.
	//
	// The output path is relative to where "go generate" is invoked (module root).
	// We use a fixed relative path: ../../schema/codeweaver-v1.json from this file's
	// source location, which resolves to codeweaver-go/schema/codeweaver-v1.json.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		// Fallback: write to schema/codeweaver-v1.json relative to CWD.
		// This is the expected path when invoked via "go run" from the module root.
		thisFile = "cmd/schema-gen/main.go"
	}

	// Navigate from cmd/schema-gen/ up to the module root, then into schema/.
	moduleRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	outputPath := filepath.Join(moduleRoot, "schema", "codeweaver-v1.json")

	// Ensure schema/ directory exists.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "schema-gen: mkdir %s: %v\n", filepath.Dir(outputPath), err)
		os.Exit(1)
	}

	r := &jsonschema.Reflector{
		// Anonymous: false — include the type's package path as $id base.
		// We override the BaseSchemaID to a stable URL.
		Anonymous: false,
		// ExpandedStruct: include the root type inline rather than as a $ref.
		ExpandedStruct: true,
		// AllowAdditionalProperties: true — consumers must ignore unknown fields
		// per the forward-compatibility principle.
		AllowAdditionalProperties: true,
		// DoNotReference: false — use $defs for referenced types.
		DoNotReference: false,
	}

	// Add Go comments as description annotations.
	// The CommentMap provides fallback descriptions when struct tags don't have jsonschema:"description=...".
	r.CommentMap = buildCommentMap()

	// Reflect on the Document type (top-level output schema type).
	schema := r.Reflect(&schema.Document{})

	// Override $schema to declare JSON Schema 2020-12 explicitly.
	// invopop/jsonschema defaults to 2020-12 via its Version constant.
	// We set it explicitly for clarity and to make the schema self-describing.
	// The field is Schema.Version (marshaled as "$schema" in JSON).
	schema.Version = jsonschema.Version

	// Set a stable $id for the schema.
	schema.ID = "https://raw.githubusercontent.com/endless-galaxy-studios/codeweaver/main/schema/codeweaver-v1.json"

	// Marshal with indentation for human readability.
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "schema-gen: marshal schema: %v\n", err)
		os.Exit(1)
	}
	// Append trailing newline for clean diffs.
	data = append(data, '\n')

	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "schema-gen: write %s: %v\n", outputPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "schema-gen: wrote %s\n", outputPath)
}

// buildCommentMap returns a map of fully-qualified Go type/field paths to description strings.
// These descriptions appear as "description" properties in the generated JSON Schema.
// Key format: "{package_path}.{TypeName}.{FieldName}" for fields, "{package_path}.{TypeName}" for types.
func buildCommentMap() map[string]string {
	pkg := "codeweaver/internal/schema"
	return map[string]string{
		pkg + ".Document":                "Top-level JSON document emitted by codeweaver parse. Consumers branch on schema_version for schema evolution.",
		pkg + ".Document.SchemaVersion":  "Output schema version. Always present. Current value: 1.0.",
		pkg + ".Document.ParserVersion":   "Semver of the codeweaver binary that produced this output.",
		pkg + ".Document.ParsedAt":        "ISO 8601 UTC timestamp of when the parse run started.",
		pkg + ".Document.FileCount":       "Number of files for which parse was attempted (total input). Equals files_parsed + files_errored.",
		pkg + ".Document.Symbols":         "Array of extracted code symbols, sorted by qualified_name.",
		pkg + ".Document.Edges":           "Array of relationships between symbols, sorted by (source, target, type). The 'inherits' edge_type is not emitted in schema v1.",
		pkg + ".Document.DeletedFiles":    "File paths deleted since last sync. Always [] in binary output; populated by the plugin from its SQLite state.",
		pkg + ".Document.ParseErrors":     "Per-file parse failures. Absent when all files parsed without errors.",
		pkg + ".Document.DryRun":          "Present and true only when --dry-run was passed.",
		pkg + ".Document.FilesValidated":  "Count of files validated in --dry-run mode.",
		pkg + ".Symbol":                   "A single code symbol extracted from a source file.",
		pkg + ".Symbol.QualifiedName":     "Stable unique ID: {workspace_relative_file_path}::{symbol_name}.",
		pkg + ".Symbol.Name":              "Short display name without file path prefix.",
		pkg + ".Symbol.FilePath":          "Workspace-relative file path with forward-slash separators on all platforms.",
		pkg + ".Symbol.SymbolType":        "Kind of code construct: function, class, or module.",
		pkg + ".Symbol.Language":          "Source language: python or typescript.",
		pkg + ".Symbol.LineStart":         "1-based start line. Absent for module symbols.",
		pkg + ".Symbol.LineEnd":           "1-based end line (inclusive). Absent for module symbols.",
		pkg + ".Edge":                     "A directed relationship between two symbols.",
		pkg + ".Edge.SourceQualifiedName": "QualifiedName of the calling or importing symbol.",
		pkg + ".Edge.TargetQualifiedName": "QualifiedName of the called or imported symbol.",
		pkg + ".Edge.EdgeType":            "Relationship type: calls or imports. The inherits type is not emitted in schema v1.",
		pkg + ".ParseError":               "A per-file parse failure or warning surfaced in the document rather than as a fatal exit.",
		pkg + ".ParseError.FilePath":      "Workspace-relative path of the file that failed.",
		pkg + ".ParseError.ErrorCode":     "Stable machine-readable error code. Consumers may branch on this value.",
		pkg + ".ParseError.Message":       "Human-readable error description. Not stable across binary versions.",
		pkg + ".ParseError.ByteOffset":    "Byte position of the first error node. Absent when not applicable.",
	}
}
