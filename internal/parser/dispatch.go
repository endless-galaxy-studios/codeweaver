// Package parser provides the language dispatch layer and per-language parse pipelines.
package parser

import (
	"path/filepath"
	"strings"
)

// Language represents a supported source language.
type Language string

const (
	// LanguagePython is the Python language identifier used in JSON output.
	LanguagePython Language = "python"
	// LanguageTypeScript is the TypeScript language identifier used in JSON output.
	// This covers .ts, .tsx, and .mts files.
	LanguageTypeScript Language = "typescript"
)

// DetectLanguage returns the Language for a given file path based on its extension.
// Returns ("", false) if the extension is not supported.
//
// Extension mapping (verified against gotreesitter v0.15.3 grammars.DetectLanguage):
//
//	.py   -> Python
//	.ts   -> TypeScript (grammars.DetectLanguage returns LangEntry{Name:"typescript"})
//	.tsx  -> TypeScript (grammars.DetectLanguage returns LangEntry{Name:"tsx"})
//	.mts  -> TypeScript (grammars.DetectLanguage returns LangEntry{Name:"typescript"})
//
// The gotreesitter registry handles .mts natively via linguist's extended extension table.
func DetectLanguage(filePath string) (Language, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".py":
		return LanguagePython, true
	case ".ts", ".tsx", ".mts":
		return LanguageTypeScript, true
	default:
		return "", false
	}
}

// SupportedExtensions returns the list of file extensions this binary can parse.
func SupportedExtensions() []string {
	return []string{".py", ".ts", ".tsx", ".mts"}
}

// IsSupportedExtension returns true if the given file path has a parseable extension.
func IsSupportedExtension(filePath string) bool {
	_, ok := DetectLanguage(filePath)
	return ok
}
