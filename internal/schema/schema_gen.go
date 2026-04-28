package schema

// Schema generation for the output schema v1.
//
// Running "go generate ./..." from the codeweaver-go module root invokes the
// schema-gen program (cmd/schema-gen/main.go) which reflects on the Document
// and VersionOutput types defined in this package and writes a JSON Schema
// 2020-12 document to schema/codeweaver-v1.json.
//
// The generated schema is checked into git. Treat schema drift as a breaking
// change — if "go generate ./..." produces a diff, the schema has changed.
//
// Generator: cmd/schema-gen/main.go
// Output:    schema/codeweaver-v1.json (relative to codeweaver-go/ module root)

//go:generate go run ../../cmd/schema-gen/main.go
