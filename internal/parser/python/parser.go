// Package python implements the Python source file parser for the codeweaver binary.
//
// # Architecture: Three-Pass Algorithm
//
// The parser uses a three-pass structure, which is how cross-file call edges become
// possible without a full type checker.
//
// Think of it like alphabetizing a multi-volume encyclopedia:
//   - Pass 1 (per file): "Number every entry in this volume." Each file gets its full
//     symbol list (MODULE + functions + classes + methods) and a local-name-to-qualified-name
//     index. Like writing page numbers before you merge volumes.
//   - Pass 2 (per file): "What does this volume reference?" Collect call expressions and
//     import statements. For each call, determine the enclosing function (context) and
//     tentatively resolve the callee using the local index built in Pass 1.
//   - Pass 3 (cross-file, in resolve.go): "Stitch the volumes together." After all files
//     are parsed, use the import maps from Pass 2 to link calls in File A to definitions
//     in File B. Only emit an edge if both endpoints are known symbols in the payload.
//
// # Shared Types
//
// This package does NOT define FileResult or RawCall. Those types live in the parent
// package (internal/parser/types.go) so that the cross-file resolution layer (resolve.go)
// can consume them without importing this package — breaking the layering dependency.
//
// This package imports internal/parser for the shared types and produces *parser.FileResult
// from its public ParseFile function. The resolve layer consumes []*parser.FileResult
// and calls the language-agnostic ResolveCallsForFile from internal/parser/types.go.
//
// # Lazy Grammar Init
//
// The Python grammar (via grammars.PythonLanguage()) is initialized on the first call
// to ParseFile, not at process startup. When the binary is invoked to parse a single
// Python file in a hot file-edit loop, this avoids loading all grammars upfront.
// Measurement: on a MacBook Pro M3, grammar init takes ~3ms (first call only).
// Subsequent parses in the same invocation reuse the cached *Language pointer.
//
// # Extraction Substrate: Hand-Walking the AST
//
// The parser uses hand-walking via gotreesitter.Walk and node.ChildByFieldName.
// This was chosen over typed-query codegen (tsquery .scm files) for codeweaver v1 because it
// allows the snapshot parity gate to validate correctness directly against committed
// *.expected.json fixture baselines.
//
// See IMPLEMENTATION_NOTES.md at the repository root for the full rationale,
// including the tsquery MatchPattern naming-collision issue and the criteria for
// migrating to typed queries if a fitness function shows hand-walking is no longer adequate.
//
// # Error Handling
//
// Tree-sitter always produces a parse tree, even for syntactically broken files — it
// inserts MISSING/ERROR nodes where the grammar doesn't match. The binary:
//  1. Checks tree.RootNode().HasError() after parsing.
//  2. If true, emits a ParseError with code E_PARSE_INCOMPLETE.
//  3. Still extracts whatever symbols were found before (or around) the error.
//  4. Exits with code 0 (partial result, not fatal).
//
// This means a half-typed file in an editor doesn't silence the entire module.
package python

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"codeweaver/internal/parser"
	"codeweaver/internal/schema"
)

// ---------------------------------------------------------------------------
// Lazy grammar initialization
// ---------------------------------------------------------------------------

// pythonGrammarState holds the lazily-initialized Python grammar.
// The zero value is valid; once.Do initializes lang on first use.
type pythonGrammarState struct {
	once sync.Once // must be first field; zero value is valid
	lang *gotreesitter.Language
	err  error
}

var pythonGrammar pythonGrammarState

// pythonLanguage returns the lazily-initialized Python grammar.
// Thread-safe: multiple goroutines may call this concurrently.
// The grammar is loaded once per process; subsequent calls return the cached value.
//
// Returns a *parser.GrammarLoadError when the grammar fails to initialize.
// This allows cli/parse.go to emit E_GRAMMAR_LOAD_FAILED instead of the generic
// E_PARSE_INCOMPLETE, signaling an environment-level failure to consumers.
func pythonLanguage() (*gotreesitter.Language, error) {
	pythonGrammar.once.Do(func() {
		pythonGrammar.lang = grammars.PythonLanguage()
		if pythonGrammar.lang == nil {
			pythonGrammar.err = &parser.GrammarLoadError{
				Language: "python",
				Cause:    fmt.Errorf("grammars.PythonLanguage() returned nil"),
			}
		}
	})
	return pythonGrammar.lang, pythonGrammar.err
}

// ---------------------------------------------------------------------------
// qualify() — stable ID derivation
// ---------------------------------------------------------------------------

