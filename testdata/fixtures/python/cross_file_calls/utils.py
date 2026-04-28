"""Utility functions shared by main.py in the cross-file call fixture.

Exercises: cross-file call edge resolution — when main.py imports helper()
from utils.py and calls it, the edge should resolve to utils.py::helper.

Note: f-strings are intentionally avoided due to a gotreesitter Python grammar
GLR parsing bug (v0.15.3). See simple_module.py for a full description.
"""


def helper(value):
    """Convert an integer to a descriptive string."""
    return "value=" + str(value)


def transform(items):
    """Apply helper to each item."""
    return [helper(item) for item in items]
