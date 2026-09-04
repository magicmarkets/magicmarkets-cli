# Contributing to magicmarkets-cli

Thanks for taking the time to contribute. This document covers the
mechanics of sending a change; see the README for everything about the
codebase itself.

By participating in this project you agree to abide by the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- **Security issues** go through [SECURITY.md](SECURITY.md), never a public
  issue or PR.
- For anything beyond a small fix, open an issue first to discuss the
  approach — it saves rework on both sides.
- Read the README's [Conventions and invariants](README.md#conventions-and-invariants)
  section before touching anything money- or price-related. The one that
  matters most: **never place a real order to test a change.** Exercise
  write paths (`order place`, `order close*`, MCP `place_order`/`close_*`)
  against a local stub server, not the live API.

## Getting set up

No setup beyond a Go toolchain matching [go.mod](go.mod) — code generation
is pinned via `go.mod`'s `tool` directive, so `git clone && go build` works
on a fresh checkout with nothing extra to install.

```bash
git clone https://github.com/magicmarkets/magicmarkets-cli.git
cd magicmarkets-cli
make build   # or: make test
```

See the README's [Everyday commands](README.md#everyday-commands) for the
full `make` target list.

## Making a change

1. **Branch.** `git checkout -b your-change`
2. **Read the spec for anything wire-facing.** `magicmarkets api show <path>`
   or [docs/api-reference.md](docs/api-reference.md).
3. **Write the code.** Client changes go in `internal/magicmarkets`; the CLI
   and MCP layers consume it. Add a test if the change touches money,
   prices, or the trading gate — see the README's
   [Conventions and invariants](README.md#conventions-and-invariants).
4. **Check it:**
   ```bash
   make fmt && make lint && make test
   ```
5. **Exercise it for real** if there's a runtime surface — read-only
   commands against the live API, or a local stub for write paths. Tests
   alone don't prove a command works.
6. **Commit.** Explain *why* in the body, not just what. Note any spec
   quirk you had to work around, so the next person doesn't rediscover it.
7. **Open a pull request** describing what you verified and what you
   didn't. Fill in the PR template's checklist.

If your change touches the vendored OpenAPI spec, run `make update-spec`
before `make test` — `internal/magicmarketsapi/contract_test.go` will tell
you if the hand-written client drifted from the generated types.

## Code review

A maintainer will review your PR and may ask for changes. CI (where
configured) must pass. We squash-merge by default, so keep your commit
history readable but don't worry about a perfectly clean history within
the branch.

## Reporting bugs and requesting features

Use the issue templates. Include Go version, OS, `magicmarkets --version`
output, and enough detail (command run, expected vs. actual output) for
someone else to reproduce it.
