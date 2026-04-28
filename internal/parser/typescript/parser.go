// Package typescript implements the TypeScript source file parser for the codeweaver binary.
//
// # Architecture: Three-Pass Algorithm
//
// The parser uses the same three-pass structure as internal/parser/python/parser.go.
//
// Think of it like parsing a novel:
//   - Pass 1 (per file): "Catalogue every character." Each file gets its full
//     symbol list (MODULE + function declarations + class declarations + method
//     definitions + arrow functions as variable declarators) and a bare-method
//     index keyed by class name.
//   - Pass 2 (per file): "What did each character do?" Collect call expressions
//     and import statements, resolving this.method() calls via the bare-method
//     index and plain calls via the local name index.
//   - Pass 3 (cross-file, in resolve.go): "Stitch the chapters together." After
//     all files are parsed, use import maps to link calls in file A to definitions
//     in file B.
//
// # Grammar Variants: TypeScript vs TSX
//
// TypeScript has two grammar variants in gotreesitter v0.15.3:
//
//   - grammars.TypescriptLanguage() — standard TypeScript; handles .ts and .mts.
//   - grammars.TsxLanguage()        — TSX (TypeScript + JSX); handles .tsx only.
//
// JSX syntax requires different parse rules because angle brackets in JSX
// expressions (`<div>`) conflict with TypeScript's generic syntax (`Array<T>`).
// The dispatch function chooses between the two grammars based on the file extension.
// Real-life analogy: it's like choosing between two editions of the same dictionary —
// one that includes emoji definitions (TSX = has JSX syntax) and one that doesn't.
//
// # Arrow Functions as Variable Declarators
//
// TypeScript (unlike Python) allows functions to be assigned to variables:
//
//	const fn = (x: number) => x * 2;
//
// The parse tree for this is: variable_declarator → arrow_function.
// To extract "fn" as a function symbol, we detect the arrow_function node,
// check that its parent is a variable_declarator, and read the name from
// variable_declarator.name. This parallels the python parser's handling of
// lambda/function assignments.
//
// # this.method() Resolution
//
// TypeScript uses `this.method()` for intra-class calls. The python parser
// handles `self/cls.method()` the same way. The bare-method index built in
// Pass 1 maps:
//
//	class_name → { bare_method_name → qualified_name }
//
// During Pass 2, when a call_expression has a member_expression function node
// with receiver "this" and a property name that matches an entry in the
// bare-method index for the enclosing class, we resolve it directly.
//
// # Extraction Substrate: Hand-Walking the AST
//
// This parser uses direct child iteration (node.Children()) and
// node.ChildByFieldName for AST traversal in Passes 1 and 2.
// gotreesitter.Walk is used only in findFirstErrorByte (error detection).
// The choice matches the hand-walking substrate documented in
// IMPLEMENTATION_NOTES.md and produces the same traversal structure as the python parser,
// though the python parser uses Walk more broadly internally.
// No .scm query files are used. See IMPLEMENTATION_NOTES.md for the rationale.
//
// # Error Handling
//
// If tree.RootNode().HasError() is true, a ParseError with code E_PARSE_INCOMPLETE
// is emitted. Partial extraction is still attempted — a half-typed file does not
// silence the whole module.
package typescript

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

// tsGrammarState holds a lazily-initialized grammar (either TypeScript or TSX).
// The zero value is valid; once.Do initializes lang on first use.
type tsGrammarState struct {
	once sync.Once
	lang *gotreesitter.Language
	err  error
}

// typescriptGrammar is the lazily-initialized standard TypeScript grammar.
// Used for .ts and .mts files.
var typescriptGrammar tsGrammarState

// tsxGrammar is the lazily-initialized TSX grammar.
// Used for .tsx files.
var tsxGrammar tsGrammarState

// typescriptLanguage returns the lazily-initialized TypeScript grammar.
// Thread-safe: sync.Once ensures exactly one initialization.
//
// Returns a *parser.GrammarLoadError when the grammar fails to initialize.
// This allows cli/parse.go to emit E_GRAMMAR_LOAD_FAILED instead of the generic
// E_PARSE_INCOMPLETE, signaling an environment-level failure to consumers.
func typescriptLanguage() (*gotreesitter.Language, error) {
	typescriptGrammar.once.Do(func() {
		typescriptGrammar.lang = grammars.TypescriptLanguage()
		if typescriptGrammar.lang == nil {
			typescriptGrammar.err = &parser.GrammarLoadError{
				Language: "typescript",
				Cause:    fmt.Errorf("grammars.TypescriptLanguage() returned nil"),
			}
		}
	})
	return typescriptGrammar.lang, typescriptGrammar.err
}

