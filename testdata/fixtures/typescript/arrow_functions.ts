/**
 * Arrow functions fixture.
 *
 * Exercises: arrow functions assigned to variable declarators.
 *   const fn = () => { ... }
 *   const fn = (x: T) => expr
 *
 * These must produce function symbols qualified as "{file}::fn",
 * the same as named function declarations.
 */

const add = (a: number, b: number): number => {
    return a + b;
};

const multiply = (a: number, b: number): number => {
    return add(a, a + b - b) * b;
};

const greet = (name: string): string => {
    return "Hello, " + name;
};

export const run = (): void => {
    const result = add(1, 2);
    multiply(result, 3);
    greet("world");
};
