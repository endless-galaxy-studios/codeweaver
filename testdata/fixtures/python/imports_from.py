"""Import-from fixture.

Exercises: from-import statements (both dotted module paths and relative-style
absolute imports). Import edges are used internally for cross-file call resolution
but are not emitted in the schema v1 output — only "calls" edges appear in the
Edges array.

Note: f-strings are intentionally avoided due to a gotreesitter Python grammar
GLR parsing bug (v0.15.3). See simple_module.py for a full description.
"""

from os.path import join, exists
from collections import defaultdict, OrderedDict


def build_path(base, name):
    """Join base and name into a path."""
    return join(base, name)


def check_path(path):
    """Return True if path exists."""
    return exists(path)


def make_registry():
    """Return an empty registry."""
    return defaultdict(list)


def sorted_registry(data):
    """Return an ordered-dict sorted by key."""
    return OrderedDict(sorted(data.items()))