// tsxLanguage returns the lazily-initialized TSX grammar.
// Thread-safe: sync.Once ensures exactly one initialization.
//
// Returns a *parser.GrammarLoadError when the grammar fails to initialize.
func tsxLanguage() (*gotreesitter.Language, error) {
	tsxGrammar.once.Do(func() {
		tsxGrammar.lang = grammars.TsxLanguage()
		if tsxGrammar.lang == nil {
			tsxGrammar.err = &parser.GrammarLoadError{
				Language: "tsx",
				Cause:    fmt.Errorf("grammars.TsxLanguage() returned nil"),
			}
		}
	})
	return tsxGrammar.lang, tsxGrammar.err
}

// selectGrammar picks the TypeScript or TSX grammar based on the file extension.
// .tsx → TsxLanguage(); everything else (.ts, .mts) → TypescriptLanguage().
// Returns the grammar name ("typescript" or "tsx") alongside the Language pointer.
//
// The suffix check is case-insensitive: "UPPERCASE.TSX" routes to TsxLanguage()
// exactly as "lowercase.tsx" does. This mirrors DetectLanguage in dispatch.go,
// which lowercases the extension before matching. Without this, a file named
// "FOO.TSX" would pass language detection but fall through to the standard
// TypeScript grammar inside ParseFile, producing ERROR nodes for JSX syntax.
func selectGrammar(relPath string) (*gotreesitter.Language, string, error) {
	if strings.HasSuffix(strings.ToLower(relPath), ".tsx") {
		lang, err := tsxLanguage()
		return lang, "tsx", err
	}
	lang, err := typescriptLanguage()
	return lang, "typescript", err
}

// ---------------------------------------------------------------------------
// qualify() — stable ID derivation
// ---------------------------------------------------------------------------

// qualify builds a qualified_name in the format "{rel_path}::{symbol_name}".
// This is the stable ID used for all symbols and edge endpoints in the output schema.
func qualify(relPath, name string) string {
	return relPath + "::" + name
}

// computeTypeScriptModuleName derives the importable module key from a
// workspace-relative TypeScript file path.
//
// TypeScript imports use relative specifiers like "./utils" or "../shared/types".
// The key stored in ModuleNameMap is the workspace-relative path with the
// file extension stripped, so that cross-file resolution in normalizeModuleRef
// can reconstruct the same key from any importing file's import specifier.
//
// Examples:
//
//	"utils.ts"                → "utils"
//	"cross_file/utils.ts"     → "cross_file/utils"
//	"cross_file/utils.tsx"    → "cross_file/utils"
//	"pkg/mod.mts"             → "pkg/mod"
//
// This is the workspace-relative path; the matching side (normalizeModuleRef in
// resolve.go) resolves a specifier like "./utils" from "cross_file/main.ts" to
// "cross_file/utils" and looks it up here.
func computeTypeScriptModuleName(relPath string) string {
	// Strip TypeScript extensions: .ts, .tsx, .mts (in order of specificity).
	for _, ext := range []string{".tsx", ".mts", ".ts"} {
		if strings.HasSuffix(relPath, ext) {
			return strings.TrimSuffix(relPath, ext)
		}
	}
	// No recognized extension — return as-is (future-proofing).
	return relPath
}

// ---------------------------------------------------------------------------
// Pass 1: symbol extraction
// ---------------------------------------------------------------------------