// qualify builds a qualified_name in the format "{rel_path}::{symbol_name}".
// This is the stable ID used for all symbols and edge endpoints in the output schema.
func qualify(relPath, name string) string {
	return relPath + "::" + name
}

// computePythonModuleName derives the importable dotted module name from a
// workspace-relative Python file path.
//
// Transformation rules:
//   - Strip the ".py" suffix.
//   - Replace all "/" separators with "." (dotted package notation).
//
// Examples:
//
//	"utils.py"           → "utils"
//	"pkg/sub/mod.py"     → "pkg.sub.mod"
//	"cross_file_calls/utils.py" → "cross_file_calls.utils"
//
// This matches the dotted name used in "from pkg.sub.mod import symbol"
// Python import statements, so ModuleNameMap["cross_file_calls.utils"] = "cross_file_calls/utils.py"
// allows the resolver to bridge the import-map key to the file path.
func computePythonModuleName(relPath string) string {
	// Strip ".py" suffix (case-sensitive: Python files are always lowercase .py).
	name := strings.TrimSuffix(relPath, ".py")
	// Replace "/" with "." to produce the dotted package path.
	return strings.ReplaceAll(name, "/", ".")
}

// ---------------------------------------------------------------------------
// Pass 1: symbol extraction
// ---------------------------------------------------------------------------

// parseSymbols extracts all symbols from the parse tree (Pass 1).
//
// This function uses an explicit stack rather than recursion to avoid goroutine stack
// overflows on pathologically deeply-nested source (e.g., 5,000 levels of nested
// functions or classes). Go's default goroutine stack starts at 8 KB and grows
// dynamically, but deeply-nested mutual recursion can still exhaust memory or hit
// the runtime's limit.
//
// The DFS pre-order traversal is preserved exactly: a node is processed before its
// children, and children are pushed in reverse order so the leftmost child is popped
// first. This produces byte-identical output to the previous recursive version.
func parseSymbols(root *gotreesitter.Node, lang *gotreesitter.Language, source []byte, relPath string) (
	symbols []schema.Symbol,
	localNameIndex map[string]string,
	bareMethodIndex map[string]map[string]string,
) {
	moduleQName := qualify(relPath, "MODULE")
	symbols = []schema.Symbol{
		{
			QualifiedName: moduleQName,
			Name:          "MODULE",
			FilePath:      relPath,
			SymbolType:    "module",
			Language:      "python",
		},
	}

	localNameIndex = map[string]string{
		"MODULE": moduleQName,
	}
	bareMethodIndex = map[string]map[string]string{}

	// stackFrame carries a node and the active class context at that node.
	// The active class changes when we descend into a class_definition body.
	type stackFrame struct {
		node        *gotreesitter.Node
		activeClass string
	}

	// Start DFS with the root node and no enclosing class.
	stack := []stackFrame{{node: root, activeClass: ""}}

	for len(stack) > 0 {
		// Pop the top frame (DFS: process this node before its children).
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		node := frame.node
		activeClass := frame.activeClass

		if node == nil {
			continue
		}
		ntype := node.Type(lang)

		switch ntype {
		case "function_definition":
			nameNode := node.ChildByFieldName("name", lang)
			if nameNode != nil {
				name := nameNode.Text(source)
				if name != "" {
					var fullName string
					if activeClass != "" {
						fullName = activeClass + "." + name
					} else {
						fullName = name
					}
					qname := qualify(relPath, fullName)
					ls := int(node.StartPoint().Row) + 1
					le := int(node.EndPoint().Row) + 1
					symbols = append(symbols, schema.Symbol{
						QualifiedName: qname,
						Name:          fullName,
						FilePath:      relPath,
						SymbolType:    "function",
						Language:      "python",
						LineStart:     &ls,
						LineEnd:       &le,
					})
					localNameIndex[fullName] = qname
					if activeClass != "" {
						if bareMethodIndex[activeClass] == nil {
							bareMethodIndex[activeClass] = map[string]string{}
						}
						bareMethodIndex[activeClass][name] = qname
					}
				}
			}
			// Fall through to default child-push below (recurse into function body).

		case "class_definition":
			nameNode := node.ChildByFieldName("name", lang)
			if nameNode != nil {
				name := nameNode.Text(source)
				if name != "" {
					qname := qualify(relPath, name)
					ls := int(node.StartPoint().Row) + 1
					le := int(node.EndPoint().Row) + 1
					symbols = append(symbols, schema.Symbol{
						QualifiedName: qname,
						Name:          name,
						FilePath:      relPath,
						SymbolType:    "class",
						Language:      "python",
						LineStart:     &ls,
						LineEnd:       &le,
					})
					localNameIndex[name] = qname
					// Push children with updated activeClass (the class we just found).
					// Children are pushed in reverse order so leftmost is popped first
					// (preserving DFS left-to-right pre-order — identical to recursive).
					children := node.Children()
					for j := len(children) - 1; j >= 0; j-- {
						stack = append(stack, stackFrame{node: children[j], activeClass: name})
					}
					continue // skip the default child-push below
				}
			}
			// If nameNode was nil, fall through to default child-push.
		}

		// Default: push all children with the same activeClass.
		// Reverse order preserves DFS left-to-right pre-order.
		children := node.Children()
		for j := len(children) - 1; j >= 0; j-- {
			stack = append(stack, stackFrame{node: children[j], activeClass: activeClass})
		}
	}

	return symbols, localNameIndex, bareMethodIndex
}

