/**
 * Main module for the cross-file TypeScript fixture.
 *
 * Exercises: named imports from utils.ts, cross-file call edges.
 * Mirrors the cross_file_calls/ python fixture pattern.
 *
 * Note: cross-file call edge resolution uses relative specifiers ("./utils")
 * resolved to workspace-relative keys ("cross_file/utils"). See resolve_test.go
 * for the matching resolution test and known edge cases.
 */

import { helper, transform } from "./utils";

export function process(data: number[]): string[] {
    return transform(data);
}

export function report(value: number): string {
    return helper(value);
}
