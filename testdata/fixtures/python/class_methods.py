"""Class methods fixture.

Exercises: class definitions, methods (including __init__), self.method() calls
(intra-class), inter-function calls.

Note: f-strings are intentionally avoided due to a gotreesitter Python grammar
GLR parsing bug (v0.15.3) that causes the next function definition after an
f-string return to be missed. See simple_module.py for a full description.
"""


class Counter:
    """A simple counter class."""

    def __init__(self, start=0):
        self.value = start

    def increment(self):
        """Increment the counter."""
        self.value += 1

    def decrement(self):
        """Decrement the counter."""
        self.value -= 1

    def reset(self):
        """Reset to zero."""
        self.decrement()
        self.decrement()

    def get(self):
        """Return current value."""
        return self.value


class ExtendedCounter(Counter):
    """Counter with double-increment."""

    def double_increment(self):
        """Increment twice."""
        self.increment()
        self.increment()


def make_counter(start=0):
    """Factory function."""
    return Counter(start)


def run_demo():
    """Run a demo of the counter."""
    c = make_counter(10)
    c.increment()
    c.reset()
