"""A simple Python module used as a fixture for the Go parser parity test.

Exercises: top-level function definitions, function calls between top-level
functions, a bare import statement.

Note: f-strings are intentionally avoided. The gotreesitter Python grammar (v0.15.3)
has a GLR parsing bug where an f-string in a return statement causes the next
function definition in the same file to be missed by query patterns. Removing
f-strings from fixtures ensures consistent symbol extraction across grammar versions.
"""

import os


def greet(name):
    """Return a greeting string."""
    return "Hello, " + name + "!"


def shout(name):
    """Return an uppercased greeting."""
    return greet(name).upper()


def main():
    """Entry point."""
    result = shout("world")
    print(result)
