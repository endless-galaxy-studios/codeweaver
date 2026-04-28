/**
 * TypeScript class methods fixture.
 *
 * Exercises: class declarations, method definitions, this.method() intra-class
 * call resolution (via _bare_method_index), and inter-function calls.
 */

class Counter {
    private value: number;

    constructor(start: number = 0) {
        this.value = start;
    }

    increment(): void {
        this.value += 1;
    }

    decrement(): void {
        this.value -= 1;
    }

    reset(): void {
        this.decrement();
        this.decrement();
    }

    get(): number {
        return this.value;
    }
}

class ExtendedCounter extends Counter {
    doubleIncrement(): void {
        this.increment();
        this.increment();
    }
}

function makeCounter(start: number = 0): Counter {
    return new Counter(start);
}

function runDemo(): void {
    const c = makeCounter(10);
    c.increment();
    c.reset();
}
