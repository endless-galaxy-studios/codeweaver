## Description

What does this PR change and why? Link any relevant issues.

## Type of change

- [ ] Bug fix
- [ ] New language / grammar addition
- [ ] Grammar fix (existing language)
- [ ] Refactor
- [ ] Documentation

## Checklist

- [ ] Every commit in this PR has a `Signed-off-by:` trailer (DCO). Use `git commit -s` or add the trailer manually.
- [ ] Snapshot fixtures updated if the JSON output changed (`testdata/fixtures/<language>/`)
- [ ] Parity tests pass (`make test`)
- [ ] `golangci-lint` passes (`make lint`)
- [ ] `make test` passes locally (unit tests + snapshot gate + parity gate)