// ---------------------------------------------------------------------------
// Pass 2: call and import extraction
// ---------------------------------------------------------------------------

// deriveEnclosingClassName walks the ancestor chain looking for a class_definition node.
// Returns the class name string or "" if not inside a class.
func deriveEnclosingClassName(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	ancestor := node.Parent()
	for ancestor != nil {
		if ancestor.Type(lang) == "class_definition" {
			nameNode := ancestor.ChildByFieldName("name", lang)
			if nameNode != nil {
				return nameNode.Text(source)
			}
			return ""
		}
		ancestor = ancestor.Parent()
	}
	return ""
}

// deriveEnclosingFunctionQName walks the ancestor chain to find the enclosing
// function_definition and returns its qualified name.
// Returns the MODULE qualified name if the call is at module level.
func deriveEnclosingFunctionQName(
	node *gotreesitter.Node,
	lang *gotreesitter.Language,
	source []byte,
	relPath string,
	localNameIndex map[string]string,
) string {
	ancestor := node.Parent()
	for ancestor != nil {
		if ancestor.Type(lang) == "function_definition" {
			nameNode := ancestor.ChildByFieldName("name", lang)
			if nameNode != nil {
				name := nameNode.Text(source)
				cls := deriveEnclosingClassName(ancestor, lang, source)
				var fullName string
				if cls != "" {
					fullName = cls + "." + name
				} else {
					fullName = name
				}
				if qname, ok := localNameIndex[fullName]; ok {
					return qname
				}
				return qualify(relPath, fullName)
			}
		}
		ancestor = ancestor.Parent()
	}
	return qualify(relPath, "MODULE")
}

// parseCalls walks the parse tree and collects raw (unresolved) call edges (Pass 2).
//
// ctx is checked every 100 nodes to support deadline cancellation mid-walk.
// gotreesitter.Walk is callback-based and not context-aware natively, so we use
// a per-iteration counter and return WalkStop when the deadline fires — the same
// mechanism used by findFirstErrorByte for early-exit on the first error node.
// Think of it like a postal worker sorting letters: they check the clock every 100
// envelopes rather than after every single one, which keeps the check overhead tiny
// while still honoring the deadline within a small constant multiple.
//
// Returns partial rawCalls collected up to the point of cancellation.
func parseCalls(
	ctx context.Context,
	root *gotreesitter.Node,
	lang *gotreesitter.Language,
	source []byte,
	relPath string,
	localNameIndex map[string]string,
) []parser.RawCall {
	var rawCalls []parser.RawCall
	var nodeCount int

	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		// Throttled context check: sample every 100 nodes to keep per-node overhead
		// near zero while still honoring deadlines on large files.
		nodeCount++
		if nodeCount%100 == 0 {
			if ctx.Err() != nil {
				return gotreesitter.WalkStop
			}
		}

		if node == nil {
			return gotreesitter.WalkContinue
		}
		if node.Type(lang) != "call" {
			return gotreesitter.WalkContinue
		}

		fnNode := node.ChildByFieldName("function", lang)
		if fnNode == nil {
			return gotreesitter.WalkContinue
		}

		enclosingFn := deriveEnclosingFunctionQName(node, lang, source, relPath, localNameIndex)
		enclosingClass := deriveEnclosingClassName(node, lang, source)

		switch fnNode.Type(lang) {
		case "identifier":
			callee := fnNode.Text(source)
			if callee != "" {
				rawCalls = append(rawCalls, parser.RawCall{
					SourceQualifiedName: enclosingFn,
					CalleeName:          callee,
					EnclosingClassName:  enclosingClass,
				})
			}

		case "attribute":
			attrNode := fnNode.ChildByFieldName("attribute", lang)
			objNode := fnNode.ChildByFieldName("object", lang)
			if attrNode != nil {
				methodName := attrNode.Text(source)
				var receiverText string
				if objNode != nil {
					receiverText = objNode.Text(source)
				}
				if methodName != "" {
					rawCalls = append(rawCalls, parser.RawCall{
						SourceQualifiedName: enclosingFn,
						CalleeName:          methodName,
						ReceiverText:        receiverText,
						EnclosingClassName:  enclosingClass,
					})
				}
			}
		}

		return gotreesitter.WalkContinue
	})

	return rawCalls
}

