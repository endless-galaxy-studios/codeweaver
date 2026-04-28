/**
 * Utility functions for the cross-file TypeScript fixture.
 *
 * Exercises: named exports consumed by main.ts via import statement.
 * The cross-file resolution gate verifies that import edges are built
 * and that named imports produce import_map entries.
 */

export function helper(value: number): string {
    return "value=" + value;
}

export function transform(items: number[]): string[] {
    return items.map(helper);
}