// parseSymbols extracts all symbols from the TypeScript parse tree (Pass 1).
//
// Extracts:
//   - MODULE (always present; anchor for import edges)
//   - function_declaration nodes
//   - class_declaration nodes
//   - method_definition nodes (inside classes; qualified as "ClassName.method")
//   - arrow_function nodes whose parent is a variable_declarator
//
// # Iterative DFS (wave 4d)
//
// Uses an explicit stack to avoid goroutine stack overflows on deeply-nested source.
// Carries the activeClass context alongside each node so method definitions within
// a class body are correctly qualified. DFS pre-order traversal is preserved:
// children are pushed in reverse so leftmost is popped first, matching the previous
// recursive behavior byte-for-byte on all snapshot fixtures.
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
			Language:      "typescript",
		},
	}

	localNameIndex = map[string]string{
		"MODULE": moduleQName,
	}
	bareMethodIndex = map[string]map[string]string{}

	type stackFrame struct {
		node        *gotreesitter.Node
		activeClass string
	}

	stack := []stackFrame{{node: root, activeClass: ""}}

	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		node := frame.node
		activeClass := frame.activeClass

		if node == nil {
			continue
		}
		ntype := node.Type(lang)

		switch ntype {
		case "function_declaration":
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
						SymbolType:    "function",
						Language:      "typescript",
						LineStart:     &ls,
						LineEnd:       &le,
					})
					localNameIndex[name] = qname
				}
			}
			// Fall through to default child-push (recurse into function body).

		case "class_declaration":
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
						Language:      "typescript",
						LineStart:     &ls,
						LineEnd:       &le,
					})
					localNameIndex[name] = qname
					// Push children with updated activeClass.
					children := node.Children()
					for j := len(children) - 1; j >= 0; j-- {
						stack = append(stack, stackFrame{node: children[j], activeClass: name})
					}
					continue // skip default child-push below
				}
			}
			// If nameNode was nil, fall through to default child-push.

		case "method_definition":
			nameNode := node.ChildByFieldName("name", lang)
			if nameNode != nil {
				rawName := nameNode.Text(source)
				if rawName != "" {
					var fullName string
					if activeClass != "" {
						fullName = activeClass + "." + rawName
					} else {
						fullName = rawName
					}
					qname := qualify(relPath, fullName)
					ls := int(node.StartPoint().Row) + 1
					le := int(node.EndPoint().Row) + 1
					symbols = append(symbols, schema.Symbol{
						QualifiedName: qname,
						Name:          fullName,
						FilePath:      relPath,
						SymbolType:    "function",
						Language:      "typescript",
						LineStart:     &ls,
						LineEnd:       &le,
					})
					localNameIndex[fullName] = qname
					if activeClass != "" {
						if bareMethodIndex[activeClass] == nil {
							bareMethodIndex[activeClass] = map[string]string{}
						}
						bareMethodIndex[activeClass][rawName] = qname
					}
				}
			}
			// Fall through to default child-push (recurse into method body).

		case "arrow_function":
			parent := node.Parent()
			if parent != nil && parent.Type(lang) == "variable_declarator" {
				nameNode := parent.ChildByFieldName("name", lang)
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
							SymbolType:    "function",
							Language:      "typescript",
							LineStart:     &ls,
							LineEnd:       &le,
						})
						localNameIndex[name] = qname
					}
				}
			}
			// Fall through to default child-push (recurse into arrow body).
		}

		// Default: push children with the same activeClass, in reverse order.
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

