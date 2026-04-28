package parser_test

import (
	"testing"

	"codeweaver/internal/parser"
)

// TestDetectLanguage verifies all supported file extensions map to the correct language.
func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		file     string
		wantLang parser.Language
		wantOK   bool
	}{
		{"foo.py", parser.LanguagePython, true},
		{"path/to/module.py", parser.LanguagePython, true},
		{"foo.ts", parser.LanguageTypeScript, true},
		{"foo.tsx", parser.LanguageTypeScript, true},
		// .mts spike result: grammars.DetectLanguage("foo.mts") -> LangEntry{Name:"typescript"}
		{"foo.mts", parser.LanguageTypeScript, true},
		{"UPPERCASE.TS", parser.LanguageTypeScript, true}, // case-insensitive
		{"foo.go", "", false},
		{"foo.js", "", false},
		{"foo.rb", "", false},
		{"no_extension", "", false},
		{"", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			lang, ok := parser.DetectLanguage(tc.file)
			if ok != tc.wantOK {
				t.Errorf("DetectLanguage(%q): ok=%v, want %v", tc.file, ok, tc.wantOK)
			}
			if lang != tc.wantLang {
				t.Errorf("DetectLanguage(%q): lang=%q, want %q", tc.file, lang, tc.wantLang)
			}
		})
	}
}

// TestSupportedExtensions verifies the supported extension list is non-empty and contains
// the four documented extensions.
func TestSupportedExtensions(t *testing.T) {
	exts := parser.SupportedExtensions()
	if len(exts) == 0 {
		t.Fatal("SupportedExtensions() returned empty list")
	}

	required := []string{".py", ".ts", ".tsx", ".mts"}
	extMap := make(map[string]bool, len(exts))
	for _, e := range exts {
		extMap[e] = true
	}
	for _, e := range required {
		if !extMap[e] {
			t.Errorf("SupportedExtensions() missing required extension %q", e)
		}
	}
}

// TestIsSupportedExtension verifies the helper function correctly identifies supported files.
func TestIsSupportedExtension(t *testing.T) {
	cases := []struct {
		file string
		want bool
	}{
		{"main.py", true},
		{"main.ts", true},
		{"main.tsx", true},
		{"main.mts", true},
		{"main.go", false},
		{"main.js", false},
		{"README.md", false},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got := parser.IsSupportedExtension(tc.file)
			if got != tc.want {
				t.Errorf("IsSupportedExtension(%q)=%v, want %v", tc.file, got, tc.want)
			}
		})
	}
}
