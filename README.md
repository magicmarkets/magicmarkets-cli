# magicmarkets-cli

A command-line interface for the [Magic Markets](https://magicmarkets.com) v2 API — stream live sports prices, quote selections, place and manage orders, and inspect your position. The same API is also available as MCP tools over stdio, so an LLM agent can drive it from a client that launches `magicmarkets mcp` as a subprocess.

Single static binary, authenticated with one API key. No request signing, no private keys.

```bash
magicmarkets markets --sport fb                     # find events with live prices
magicmarkets offers fb 2026-06-15,1001,2002         # list priced bet types
magicmarkets betslip create fb 2026-06-15,1001,2002 for,h --wait 5s
magicmarkets order place --betslip bs-123 --price 2.10 --stake 50
```

- **[Setup](#setup)** — install, authenticate, first commands
- **[Using it](#using-it)** — the bet flow, command reference, MCP over stdio, prices, errors
- **[Development](#development)** — layout, code generation, conventions, contributing

---

# Setup

## 1. Install

From a clone:

```bash
git clone https://github.com/magicmarkets/magicmarkets-cli
cd magicmarkets-cli
make install                 # installs `magicmarkets` into your Go bin directory
```

`make where` prints exactly where that landed. If `magicmarkets` is not found afterwards, that directory is not on your `PATH`:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"      # add to ~/.zshrc or ~/.bashrc
```

Prefer not to install? `make build` produces `./build/magicmarkets` and leaves your `PATH` alone.

<details>
<summary><code>go install</code> directly</summary>

Note the `/cmd/magicmarkets` path — installing the module root would build a binary called `magicmarkets-cli`:

```bash
go install ./cmd/magicmarkets
```

The Go module path is `magicmarkets-cli`, not a GitHub URL, so `go install github.com/…/magicmarkets-cli@latest` will not work. Cloning and `make install` is the supported path.
</details>

## 2. Add your API key

Create a key at magicmarkets.com under **Settings → API**. It is shown **once at creation**, so store it immediately.

Put it in `~/.magicmarkets/.env` so it works from any directory:

```bash
mkdir -p ~/.magicmarkets
echo 'MAGICMARKETS_API_KEY=your-key-here' > ~/.magicmarkets/.env
chmod 600 ~/.magicmarkets/.env
```

An env var or a project-local `.env` works too — `cp env.example .env` and fill it in.

## 3. Verify

```bash
$ magicmarkets status
version        v1.0.0
api url        https://magicmarkets.com/v2
ws url         wss://magicmarkets.com/v2/stream
lang           en
api key        ***********1234
env files      [/Users/you/.magicmarkets/.env]
authenticated  yes
```

Always check this before opening a stream — the WebSocket rejects a bad key at the handshake without a useful error.

That's the whole setup. Everything below is optional.

## Try it

```bash
magicmarkets balance                          # money position
magicmarkets xrates                           # exchange rates to USDT
magicmarkets markets --sport fb --limit 5     # events that currently have prices
magicmarkets offers fb <event-id> --depth 2   # priced bet types on one event
magicmarkets orders --open                    # your open orders
magicmarkets ticks 2.345                      # where a price lands on the tick schedule
magicmarkets api endpoints                    # every endpoint (no key, no network)
```

Add `--json` to any command to pipe it into `jq`.

## Configuration

Resolved in this order, first match winning:

| Priority | Source |
|---|---|
| 1 | Real environment variables |
| 2 | `./.env` |
| 3 | `~/.magicmarkets/.env` |
| 4 | `~/.env` |
| 5 | Built-in defaults |

| Variable | Default | Purpose |
|---|---|---|
| `MAGICMARKETS_API_KEY` | — | Your API key (required) |
| `MAGICMARKETS_API_URL` | `https://magicmarkets.com/v2` | REST base, including `/v2` |
| `MAGICMARKETS_WS_URL` | derived from `MAGICMARKETS_API_URL` | Stream endpoint |
| `MAGICMARKETS_LANG` | `en` | Event name language: `en`, `ko`, `zh-hans` |
| `MAGICMARKETS_TIMEOUT` | `30s` | Per-request timeout |
| `MAGICMARKETS_ALLOW_TRADING` | unset (off) | Lets [`magicmarkets mcp`](#mcp-over-stdio) place bets. No effect on the CLI. |

Global flags: `--json`, `--verbose`/`-v`, `--api-key`, `--api-url`.

---

# Using it

## The two-step bet flow

Placing a bet always takes two steps:

1. **A betslip** registers interest in one selection and receives a live quote. It costs nothing and commits nothing.
2. **An order** commits a stake against that quote.

Betslips are short-lived and carry **no price when created** — the quote arrives asynchronously, over the WebSocket as a `pmm` message or by polling. Hence `--wait`:

```bash
# 1. find an event that has prices
$ magicmarkets markets --sport fb --limit 5
SPORT  EVENT ID              EVENT               COMPETITION             STATUS     START
-----  --------              -----               -----------             ------     -----
fb     2026-06-15,1001,2002  Arsenal v Chelsea   England Premier League  pre_event  2026-06-15 16:00:00

# 2. read a bet_type straight off the feed
$ magicmarkets offers fb 2026-06-15,1001,2002 --depth 2
BET TYPE          MARKET  IR  PRICES (stake @ price)      TOTAL
--------          ------  --  ----------------------      -----
for,h             1x2     -   150.00@2.10  80.00@2.08     230.00
for,ah,h,-4       ah      -   200.00@1.95  120.00@1.94    320.00

# 3. quote it, waiting for the price to land
$ magicmarkets betslip create fb 2026-06-15,1001,2002 for,h --wait 5s
betslip id       bs-abc123
bet type         for,h
description      Home
expires          2026-06-15 15:42:10 (28s)
total available  230.00 USDT

Prices:
PRICE  MIN   MAX
-----  ---   ---
2.10   5.00  150.00
2.08   -     80.00

# 4. commit a stake (asks for confirmation)
$ magicmarkets order place --betslip bs-abc123 --price 2.10 --stake 50
```

**Never construct a `bet_type` by hand.** Copy it verbatim from `magicmarkets offers` or the stream — it encodes the market, handicap, outcome and direction in one string.

## Command reference

### Account

| Command | Purpose |
|---|---|
| `magicmarkets status` | Show config and verify the key |
| `magicmarkets balance` | Balance, open stake, smart credit, available |
| `magicmarkets xrates` | Exchange rates to USDT |
| `magicmarkets position` | Aggregate P&L, with `--grid` for the payoff matrix |

### Discovery

| Command | Purpose |
|---|---|
| `magicmarkets markets` | Events that currently have prices |
| `magicmarkets offers <sport> <event-id>` | Priced bet types on an event |
| `magicmarkets stream` | Tail the live price and account feed |

The v2 REST API has **no event-listing endpoint** — discovery happens over the WebSocket. These commands connect, read the snapshot, and disconnect, so they take a few seconds.

```bash
magicmarkets markets --sport fb,tennis --limit 20
magicmarkets markets --search arsenal
magicmarkets markets --in-play
magicmarkets offers fb 2026-06-15,1001,2002 --market ah --depth 3
magicmarkets stream --register fb:2026-06-15,1001,2002
magicmarkets stream --type order,bet          # only order activity
```

### Trading

| Command | Purpose |
|---|---|
| `magicmarkets betslip create [sport] [event] [bet-type]` | Quote a selection |
| `magicmarkets betslip get <id>` | Show a betslip and its prices |
| `magicmarkets betslip list` | Open betslip IDs (`--expand` for full detail) |
| `magicmarkets betslip refresh <id>` | Extend expiry |
| `magicmarkets order place` | Place an order against a betslip |
| `magicmarkets orders` / `magicmarkets order list` | List orders |
| `magicmarkets order get <id>` | Show one order and its bets |
| `magicmarkets order tracked <uuid>` | Look an order up by idempotency key |
| `magicmarkets order updates` | Orders changed in a time window |
| `magicmarkets order close <id>` | Cancel one order |
| `magicmarkets order close-many <id>...` | Cancel up to 500 orders |
| `magicmarkets order close-all` | Cancel every open order |

Lay bets and parlays:

```bash
# lay (against) a selection
magicmarkets betslip create --lay fb 2026-06-15,1001,2002 for,over,2.5

# a 2-leg accumulator
magicmarkets betslip create \
  --leg fb:2026-06-15,1001,2002:for,h \
  --leg fb:2026-06-16,1003,2004:for,over,2.5 --wait 5s
```

### Risk

| Command | Purpose |
|---|---|
| `magicmarkets heartbeat run` | Create a heartbeat and keep it alive in the foreground |
| `magicmarkets heartbeat create/list/get/refresh/cancel` | Manage heartbeats directly |

A heartbeat is a dead-man's switch: if it is not refreshed before it expires, **every open order is closed automatically**. Run one alongside an automated strategy so a crash cannot leave orders live.

```bash
$ magicmarkets heartbeat run --timeout 60
heartbeat hb-xyz created, expires 2026-06-15 15:43:10; refreshing every 20s
press Ctrl-C to cancel it and leave orders open
```

On Ctrl-C the heartbeat is cancelled cleanly, leaving orders open. If the process dies, the switch fires.

### MCP

| Command | Purpose |
|---|---|
| `magicmarkets mcp` | MCP tools over stdio — a client launches this; it does not listen on a port. See [MCP over stdio](#mcp-over-stdio) |

### Reference

| Command | Purpose |
|---|---|
| `magicmarkets bet-type <sport> <bet-type>` | Validate a bet type, show its payoff grid |
| `magicmarkets ticks <price>` | Snap a price onto the tick schedule |
| `magicmarkets api endpoints` | List every endpoint |
| `magicmarkets api show <path> [method]` | Endpoint detail: parameters, body, responses |
| `magicmarkets api schema [name]` | Component schemas |
| `magicmarkets api curl <method> <path>` | Generate a runnable curl command |
| `magicmarkets api search <term>` | Search endpoints and schemas |
| `magicmarkets api spec` | Print the embedded OpenAPI spec |

The `magicmarkets api` commands need **no API key and no network** — the OpenAPI 3.1 spec is compiled into the binary.

## Safe-operation checklist

Worth internalising before running anything that spends money:

- **Pass `--request-uuid` on every order.** It makes placement idempotent: a retry after a timeout cannot create a second order, and the order stays retrievable for six hours. Without it, a timeout leaves you unsure whether a bet was placed.
- **Run a heartbeat when automating.** Without one, a crashed strategy leaves orders live in the market.
- **Verify the key over REST before opening a stream.** The WebSocket fails the handshake with no useful error.
- **Take `bet_type` from the feed, never by hand.** Asian handicap lines are 4× the real line, so a hand-built string is easy to get silently wrong.
- **Check the snapped price.** `magicmarkets order place` shows it in the confirmation; that is the price the order actually runs with, not what you typed.

## Prices and the tick schedule

Every price lies on a fixed tick schedule whose step widens as the price grows:

| Decimal price | Tick |
|---|---|
| 1.01 – 2 | 0.01 |
| 2 – 3 | 0.02 |
| 3 – 4 | 0.05 |
| 4 – 6 | 0.10 |
| 6 – 10 | 0.20 |
| 10 – 20 | 0.50 |
| 20 – 30 | 1 |
| 30 – 50 | 2 |
| 50 – 100 | 5 |
| 100 – 1000 | 10 |

An off-tick order price is rounded so it never tightens your limit: **down** for back (`for`) orders, **up** for lay (`against`) orders. `magicmarkets order place` snaps the price itself and shows the result in the confirmation.

```bash
$ magicmarkets ticks 2.345
snapped price    2.34        # back: rounded down
$ magicmarkets ticks 2.345 --lay
snapped price    2.36        # lay: rounded up
```

Prices quoted from the feed are already on the schedule and are never re-rounded.

## Bet type grammar

`bet_type` is a comma-separated string beginning with the direction: `for` to back, `against` to lay. Handicaps always refer to the **home team**.

| Example | Meaning |
|---|---|
| `for,h` / `for,d` / `for,a` | Home / draw / away win |
| `for,dnb,h` | Home win, void if draw |
| `for,dc,h,d` | Double chance: home or draw |
| `for,over,2.5` / `for,under,2.5` | Over/under 2.5 goals |
| `for,ah,h,-4` | Asian handicap, home **-1.0** |
| `for,ahover,7` | Asian total over **1.75** |
| `for,cs,2,1` | Correct score 2–1 |
| `for,score,both,yes` | Both teams to score |
| `for,win,<team_id>` | Runner to win an outright |
| `for,top,3,<team_id>` | Runner to finish top 3 |

**Asian handicap lines are integers equal to 4× the real line** — `-4` is -1.0, `2` is +0.5, `7` is +1.75. This keeps 0.25-step lines integer-only on the wire.

Validate any candidate string:

```bash
$ magicmarkets bet-type fb for,ah,h,-4
description  Home -1.0 (Asian)
valid        yes
```

The full grammar — tennis periods, time-period tokens, every market — is in [`docs/api-reference.md`](docs/api-reference.md).

## JSON output

Every command takes `--json`:

```bash
magicmarkets orders --open --json | jq -r '.[] | "\(.order_id) \(.status)"'
magicmarkets markets --sport fb --json | jq -r '.[].event_id'
magicmarkets stream --type order --json          # one JSON object per line
```

**Stakes are two-element tuples, not objects** — `--json` mirrors the API wire
format exactly, so index them rather than reaching for a field name:

```bash
$ magicmarkets balance --json
{
  "balance": ["USDT", 10000.5],
  "open_stake": ["USDT", 152.55],
  "smart_credit": null
}

$ magicmarkets balance --json | jq '.balance[1]'      # amount
10000.5
$ magicmarkets balance --json | jq -r '.balance[0]'   # currency
USDT
```

The same applies to every money field: `want_stake`, `stake`, `profit_loss`, `total`, and the `min`/`max` inside a price level.

## MCP over stdio

There is no standalone MCP server to start or host. `magicmarkets mcp` is a stdio subprocess: an MCP client (Claude Code, Cursor, and the like) launches it and they exchange JSON-RPC on stdin/stdout. It does not listen on a port, and there is no HTTP or SSE transport.

Register the command with a client:

```bash
claude mcp add magicmarkets -e MAGICMARKETS_API_KEY=your-key -- magicmarkets mcp
```

Or in a client's MCP config — `mcpServers` is the client's name for a stdio subprocess, not a network service:

```json
{
  "mcpServers": {
    "magicmarkets": {
      "command": "magicmarkets",
      "args": ["mcp"],
      "env": { "MAGICMARKETS_API_KEY": "your-key" }
    }
  }
}
```

MCP support is developer-mode for now — expect some friction wiring a stdio server into a given client, and expect that to keep improving. For client-specific setup (where the config file lives, restart behavior, log locations), see that client's own docs rather than this README:

- [Claude Code: Connect to tools via MCP](https://docs.claude.com/en/docs/claude-code/mcp)
- [Model Context Protocol: Connect to local (stdio) servers](https://modelcontextprotocol.io/docs/develop/connect-local-servers) — covers Claude Desktop and other MCP clients generically

Across clients, the most common snag is the `command` field: a client often spawns the subprocess with a minimal PATH, not your shell's, so a bare `"command": "magicmarkets"` can fail to resolve even though the same command works from a terminal. If that happens, swap in the absolute path instead:

```bash
which magicmarkets   # or: make where
```

### Enabling trading

**Trading is off by default.** A fresh registration is read-only, so an agent asking to place a bet will find no `place_order` tool at all. Enable it with `MAGICMARKETS_ALLOW_TRADING=1`, **replace** the existing registration, and **restart your client**:

```bash
claude mcp remove magicmarkets
claude mcp add magicmarkets -e MAGICMARKETS_API_KEY=your-key -e MAGICMARKETS_ALLOW_TRADING=1 -- magicmarkets mcp
```

Restarting matters: a client reads the subprocess's tool list once at startup, so an already-running session keeps the read-only list even after you re-register.

Editing a client's MCP config file directly, set `MAGICMARKETS_ALLOW_TRADING` in `env`:

```json
{
  "mcpServers": {
    "magicmarkets": {
      "command": "magicmarkets",
      "args": ["mcp"],
      "env": {
        "MAGICMARKETS_API_KEY": "your-key",
        "MAGICMARKETS_ALLOW_TRADING": "1"
      }
    }
  }
}
```

Confirm which mode you are in without involving a client:

```bash
$ MAGICMARKETS_ALLOW_TRADING=1 magicmarkets mcp --print-tools
mode: trading ENABLED (via MAGICMARKETS_ALLOW_TRADING) — this process can place real bets

TOOL
----
close_all_orders
close_order
create_betslip
place_order
...
```

Run it without the env var to see the read-only list (11 tools vs 19). `magicmarkets mcp` also logs the mode to stderr on every start, which appears in your client's MCP logs (stderr is the only place those lines can go — stdout is the JSON-RPC stream).

### What each mode exposes

| Always available | Requires `MAGICMARKETS_ALLOW_TRADING` |
|---|---|
| `get_balance`, `get_exchange_rates`, `get_position` | `create_betslip` |
| `list_events`, `list_event_offers` | `place_order` |
| `list_orders`, `get_order` | `close_order`, `close_all_orders` |
| `list_betslips`, `get_betslip` | `create_heartbeat`, `refresh_heartbeat`, `cancel_heartbeat`, `list_heartbeats` |
| `validate_bet_type`, `snap_price` | |

Enable trading only if the agent should be able to bet real money. The money-spending tools carry MCP destructive hints so clients prompt before calling them.

Note this gate applies to **`magicmarkets mcp` only**. The CLI's own `magicmarkets order place` is always available — it has its own confirmation prompt instead.

## Errors

Errors carry a stable machine-readable `code` to branch on:

| HTTP | Code | Meaning |
|---|---|---|
| 400 | `validation_error` | Body or query failed validation; per-field reasons are printed |
| 400 | `order_closed` | Order exists but is already closed or settled |
| 401 | `auth_error` | Key missing, malformed or rejected |
| 403 | `forbidden` | Key valid but action not allowed |
| 404 | `not_found` | Resource unknown or invisible to this key |
| 409 | `order_already_created` | A `request_uuid` was reused; the existing order ID is reported |
| 409 | `limit_reached` | A per-customer cap was hit |
| 429 | `throttled` | Rate limited; retried automatically, honouring `Retry-After` |
| 500 | `server_error` | Internal error; quote the support token when reporting |

Throttled requests are retried automatically (twice by default) because a 429 means the request was rejected outright, so nothing was created. No other status is retried.

### Idempotency

```bash
uuid=$(uuidgen)
magicmarkets order place --betslip bs-123 --price 2.10 --stake 50 --request-uuid "$uuid"
magicmarkets order tracked "$uuid"     # recover after a timeout, safely
```

If a reused UUID is detected, `magicmarkets` fetches and shows the original order instead of failing.

### Rate limits

Per account, sliding window: 100 req/s burst and 1200 req/min sustained overall, with dedicated budgets of 10 req/s for betslip creation and 5 req/s for order placement.

## Troubleshooting

**`no API key configured`** — set `MAGICMARKETS_API_KEY`. `magicmarkets status` shows which `.env` files were read.

**`auth_error` (401)** — no key was sent at all. Check the variable name.

**`session_not_found` (404) on every call** — the key was sent but is not recognised. Regenerate it under Settings → API.

**`stream handshake failed`** — the WebSocket rejects a bad key at the HTTP handshake. Run `magicmarkets status` first; REST gives a clearer error.

**`magicmarkets markets` returns nothing** — the snapshot only contains events that *currently have live prices*, not the full fixture list. Try without `--sport`, or raise `--timeout`.

**Betslip has no prices** — quotes arrive asynchronously. Use `--wait 5s`. If it still has none, no source is quoting that selection.

**`updated_at_to must be at least 60 seconds in the past`** — `magicmarkets order updates` windows must end ≥60s ago and span ≤70 minutes.

**An agent says it cannot bet / needs `MAGICMARKETS_ALLOW_TRADING`** — the `magicmarkets mcp` subprocess is running read-only, so the betting tools are not registered. See [Enabling trading](#enabling-trading): re-register with `MAGICMARKETS_ALLOW_TRADING=1` and restart the client. Check the current mode with `magicmarkets mcp --print-tools`.

---

# Development

## Layout

```
cmd/magicmarkets/main.go          entry point — signal handling, version
internal/config/           .env + environment resolution
internal/magicmarkets/            API client — no CLI or MCP dependencies
  client.go                transport, envelope, 429 retry
  errors.go                typed APIError per error code
  types.go                 wire types (Stake is a [ccy, amount] tuple)
  ticks.go                 tick schedule and price snapping
  betslips.go orders.go account.go heartbeats.go
  stream.go                WebSocket client
internal/cli/              cobra command tree, table/JSON rendering
internal/mcpserver/        MCP tools over stdio, same client
internal/spec/             embedded openapi.json + reference commands
internal/magicmarketsapi/         generated models + the contract test guarding drift
tools/prepspec/            adapts the spec for oapi-codegen
docs/api-reference.md      full API reference (vendored)
```

`internal/magicmarkets` has no dependency on the CLI or MCP layers, so it is usable as a plain Go client library.

## Everyday commands

```bash
make test           # go test ./...
make lint           # go vet ./...
make fmt            # gofmt -w
make build          # ./build/magicmarkets
make install        # install `magicmarkets` into your Go bin directory
make where          # print where make install puts the binary
make generate       # regenerate internal/magicmarketsapi from the vendored spec
make update-spec    # refresh the vendored spec + docs, then regenerate
```

Tests need no API key and no network. Keep it that way.

The main package lives in `cmd/magicmarkets/`, not the module root, so the binary is
named `magicmarkets`. Building the root would name it after the module path —
`magicmarkets-cli` — which is not what the docs or `magicmarkets --help` tell you to run. Keep
new build targets pointed at `$(PKG)`.

`make build` and `make install` stamp `main.version` from `git describe`, so
`magicmarkets --version` reports something traceable. Override with
`make build VERSION=v1.2.3`.

## Code generation

Models in `internal/magicmarketsapi` are generated from the vendored OpenAPI spec with [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen).

**Setup: none.** oapi-codegen is pinned by the `tool` directive in `go.mod`, so `make generate` works on a fresh clone. The generated file is checked in, so `git clone && go build` never requires codegen.

```bash
make generate                 # regenerate from internal/spec/openapi.json
make update-spec              # pull the latest spec from the API, then regenerate
```

The canonical spec comes from [magicmarkets.com/magic-api/docs](https://magicmarkets.com/magic-api/docs) — `make update-spec` fetches `/magic-api/v2/openapi.json` plus the Markdown reference. Run `git diff` afterwards to see exactly what the API changed.

### These generated types are a contract reference, not what the CLI uses

The client in `internal/magicmarkets` keeps hand-written types, because generated code cannot express three things this API needs:

- **Stake tuples.** `["USDT", 115.38]` is an OpenAPI 3.1 tuple; oapi-codegen cannot generate one at all.
- **The bet-status union.** A bet's status is either a bare string or an object. `magicmarkets.BetStatus` unmarshals both; a generated union type pushes that branch onto every caller.
- **Non-pointer access.** The spec marks almost nothing `required`, so every generated field is a pointer. Threading nil checks through the CLI for fields the API always sends would be noise.

### What keeps the two honest

`internal/magicmarketsapi/contract_test.go` compares the JSON field names of every hand-written type against its generated counterpart, in both directions, and fails on any difference. An upstream field added, removed or renamed breaks `go test` after `make generate` instead of being discovered at runtime.

It earned its keep on the first run: it caught `bet_bar_values` missing from `Order`, which was silently dropping a field from `magicmarkets order get --json`.

If it fails, the spec and the client have diverged. **Fix the client**, or record the exception in that pair's `specOnly` / `handOnly` map *with a reason*. Do not delete the pair to make it pass.

### Two wrinkles handled by `tools/prepspec`

It adapts the spec before codegen without touching the vendored file:

- **`number` → `float64`.** oapi-codegen maps a formatless OpenAPI `number` to `float32` (~7 significant digits), not enough for prices and stakes. prepspec adds `format: double`. This includes the nullable `["number", "null"]` form, which covers precisely the achieved-price fields. A test asserts no generated money field is ever `float32`.
- **`StakeTuple` flattened** to an untyped array, since oapi-codegen fails outright on a 3.1 tuple. `magicmarkets.Stake` is the real typed equivalent.

Do not edit `internal/magicmarketsapi/types.gen.go` by hand.

## Conventions and invariants

Things this codebase relies on. Breaking one should be deliberate.

**Never place a real order to test a change.** `magicmarkets order place`, `magicmarkets order close*`, and the MCP `place_order` / `close_*` tools spend real money. Read-only commands (`status`, `balance`, `xrates`, `markets`, `offers`, `orders`, `position`) and the offline `magicmarkets api` commands are safe to exercise. For write paths, use a local stub server.

**Check the spec before inferring a shape.** `magicmarkets api show orders POST` beats guessing. Several endpoints break the common `{status, data}` pattern, and each break was a bug caught only by reading the spec:

- `GET /v2/heartbeats/` wraps data under a `heartbeats` key; every other list endpoint returns a flat array.
- `POST /v2/orders/{id}/close/` always returns `data: null`. Re-read the order for its final state.
- `POST /v2/betslips/{id}/refresh/` has no documented response body, so `RefreshBetslip` re-reads the betslip instead of decoding the reply.

**Keep the layering.** `internal/magicmarkets` must not import `internal/cli` or `internal/mcpserver`.

**Money is `float64`, and prices go through `SnapPrice`.** Never introduce `float32` on a price or stake path.

**New MCP tools that spend money go behind `AllowTrading`** and carry a destructive hint. The gate is tested; do not weaken it.

**Money-touching code needs a test.** `ticks.go` and the MCP trading gate both have tests asserting safety properties — a snap never tightens the bettor's limit, and trading tools are unreachable without `MAGICMARKETS_ALLOW_TRADING`. Extend those rather than working around them.

**Every command supports `--json`** and renders a table otherwise. Data goes to stdout; warnings and prompts go to stderr, so piping stays clean.

**Branch on error codes, not strings.** Use `magicmarkets.HasCode(err, magicmarkets.CodeOrderClosed)`.

## Two APIs share the "magicmarkets" name

This repo targets the **public v2 API**: `https://magicmarkets.com/v2`, authenticated with a single `X-Api-Key` header.

The sibling `magicmarkets-mcp` repo targets a **different surface** — the canary deployment, with OAuth/Firebase tokens (`MAGIC_TOKEN` / `MAGIC_JWT`) and the `magic-cpricefeed` WebSocket. Bet-type strings differ too: canary uses real Asian handicap lines (`for,ah,h,-0.5`) while v2 uses integers equal to 4× the line (`for,ah,h,-2`). Do not copy wire details between the repos.

## Making a change

See [CONTRIBUTING.md](CONTRIBUTING.md) for the branch → code → check → PR
workflow, including what to do if your change touches the vendored OpenAPI
spec.

---

## License

MIT — see [LICENSE](LICENSE).

Maintained by [Magic Markets](https://magicmarkets.com).
