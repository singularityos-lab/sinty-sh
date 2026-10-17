# Contributing to sinty-sh

Thanks for your interest in contributing!

## Quick Start

```bash
git clone https://github.com/singularityos-lab/sinty-sh
cd sinty-sh
go build ./...
go test ./...
```

## Guidelines

- Keep changes additive and tested. sinty-sh runs every system script, often
  as root; prefer small, verifiable commits over large rewrites.
- Every change to the interpreter needs a script under `testdata/compat` that
  behaves the same under dash and under sinty-sh.
- Match the surrounding style. Comments explain WHY, not WHAT.
- By submitting a contribution you agree to the [CLA](CLA.md).

## License

GPL-3.0-only. The interpreter in `internal/interp`, `internal/expand` and
`internal/shpattern` comes from mvdan.cc/sh and keeps its BSD-3-Clause license.
