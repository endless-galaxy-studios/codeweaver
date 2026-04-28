# codeweaver

Code-graph parser for AI coding agents and language tooling. Parses Python and
TypeScript source files and emits a structured JSON payload describing symbols,
relationships, and metadata. Integrate with plugins, language servers, or agents
that need cross-file call-graph context.

## Usage

```sh
# Parse a single file (JSON to stdout)
codeweaver parse path/to/file.py

# Parse multiple files
codeweaver parse src/index.ts src/utils.ts

# Print version information (JSON)
codeweaver version --json

# Print version (human-readable)
codeweaver version
```

## Flags

| Flag | Description |
|------|-------------|
| `--help` | Show help for any command |
| `--version` | Print version and exit |

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Parse error or invalid input |
| `2` | Usage error (bad arguments) |
| `3` | Internal error |

## Output format

All output is JSON to stdout. Errors are JSON to stderr. No other output is
written to stdout. The schema is at `schema/codeweaver-v1.json` in this archive.

## Shell completion

Install completions for your shell:

```sh
# bash (current session)
source <(codeweaver completion bash)

# bash (persistent — requires bash-completion)
codeweaver completion bash > /etc/bash_completion.d/codeweaver

# zsh
codeweaver completion zsh > "${fpath[1]}/_codeweaver"

# fish
codeweaver completion fish | source

# PowerShell
codeweaver completion powershell | Out-String | Invoke-Expression
```

Completions are not auto-installed — run one of the commands above to enable
them for your shell.

## Verifying integrity

Each release ships a `SHA256SUMS` file and a build-provenance attestation.

```sh
# Verify checksum
sha256sum --check SHA256SUMS

# Verify build attestation (requires GitHub CLI)
gh attestation verify <archive-filename> --repo endless-galaxy-studios/codeweaver

# Verify the manifest (primary trust anchor — verifying this covers all archives)
gh attestation verify SHA256SUMS --repo endless-galaxy-studios/codeweaver
```

## Support

https://github.com/endless-galaxy-studios/codeweaver
