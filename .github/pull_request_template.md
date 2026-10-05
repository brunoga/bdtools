## Summary

<!-- What does this PR do and why? Link to a related issue if applicable. -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Refactor / internal improvement
- [ ] Documentation update

## Testing

<!-- How did you verify this works? New tests? Manual steps? -->

## Checklist

- [ ] `go test -race ./...` passes
- [ ] `go test -tags purego ./...` passes (the pure-Go decoder)
- [ ] `golangci-lint run` passes
- [ ] Decoder changes are bit-exact (the conformance test still passes)
- [ ] Documentation updated where applicable