// deriveEnclosingClassName walks the ancestor chain looking for a
// class_declaration node and returns its name.
// Returns "" if the node is not inside a class.
// Mirrors Python's _derive_class_name() for TypeScript.
func deriveEnclosingClassName(node *gotreesitter.Node, lang *gotreesitter.Language, source []byte) string {
	ancestor := node.Parent()
	for ancestor != nil {
		if ancestor.Type(lang) == "class_declaration" {
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

// sanitizeImportPath strips control characters (code points < 0x20) from an import
// path specifier string, except for \t, \n, and \r.
//
// NUL bytes (\x00) are silently stripped — they almost always indicate malformed
// source already classified by tree-sitter via ERROR/MISSING nodes (E_PARSE_INCOMPLETE).
// We strip silently to avoid corrupting the module map with NUL-keyed entries.
// See the same function in internal/parser/python/parser.go for the full rationale.
func sanitizeImportPath(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
}

// parseCallsAndImports walks the parse tree collecting raw call edges and
// import statements (Pass 2).
//
// This pass uses a stateful current_fn_qname (the enclosing function context)
// to assign the source endpoint of each call edge.
//
// For call_expression nodes:
//   - Plain function call (function child is identifier): callee = identifier text.
//   - Method call (function child is member_expression):
//   - receiver == "this": resolve via bare-method index for enclosing class.
//   - other receivers: callee = property name (bare-name lookup will resolve).
//
// For import_statement nodes:
//   - Extract the source string literal and strip quotes.
//   - Map locally imported names to their target qualified names.
//
// # Iterative DFS (wave 4d)
//
// The recursive visitEdges closure has been replaced with an explicit stack to
// prevent goroutine stack overflows on deeply-nested source code. The currentFnQName
// context (which function/method scope we're currently inside) is carried as part of
// each stack frame. When we push children of a function/method/arrow node, we push
// them with the updated currentFnQName for that scope.
//
// DFS pre-order is preserved: children are pushed in reverse order (rightmost first)
// so leftmost is popped first. This produces byte-identical output to the previous
// recursive version on all snapshot fixtures.
func parseCallsAndImports(
	ctx context.Context,
	root *gotreesitter.Node,
	lang *gotreesitter.Language,
	source []byte,
	relPath string,
	localNameIndex map[string]string,
	bareMethodIndex map[string]map[string]string,
	moduleQName string,
) (rawCalls []parser.RawCall, importMap map[string]string) {
	importMap = map[string]string{}

	type edgeFrame struct {
		node           *gotreesitter.Node
		currentFnQName string
	}

	stack := []edgeFrame{{node: root, currentFnQName: moduleQName}}

	for len(stack) > 0 {
		// Check for deadline/cancellation at the top of each iteration.
		// Pass 2 walks every node in the AST; on a large file this loop runs
		// for as long as Pass 1 does. Without this check, a tight deadline would
		// not be respected during call extraction — only between passes.
		// Think of it like a kitchen timer: we peek at the clock at the start of
		// every step, not just between courses, so we can plate whatever's ready
		// the moment the buzzer sounds.
		if ctx.Err() != nil {
			break
		}

		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		node := frame.node
		currentFnQName := frame.currentFnQName

		if node == nil {
			continue
		}
		ntype := node.Type(lang)

		// childFnQName is the currentFnQName that children of this node should see.
		// For most nodes it's the same as the current frame's value.
		// For function/method/arrow nodes it's the new qualified name.
		childFnQName := currentFnQName
		pushChildren := true

		switch ntype {
		case "function_declaration":
			nameNode := node.ChildByFieldName("name", lang)
			if nameNode != nil {
				name := nameNode.Text(source)
				if name != "" {
					childFnQName = qualify(relPath, name)
				}
			}
			// Fall through: push children with updated childFnQName.

		case "method_definition":
			nameNode := node.ChildByFieldName("name", lang)
			if nameNode != nil {
				rawName := nameNode.Text(source)
				if rawName != "" {
					cls := deriveEnclosingClassName(node, lang, source)
					var fullName string
					if cls != "" {
						fullName = cls + "." + rawName
					} else {
						fullName = rawName
					}
					childFnQName = qualify(relPath, fullName)
				}
			}
			// Fall through: push children with updated childFnQName.

		case "arrow_function":
			parent := node.Parent()
			if parent != nil && parent.Type(lang) == "variable_declarator" {
				nameNode := parent.ChildByFieldName("name", lang)
				if nameNode != nil {
					name := nameNode.Text(source)
					if name != "" {
						childFnQName = qualify(relPath, name)
					}
				}
			}
			// Arrow function not in a variable_declarator (inline callback):
			// childFnQName stays unchanged (same enclosing fn scope).
			// Fall through: push children.

		case "call_expression":
			fnNode := node.ChildByFieldName("function", lang)
			if fnNode != nil {
				switch fnNode.Type(lang) {
				case "identifier":
					callee := fnNode.Text(source)
					if callee != "" {
						rawCalls = append(rawCalls, parser.RawCall{
							SourceQualifiedName: currentFnQName,
							CalleeName:          callee,
						})
					}

				case "member_expression":
					propNode := fnNode.ChildByFieldName("property", lang)
					objNode := fnNode.ChildByFieldName("object", lang)
					if propNode != nil {
						methodName := propNode.Text(source)
						var receiverText string
						if objNode != nil {
							receiverText = objNode.Text(source)
						}
						if methodName != "" {
							rawCalls = append(rawCalls, parser.RawCall{
								SourceQualifiedName: currentFnQName,
								CalleeName:          methodName,
								ReceiverText:        receiverText,
								EnclosingClassName:  deriveEnclosingClassName(node, lang, source),
							})
						}
					}
				}
			}
			// Fall through: push children (for nested call_expressions in args).

		case "import_statement":
			srcNode := node.ChildByFieldName("source", lang)
			if srcNode != nil {
				raw := srcNode.Text(source)
				if raw != "" {
					// Strip surrounding quotes and sanitize the module path.
					// Sanitization removes NUL bytes and other control characters
					// that would corrupt the module map used for cross-file resolution.
					stripped := sanitizeImportPath(strings.Trim(raw, `"'`))
					for _, child := range node.Children() {
						if child == nil {
							continue
						}
						if child.Type(lang) != "import_clause" {
							continue
						}
						for _, clauseChild := range child.Children() {
							if clauseChild == nil || clauseChild.Type(lang) != "named_imports" {
								continue
							}
							for _, spec := range clauseChild.Children() {
								if spec == nil || spec.Type(lang) != "import_specifier" {
									continue
								}
								nameNode := spec.ChildByFieldName("name", lang)
								aliasNode := spec.ChildByFieldName("alias", lang)
								if nameNode != nil {
									originalName := sanitizeImportPath(nameNode.Text(source))
									var localName string
									if aliasNode != nil {
										localName = sanitizeImportPath(aliasNode.Text(source))
									} else {
										localName = originalName
									}
									if originalName != "" && localName != "" {
										importMap[localName] = stripped + "::" + originalName
									}
								}
							}
						}
					}
				}
			}
			// import_statement children are already handled above; don't push children
			// again via the default path (would re-process the import_clause subtree).
			pushChildren = false
		}

		if pushChildren {
			// Push children in reverse order so leftmost is popped first (DFS pre-order).
			children := node.Children()
			for j := len(children) - 1; j >= 0; j-- {
				stack = append(stack, edgeFrame{node: children[j], currentFnQName: childFnQName})
			}
		}
	}

	return rawCalls, importMap
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

// ParseFile parses a single TypeScript source file and returns a *parser.FileResult.
//
// ctx is a context.Context that may carry a deadline or cancellation signal.
// By Go convention, context.Context is always the first argument. If ctx is
// cancelled or its deadline expires during parsing, ParseFile returns early
// with whatever symbols were extracted so far and a parse_errors entry.
//
// The relPath must be workspace-relative with forward slashes.
// source is the raw source bytes of the file.
//
// Grammar selection:
//   - .tsx extension → TsxLanguage() (supports JSX syntax)
//   - .ts / .mts extension → TypescriptLanguage() (standard TypeScript)
//
// The returned *parser.FileResult uses the shared type from internal/parser/types.go.
// The GrammarName field on the result documents which grammar was selected —
// used by tests to assert the TSX grammar is selected for .tsx files.
func ParseFile(ctx context.Context, source []byte, relPath string) (*parser.FileResult, error) {
	// Check for cancellation or deadline before doing any work.
	if err := ctx.Err(); err != nil {
		return &parser.FileResult{
			RelPath:     relPath,
			Symbols:     []schema.Symbol{},
			GrammarName: "typescript",
			ModuleName:  computeTypeScriptModuleName(relPath),
			HasError:    true,
		}, fmt.Errorf("context cancelled before parse started for %s: %w", relPath, err)
	}

	lang, grammarName, err := selectGrammar(relPath)
	if err != nil {
		return nil, err
	}

	p := gotreesitter.NewParser(lang)
	root, parseErr := parser.ParseSource(p, source, relPath)
	if parseErr != nil {
		return nil, parseErr
	}

	// Pass 1: extract symbols and build name indexes.
	symbols, localNameIndex, bareMethodIndex := parseSymbols(root, lang, source, relPath)
	// Sort symbols by qualified_name — schema v1 output invariant.
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
			GrammarName:     grammarName,
			ModuleName:      computeTypeScriptModuleName(relPath),
		}, fmt.Errorf("context cancelled during pass 1 for %s: %w", relPath, ctxErr)
	}

	// Pass 2: extract calls and build import map.
	// ctx is propagated so parseCallsAndImports can respect deadline cancellation
	// mid-walk. If the deadline expires during Pass 2, it returns partial rawCalls
	// collected so far — the partial results are still incorporated into the FileResult.
	moduleQName := qualify(relPath, "MODULE")
	rawCalls, importMap := parseCallsAndImports(ctx, root, lang, source, relPath, localNameIndex, bareMethodIndex, moduleQName)

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
			GrammarName:     grammarName,
			ModuleName:      computeTypeScriptModuleName(relPath),
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
		GrammarName:     grammarName,
		ModuleName:      computeTypeScriptModuleName(relPath),
	}, nil
}

// GrammarVersion returns the version string for the TypeScript grammar.
// Used by the --version --json output.
func GrammarVersion() string {
	return "gotreesitter-v0.15.3/typescript+tsx"
}
