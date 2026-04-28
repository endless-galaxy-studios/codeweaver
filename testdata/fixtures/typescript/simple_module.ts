/**
 * Simple TypeScript module fixture.
 *
 * Exercises: top-level function declarations, exports.
 * No classes, no arrow functions — minimal surface for symbol extraction.
 *
 * Note: this fixture avoids template literals (backtick strings) because
 * gotreesitter TypeScript grammar may parse them differently from alternative
 * bindings. Verify behavior when bumping gotreesitter past the current pinned version.
 */

export function greet(name: string): string {
    return "Hello, " + name + "!";
}

function shout(message: string): string {
    return greet(message).toUpperCase();
}

export function main(): void {
    shout("world");
}
