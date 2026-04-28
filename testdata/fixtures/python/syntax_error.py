"""Syntax error fixture.

This file is intentionally malformed. The Go parser must:
  1. Detect the ERROR node produced by tree-sitter.
  2. Still emit the symbols that were successfully parsed before the error.
  3. Emit a parse_errors entry with error_code="E_PARSE_INCOMPLETE".
  4. Exit with code 0 (partial result, not fatal failure).

Note: The gotreesitter Python grammar uses GLR error recovery, so common syntax
mistakes (like a missing colon) may be recovered without ERROR nodes. This fixture
uses a pattern that reliably triggers tree-sitter ERROR detection.
"""


def valid_function():
    """This function is syntactically valid."""
    return "hello"


# Intentionally broken: invalid token sequence that triggers a tree-sitter ERROR node.
x = @@@invalid
