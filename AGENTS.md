# Agent Guidelines — magicmarkets-cli

## Never place a real order

`magicmarkets order place`, `magicmarkets order close*`, and the MCP `place_order` /
`close_order` / `close_all_orders` tools spend real money. Do not run them to
"verify" a change. Read-only endpoints (`status`, `balance`, `xrates`,
`markets`, `offers`, `orders`, `position`) and the offline `magicmarkets api` commands
are safe to exercise.

## The API contract lives in this repo

| File | Role |
| ---- | ---- |
| `internal/spec/openapi.json` | Canonical OpenAPI 3.1 spec, embedded in the binary |
| `docs/api-reference.md` | Full prose reference, including the bet-type grammar |
| `internal/magicmarketsapi/types.gen.go` | Generated models — **never hand-edit**, run `make generate` |

The first two are vendored copies of what magicmarkets.com serves. Refresh with
`make update-spec` (which regenerates too) — never hand-edit them. When adding or
changing a client method, check the spec first rather than inferring the shape.

`internal/magicmarketsapi/contract_test.go` compares every hand-written type in
`internal/magicmarkets` against its generated counterpart by JSON field name, in both
directions. If it fails, the spec and the client have diverged: fix the client,
or record the exception in that pair's `specOnly` / `handOnly` map **with a
reason**. Do not delete the pair to make it pass.

`tools/prepspec` adapts the spec before codegen and must keep doing two things:
widen formatless `number` to `format: double` (oapi-codegen would otherwise emit
`float32`, too imprecise for money — including the nullable
`["number", "null"]` price fields), and flatten `StakeTuple`, which oapi-codegen
cannot generate at all.

Watch for endpoints whose response does not follow the common pattern:

- `GET /v2/heartbeats/` wraps its data under a `heartbeats` key; every other
  list endpoint returns a flat array.
- `POST /v2/orders/{id}/close/` always returns `data: null`. Re-read the order
  to show its final state.
- `POST /v2/betslips/{id}/refresh/` has no documented response body, so
  `RefreshBetslip` re-reads the betslip instead of decoding the refresh reply.

## Two APIs share the "magicmarkets" name

This repo targets the **public v2 API**: `https://magicmarkets.com/v2`,
authenticated with a single `X-Api-Key` header.

The sibling `magicmarkets-mcp` repo targets a **different surface** — the canary
deployment, with OAuth/Firebase tokens (`MAGIC_TOKEN` / `MAGIC_JWT`) and the
`magic-cpricefeed` WebSocket. Bet-type strings differ too: canary uses real
Asian handicap lines (`for,ah,h,-0.5`) while v2 uses integers equal to 4x the
line (`for,ah,h,-2`). Do not copy wire details between the repos.

## Layering

`internal/magicmarkets` must not import `internal/cli` or `internal/mcpserver`. It is
a standalone Go client library; the CLI and the MCP stdio tools are both consumers.

## The binary is `magicmarkets`, and the main package must stay in cmd/magicmarkets

`go install`/`go build` name a root main package after the module path, which
would produce `magicmarkets-cli` — not what the docs or `magicmarkets --help` tell users to
run. The main package therefore lives in `cmd/magicmarkets/`. Point any new build
target at `$(PKG)`, never at `.`.

## Running tests

```bash
go test ./...
go vet ./...
```

Tests must not require an API key or network access. `internal/config` tests
clear the `MAGICMARKETS_*` environment so a developer's real key cannot leak into an
assertion — keep that isolation if you add cases there.

## The MCP trading gate is `MAGICMARKETS_ALLOW_TRADING`

`magicmarkets mcp` registers the money-spending tools when `MAGICMARKETS_ALLOW_TRADING`
is truthy. There is no `--allow-trading` flag — the env var is the only door, so a
client that can set `env` but not `args` can still opt in.

`MAGICMARKETS_ALLOW_TRADING` rejects unrecognised values rather than defaulting to
false, so a typo cannot silently leave betting disabled. Keep that behaviour.

`magicmarkets mcp --print-tools` reports the mode and tool list without a key or a
stdio session; it derives the list by registering into a throwaway server, so it
cannot drift from what `Serve` exposes. Do not replace it with a hardcoded list.

## Money-touching code needs a test

The tick schedule (`internal/magicmarkets/ticks.go`) and the MCP trading gate
(`internal/mcpserver`) both have tests asserting safety properties: a snapped
price never tightens the bettor's limit, and trading tools are unreachable
without `MAGICMARKETS_ALLOW_TRADING`. Extend those tests rather than weakening them.
