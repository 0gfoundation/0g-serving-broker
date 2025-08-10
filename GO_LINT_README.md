# Go Lint CI

Go code quality checks using `golangci-lint`.

## Setup

```bash
cd api
make install-tools
```

## Usage

```bash
# Run lint check
make lint

# Run CI lint check  
make lint-ci
```

## What's Added

- `.golangci.yml` - lint config
- `.github/workflows/lint.yml` - CI workflow
- `api/Makefile` - lint commands

## CI

- Runs on main/develop branches
- Runs on PRs
- Checks: typecheck + gofmt
- 10 min timeout 