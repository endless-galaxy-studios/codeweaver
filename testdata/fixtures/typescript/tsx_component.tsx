/**
 * TSX component fixture.
 *
 * Exercises: TSX grammar variant (grammars.TsxLanguage()), which handles
 * JSX syntax inside TypeScript. This file requires the TSX grammar, NOT
 * the standard TypeScript grammar — because JSX angle-bracket expressions
 * conflict with TypeScript's generic syntax at the grammar level.
 *
 * A parity test verifies the grammar name used is "tsx".
 */

interface GreeterProps {
    name: string;
}

function Greeter(props: GreeterProps): JSX.Element {
    return <div>{"Hello, " + props.name}</div>;
}

function formatName(name: string): string {
    return name.trim();
}

export function App(): JSX.Element {
    const name = formatName("world");
    return <Greeter name={name} />;
}