// sanitizeImportPath strips control characters (code points < 0x20) from an import
// path specifier string, except for \t, \n, and \r which can legitimately appear
// in source text around import statements after lexer normalization.
//
// NUL bytes (\x00) are silently stripped — they almost always indicate malformed
// source already classified by tree-sitter via ERROR/MISSING nodes (E_PARSE_INCOMPLETE).
// We strip rather than reject to avoid corrupting the module map with NUL-keyed entries,
// which would cause subtle lookup failures in the cross-file resolution layer.
//
// Real-life analogy: if a librarian receives a catalog card where someone has typed
// a null character in the middle of the call number, they strip the garbage character
// rather than placing the card in an invisible section that no one can search.
func sanitizeImportPath(s string) string {
	clean := strings.Map(func(r rune) rune {
		// Keep tab, newline, carriage return (they can appear in source context).
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		// Strip all other control characters, including NUL (0x00).
		if r < 0x20 {
			return -1 // returning -1 from strings.Map drops the rune
		}
		return r
	}, s)
	return clean
}

// parseImportMap builds a named import map for a Python file (Pass 2, import side).
//
// Handles:
//   - from X import Y      -> maps "Y" -> "X::Y"
//   - from X import Y as Z -> maps "Z" -> "X::Y"
func parseImportMap(root *gotreesitter.Node, lang *gotreesitter.Language, source []byte) map[string]string {
	importMap := map[string]string{}

	for _, node := range root.Children() {
		if node == nil {
			continue
		}
		if node.Type(lang) != "import_from_statement" {
			continue
		}

		moduleNode := node.ChildByFieldName("module_name", lang)
		if moduleNode == nil {
			continue
		}
		// Sanitize the module name: strip NUL bytes and other control characters.
		// Malformed source with embedded NUL bytes in import paths is already flagged
		// as E_PARSE_INCOMPLETE by tree-sitter; we sanitize here to avoid corrupting
		// the module map with NUL-keyed entries.
		moduleName := sanitizeImportPath(moduleNode.Text(source))
		if moduleName == "" {
			continue
		}

		for _, child := range node.Children() {
			if child == nil || child == moduleNode {
				continue
			}
			ctype := child.Type(lang)

			if ctype == "dotted_name" {
				name := sanitizeImportPath(child.Text(source))
				if name != "" {
					importMap[name] = moduleName + "::" + name
				}
			} else if ctype == "aliased_import" {
				nameNode := child.ChildByFieldName("name", lang)
				aliasNode := child.ChildByFieldName("alias", lang)
				if nameNode != nil {
					original := sanitizeImportPath(nameNode.Text(source))
					var local string
					if aliasNode != nil {
						local = sanitizeImportPath(aliasNode.Text(source))
					} else {
						local = original
					}
					if original != "" && local != "" {
						importMap[local] = moduleName + "::" + original
					}
				}
			}
		}
	}

	return importMap
}

// ---------------------------------------------------------------------------
// Error detection
// ---------------------------------------------------------------------------

// findFirstErrorByte walks the tree looking for the first ERROR or MISSING node
// and returns its byte offset. Returns nil if no error nodes are found.
func findFirstErrorByte(root *gotreesitter.Node) *int {
	var found *int
	gotreesitter.Walk(root, func(node *gotreesitter.Node, depth int) gotreesitter.WalkAction {
		if node == nil {
			return gotreesitter.WalkContinue
		}
		if node.IsError() || node.IsMissing() {
			off := int(node.StartByte())
			found = &off
			return gotreesitter.WalkStop
		}
		return gotreesitter.WalkContinue
	})
	return found
}

// ---------------------------------------------------------------------------
// ParseFile — public entry point
// ---------------------------------------------------------------------------

