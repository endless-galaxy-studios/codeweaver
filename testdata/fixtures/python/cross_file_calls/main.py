"""Main module for the cross-file call fixture.

Exercises cross-file call resolution: main.py imports helper and transform
from utils.py. The parse pipeline must resolve calls to those imported names
into edges pointing to the definitions in utils.py.

Note: f-strings are intentionally avoided due to a gotreesitter Python grammar
GLR parsing bug (v0.15.3). See simple_module.py for a full description.
"""

from cross_file_calls.utils import helper, transform


def process(data):
    """Process a list using imported utilities."""
    return transform(data)


def report(value):
    """Format a single value using the helper."""
    return helper(value)
