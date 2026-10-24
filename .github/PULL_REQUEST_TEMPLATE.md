## What

Describe the behavior changed and the affected API or package.

## Why

Explain the problem and reference the related issue by number, if any.

## How

Summarize the approach, verification and any migration or compatibility impact.

## Checklist

- [ ] Tests cover the change and `go test -race -count=1 ./...` passes.
- [ ] `golangci-lint run --timeout=5m --config=.golangci.yml` passes.
- [ ] Documentation is updated, or no documentation change is needed.