// ParseFile parses a single Python source file and returns a *parser.FileResult.
//
// ctx is a context.Context that may carry a deadline or cancellation signal.
// By Go convention, context.Context is always the first argument. If ctx is
// cancelled or its deadline expires during parsing, ParseFile returns early
// with whatever symbols were extracted so far and a parse_errors entry.
//
// The relPath must be workspace-relative with forward slashes.
// source is the raw source bytes of the file.
//
// The returned *parser.FileResult uses the shared type from internal/parser/types.go,
// This allows the resolve layer (resolve.go) to accept results from multiple language
// parsers without importing any language package.
func ParseFile(ctx context.Context, source []byte, relPath string) (*parser.FileResult, error) {
	// Check for cancellation or deadline before doing any work.
	// This catches the case where the context was cancelled before we even started.
	if err := ctx.Err(); err != nil {
		return &parser.FileResult{
			RelPath:     relPath,
			Symbols:     []schema.Symbol{},
			GrammarName: "python",
			ModuleName:  computePythonModuleName(relPath),
			HasError:    true,
		}, fmt.Errorf("context cancelled before parse started for %s: %w", relPath, err)
	}

	lang, err := pythonLanguage()
	if err != nil {
		// Grammar load failure: return the GrammarLoadError directly so cli/parse.go
		// can emit E_GRAMMAR_LOAD_FAILED instead of E_PARSE_INCOMPLETE.
		return nil, err
	}

	p := gotreesitter.NewParser(lang)
	root, parseErr := parser.ParseSource(p, source, relPath)
	if parseErr != nil {
		return nil, parseErr
	}

	// Pass 1: extract symbols and build name indexes.
	// Check context between passes — tree-sitter's own Parse() call is not
	// context-aware, but the Go extraction walks are where CPU time is spent.
	symbols, localNameIndex, bareMethodIndex := parseSymbols(root, lang, source, relPath)
	// Sort symbols by qualified_name. This is the schema v1 output invariant;
	// consumers (tests, snapshot fixtures, the CLI) rely on deterministic ordering.
	parser.SortSymbols(symbols)

	// Context check between Pass 1 and Pass 2.
	// If the deadline expired during symbol extraction, return partial results.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &parser.FileResult{
			RelPath:         relPath,
			Symbols:         symbols,
			RawCalls:        nil,
			ImportMap:       map[string]string{},
			LocalNameIndex:  localNameIndex,
			BareMethodIndex: bareMethodIndex,
			HasError:        true,
			GrammarName:     "python",
			ModuleName:      computePythonModuleName(relPath),
		}, fmt.Errorf("context cancelled during pass 1 for %s: %w", relPath, ctxErr)
	}

	// Pass 2: extract calls and build import map.
	// ctx is propagated so parseCalls can respect deadline cancellation mid-walk.
	// If the deadline expires during Pass 2, parseCalls returns partial rawCalls
	// collected so far — the partial results are still incorporated into the FileResult.
	rawCalls := parseCalls(ctx, root, lang, source, relPath, localNameIndex)
	importMap := parseImportMap(root, lang, source)

	// Context check after Pass 2.
	// If the deadline expired during call extraction, return partial results.
	// The CLI layer re-classifies E_PARSE_INCOMPLETE to E_DEADLINE_EXCEEDED when
	// ctx.Err() == context.DeadlineExceeded (existing routing in cli/parse.go).
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &parser.FileResult{
			RelPath:         relPath,
			Symbols:         symbols,
			RawCalls:        rawCalls,
			ImportMap:       importMap,
			LocalNameIndex:  localNameIndex,
			BareMethodIndex: bareMethodIndex,
			HasError:        true,
			GrammarName:     "python",
			ModuleName:      computePythonModuleName(relPath),
		}, fmt.Errorf("context cancelled during pass 2 for %s: %w", relPath, ctxErr)
	}

	// Detect parse errors (ERROR/MISSING nodes from tree-sitter).
	var hasError bool
	var errorByteOffset *int
	if root.HasError() {
		hasError = true
		errorByteOffset = findFirstErrorByte(root)
	}

	return &parser.FileResult{
		RelPath:         relPath,
		Symbols:         symbols,
		RawCalls:        rawCalls,
		ImportMap:       importMap,
		LocalNameIndex:  localNameIndex,
		BareMethodIndex: bareMethodIndex,
		HasError:        hasError,
		ErrorByteOffset: errorByteOffset,
		GrammarName:     "python",
		ModuleName:      computePythonModuleName(relPath),
	}, nil
}

// GrammarVersion returns the version string for the Python grammar.
// Used by the --version --json output.
func GrammarVersion() string {
	return "gotreesitter-v0.15.3/python"
}
