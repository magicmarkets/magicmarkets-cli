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

## Authentication

This repo targets the **public v2 API**: `https://magicmarkets.com/v2`. A
caller presents one of two credentials to this process: an `X-Api-Key`
header, or `Authorization: Bearer` with an OAuth access token from
`https://magicmarkets.com/api/auth` (scope `mcp`). `internal/mcpserver`
accepts the same way, via the same client — but neither credential is what
actually reaches the v2 API/`/v2/stream` unchanged. An API key is forwarded
as-is; a Bearer token is *not* — the v2 API and stream don't accept the raw
OAuth access token, and `GET /me` doesn't work for it either (it requires a
genuine Firebase ID token, which a self-signed OAuth access token never is).
It's resolved first, via `POST {issuer}/oauth2/firebase-token` followed by a
Firebase `signInWithCustomToken` redemption, to the `magic-metadata-jwt`/
`session` pair those endpoints actually require — see
`internal/magicmarkets.MeResolver` (`internal/magicmarkets/meauth.go`) and
the README's [Authentication](README.md#authentication) section.

`magicmarkets mcp --http` serves the MCP streamable HTTP transport. The
process does not hold a Magic Markets credential. Every MCP request must
carry the caller's `X-Api-Key` **or** `Authorization: Bearer`; that value is
forwarded to the API and the stream. Don't remove the header check — without
it anyone who can reach the listener can open a session. Unauthenticated
`/mcp` responses include `WWW-Authenticate` pointing at RFC 9728
protected-resource metadata. That metadata lists **this host** as the
Authorization Server (not `https://magicmarkets.com/api/auth`) so Claude
POSTs `/register` here.

`internal/mcpserver/oauth.go` is a real OAuth proxy, not a passthrough: the
real Magic Markets AS only allowlists **this process's own** callback
(`{public-url}/mcp/callback`) for `MAGICMARKETS_OAUTH_CLIENT_ID` — it will
never allowlist an arbitrary downstream client's redirect_uri (Claude's,
Cursor's, ...). So `/authorize` never forwards the downstream client's
redirect_uri upstream: it runs its own PKCE leg against the upstream AS with
its own fixed callback, and `/callback` exchanges the upstream code, then
hands the real Magic Markets tokens to the downstream client as a one-time
proxy code (redeemed at `/token`). Don't "simplify" this back to forwarding
the caller's redirect_uri — that only works in tests against a fake AS that
doesn't enforce an allowlist; the real one will reject it.

The proxy keeps no server-side session state — the hosted deployment runs
multiple replicas with no session affinity, and the OAuth dance's three legs
(`/authorize`, the upstream's redirect to `/callback`, and the downstream
client's own `/token` call) aren't guaranteed to land on the same one. DCR
client_ids, in-flight login state, and one-time codes are instead sealed
(AES-256-GCM) self-contained tokens — see `oauthcrypto.go`. Every replica
must share `MAGICMARKETS_OAUTH_PROXY_SECRET` or a request landing on a
different replica than the one that minted a token can't decode it. Stdio
still uses `MAGICMARKETS_API_KEY` or `MAGICMARKETS_ACCESS_TOKEN` in the
process environment.

## Repeatable flags that hold event IDs use StringArrayVar, not StringSliceVar

`event_id` is comma-separated (`date,tag,seq`), and pflag's `StringSliceVar`
splits its value on commas — silently turning one ID into several invalid
ones. `--event` (`internal/cli/account.go`) and `--register`
(`internal/cli/stream.go`) use `StringArrayVar`, which takes each `--flag`
occurrence verbatim. Keep using `StringArrayVar` for any new flag whose
values may contain a comma; `StringSliceVar` is fine for flags whose values
never do (`--sport`, `--status`, `--type`).

## Layering

`internal/magicmarkets` must not import `internal/cli` or `internal/mcpserver`. It is
a standalone Go client library; the CLI and the MCP tools (stdio and HTTP) are both
consumers.

## MCP types are not API types

Stdio (`magicmarkets mcp`) and HTTP (`magicmarkets mcp --http`) share
`internal/mcpserver`. They must not expose `internal/magicmarkets` or generated
`internal/magicmarketsapi` structs as tool input/output.

| Layer | Package | Role |
| ----- | ------- | ---- |
| Spec / generated models | `internal/spec`, `internal/magicmarketsapi` | OpenAPI contract. Never hand-edit generated files. |
| Client wire types | `internal/magicmarkets` | Hand-written API models used by the REST/WS client. Kept in sync with generated types by `contract_test.go`. |
| MCP tool contract | `internal/mcpserver` (`types.go`) | Object-rooted structs the LLM/host sees. Reuse shared records (`order`, `betslip`, `stake`, …) across tools. Acyclic — no nested self-types, no `map[string]any`. |
| Mapper | `internal/mcpserver` (`mapper.go`) | The only place that converts MCP structs ↔ client structs (and MCP inputs → `CreateBetslipRequest` / `CreateOrderRequest` / `OrderFilter`). |

When the API grows a field, update the client type (and the contract test exception maps if needed), then extend the MCP struct and mapper — do not return the API struct from a tool handler. List tools wrap arrays in an object so structured content stays object-rooted.

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
back price is never below the requested price, and trading tools are unreachable
without `MAGICMARKETS_ALLOW_TRADING`. Extend those tests rather than weakening them.
