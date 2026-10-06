# Contributing to mvc

Thanks for your interest in contributing.

## Prerequisites

- Go (version specified in `go.mod`)
- [`golangci-lint`](https://golangci-lint.run/welcome/install/) for linting
- For the end-to-end conversion tests only: x264 and ffmpeg on `PATH` (the
  tests skip without them)

## Building

```sh
go build ./cmd/mvcdec
go build ./cmd/mvctools
```

## Running tests

```sh
go test ./...

# With race detector (recommended before submitting a PR):
go test -race ./...

# The pure-Go decoder, which every non-amd64 platform runs:
go test -tags purego ./...
```

## Linting

```sh
golangci-lint run
```

## Decoder changes

The decoder is verified bit-exactly: `TestConformance` hashes the output of
every stream under `testdata/conformance` against `manifest.txt`. A change
that alters any hash is a decoding change and needs a reason. The assembly
kernels in `*_amd64.s` each have a Go counterpart (`*_noasm.go`,
`*_generic.go`) and a test comparing the two on random input; keep both in
step.

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/):

| Prefix | When to use |
|--------|-------------|
| `feat` | New feature |
| `fix` | Bug fix |
| `perf` | Performance improvement with no behaviour change |
| `docs` | Documentation only |
| `ci` | CI / tooling changes |
| `chore` | Maintenance (deps, build, etc.) |
| `test` | Tests only |
| `refactor` | Refactor without behaviour change |

## Pull requests

- Keep PRs focused — one logical change per PR.
- All CI checks must pass before merge.
- Include tests for any new behaviour.

## Code style

- Standard `gofmt` formatting.
- No unnecessary comments — the code should speak for itself.
- Error strings are lowercase and do not end with punctuation.
- Context is threaded through all blocking calls.
