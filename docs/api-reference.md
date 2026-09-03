# Magic Markets API

> Version 0.1.0

REST and WebSocket API for MagicMarkets. Place orders, stream real-time
prices, and manage your positions programmatically.

## Authentication

Every request must be authenticated with an **API key**. Pass it in the
`X-Api-Key` header:

```
X-Api-Key: <your-api-key>
```

### Getting an API key

API keys are created and managed through the MagicMarkets website at
[magicmarkets.com](https://magicmarkets.com/):

1. Go to **Settings → API**.
2. Click **Add API Key** and give it a name.
3. Copy the key value — it is shown **once at creation**, so store it
   somewhere safe immediately.

You can also revoke or rename existing keys from the same page. There is
no endpoint in this API to manage keys — all key management happens on
the website.

### Using a key

Send the key on every request:

```bash
curl https://magicmarkets.com/v2/xrates/ \
  -H "X-Api-Key: $MAGIC_API_KEY"
```

## Quickstart

This walkthrough goes from zero to a placed bet: stream prices over the
WebSocket, read a `bet_type` off the feed, quote it as a betslip, place
an order, and watch the order update on the same socket.

### Before you start

You need an API key (see **Authentication** above) and Python 3.9+ with
two libraries:

```bash
pip install requests websockets
```

Every snippet reads its configuration from the environment:

```bash
export MAGIC_API_URL="https://<host>/v2"
export MAGIC_WS_URL="wss://<host>/v2/stream"
export MAGIC_API_KEY="<your-api-key>"
```

Three concepts carry the whole flow:

- **Two steps to a bet.** A *betslip* registers your interest in one
  selection (`POST /v2/betslips/`); its live quote then arrives as
  `["pmm", …]` entries on the WebSocket. An *order* commits a stake
  against that quote (`POST /v2/orders/`). You always create the
  betslip first.
- **`bet_type` comes from the feed.** Offers on the WebSocket carry the
  `bet_type` string ready to use — pass it to `POST /v2/betslips/`
  verbatim. You never need to construct or parse it (the grammar in
  **Sports & bet types** is reference material, not required reading).
- **Stakes are USDT.** Wherever money appears it is a
  `["USDT", amount]` pair.

### Step 1 — Check your key

Before opening the socket, prove the key works with a cheap REST call —
the WebSocket closes silently on a bad key, so verify here first:

```bash
curl "$MAGIC_API_URL/xrates/" -H "X-Api-Key: $MAGIC_API_KEY"
```

A `200` with `{"status": "ok", ...}` means you are good to go.

### Step 2 — Connect to the stream

Connect with the key as a query parameter. Every frame the server sends
is a batch envelope `{"ts": ..., "data": [...]}` — iterate `data[]` and
dispatch on each entry's leading type tag (see the **Streaming API**
endpoint for the full wire format):

```python
import json, os
from websockets.sync.client import connect

ws = connect(f"{os.environ['MAGIC_WS_URL']}?api_key={os.environ['MAGIC_API_KEY']}")

events, synced = [], False
while not synced:
    frame = json.loads(ws.recv())
    for entry in frame["data"]:
        if entry[0] == "event":
            events.append(entry[1])   # {"sport": ..., "event_id": ..., ...}
        elif entry[0] == "sync":
            synced = True
```

After `["sync", …]` you hold the list of currently-priced events, e.g.:

```json
["event", {"event_type": "normal", "sport": "fb",
  "event_id": "2026-06-15,1001,2002", "competition_id": 1,
  "competition_name": "England Premier League",
  "competition_country": "XE", "home": "Arsenal", "away": "Chelsea",
  "event_name": "Arsenal vs. Chelsea", "ir_status": "pre_event",
  "start_time": "2026-06-15T15:00:00Z"}]
```

### Step 3 — Register for offers on an event

Pick an event and register. The server replies with one `["offer", …]`
per bet type (the snapshot), then an ok `["response", …]`:

```python
event = events[0]
ws.send(json.dumps(["register_event", event["sport"], event["event_id"]]))

offers, registered = [], False
while not registered:
    frame = json.loads(ws.recv())
    for entry in frame["data"]:
        if entry[0] == "offer":
            offers.append(entry[1])
        elif entry[0] == "response":
            if entry[1]["status"] == "ok":
                registered = True
            else:
                raise SystemExit(f"register_event failed: {entry[1]['code']}")
```

(Registering an event that has not yet appeared in the sync stream is
not an error — you just get an empty snapshot, and offers start flowing
if the event becomes priced. Prefer event ids you saw in the sync
stream.) From now on the full offer set is re-broadcast whenever this
event's prices change.

### Step 4 — Read the bet_type off an offer

Each offer is one priced selection. Everything the next step needs is
already in it:

```json
["offer", {
  "sport": "fb",
  "event_id": "2026-06-15,1001,2002",
  "bet_type": "for,ah,h,1",
  "market_type": "ah",
  "in_running": false,
  "price_list": [
    {"effective": {"price": 2.0, "min": ["USDT", 5.0], "max": ["USDT", 150.0]}},
    {"effective": {"price": 1.99, "min": null, "max": ["USDT", 80.0]}}
  ]
}]
```

`price_list` is sorted by price descending; `min` is `null` when there
is no minimum stake. Pick a priced offer — its `sport`, `event_id` and
`bet_type` are everything the next step needs:

```python
offer = next(o for o in offers if o["price_list"])
```

### Step 5 — Create a betslip

Quote the selection by passing the offer's fields through verbatim:

```python
import requests

API = os.environ["MAGIC_API_URL"]
HEADERS = {"X-Api-Key": os.environ["MAGIC_API_KEY"]}

betslip = requests.post(f"{API}/betslips/", headers=HEADERS, json={
    "sport": offer["sport"],
    "event_id": offer["event_id"],
    "bet_type": offer["bet_type"],
    "betslip_type": "normal",
}).json()["data"]
```

The response registers the betslip — note the `betslip_id` and the
`expiry_ts` (betslips are short-lived; re-create one that expires).
Do not expect prices in this response: your private quote arrives on
the WebSocket you already hold open, as `["pmm", …]` entries carrying
your `betslip_id` — typically within a couple of seconds, refreshed
while the betslip stays open:

```python
quote = None
while quote is None:
    frame = json.loads(ws.recv())
    for entry in frame["data"]:
        if entry[0] == "pmm" and entry[1]["betslip_id"] == betslip["betslip_id"]:
            if entry[1]["price_list"]:
                quote = entry[1]
```

```json
["pmm", {
  "betslip_id": "65b6ff7da480479b9dda1c7ff765c434",
  "sport": "fb",
  "event_id": "2026-06-15,1001,2002",
  "bet_type": "for,ah,h,1",
  "status": {"code": "success"},
  "price_list": [
    {"effective": {"price": 2.0, "min": ["USDT", 5.0], "max": ["USDT", 150.0]}}
  ],
  "total": ["USDT", 150.0]
}]
```

The `price_list` uses the same format as offers — prices descending,
stakes in USDT. A pmm whose `price_list` stays empty means there is no
liquidity for this selection right now — pick another offer and
re-quote.

If you are not holding the stream open, poll
`GET /v2/betslips/{betslip_id}/` until `price_list` populates.

### Step 6 — Place an order

Commit a stake at one of the quoted prices. Three fields are
required (`betslip_id`, `price`, `stake`); `duration` is the order's
lifetime in **seconds** (optional, default 15):

```python
best = quote["price_list"][0]["effective"]

order = requests.post(f"{API}/orders/", headers=HEADERS, json={
    "betslip_id": betslip["betslip_id"],
    "price": best["price"],
    "stake": ["USDT", 10.0],
    "duration": 5.0,
}).json()["data"]
```

The response confirms acceptance — `order_id` and `status: "open"`
(abridged; `bets` appear on the subsequent updates as the order fills):

```json
{
  "order_id": 5001,
  "status": "open",
  "bet_type": "for,ah,h,1",
  "sport": "fb",
  "want_price": 2.0,
  "want_stake": ["USDT", 10.0],
  "closed": false,
  "price": null,
  "stake": null,
  "profit_loss": null
}
```

### Step 7 — Watch the order on the WebSocket

Order updates arrive on the socket you already hold open, as
`["order", …]` and `["bet", …]` entries in the same envelopes as
offers. An order moves `open → pending → done | failed`; when it
closes, `price`, `stake` and `profit_loss` are filled in:

```python
while True:
    frame = json.loads(ws.recv())
    for entry in frame["data"]:
        if entry[0] == "order" and entry[1]["order_id"] == order["order_id"]:
            o = entry[1]
            print("order:", o["status"], o.get("close_reason"))
            if o["status"] in ("done", "failed"):
                raise SystemExit(0)
```

Note that a `done` order is filled, not settled — the final
`profit_loss` lands after the event finishes. To re-check an order
later (e.g. after a restart), `GET /v2/orders/{order_id}/` returns the
same object on demand.

### Handling errors

Always check `status` before reading `data`. REST errors use the
envelope from the **Errors** section below — `validation_error`
bodies name the offending field (e.g. `bet_type: ["invalid_bet_type"]`),
and on `429` honour `data.retry_after` (the limits are listed under
**Rate limiting**). WebSocket errors arrive
in-band as `["response", {"status": "error", …}]` entries; the reason table and the
silent-close cases are documented on the **Streaming API** endpoint.

### Complete example

The whole flow, runnable as-is with the three environment variables set:

```python
import json, os
import requests
from websockets.sync.client import connect

API = os.environ["MAGIC_API_URL"]
KEY = os.environ["MAGIC_API_KEY"]
HEADERS = {"X-Api-Key": KEY}

# 1. verify the key via REST first — the socket closes silently on a bad key
requests.get(f"{API}/xrates/", headers=HEADERS).raise_for_status()

with connect(f"{os.environ['MAGIC_WS_URL']}?api_key={KEY}") as ws:
    # 2. initial sync: collect events until ["sync", …]
    events, synced = [], False
    while not synced:
        frame = json.loads(ws.recv())
        for entry in frame["data"]:
            if entry[0] == "event":
                events.append(entry[1])
            elif entry[0] == "sync":
                synced = True
    if not events:
        raise SystemExit("no priced events right now")

    # 3+4. register events until one returns a priced offer
    offer = None
    for event in events:
        ws.send(json.dumps(["register_event", event["sport"], event["event_id"]]))
        offers, registered = [], False
        while not registered:
            frame = json.loads(ws.recv())
            for entry in frame["data"]:
                if entry[0] == "offer":
                    offers.append(entry[1])
                elif entry[0] == "response":
                    if entry[1]["status"] == "ok":
                        registered = True
                    else:
                        raise SystemExit(f"register_event failed: {entry[1]['code']}")
        offer = next((o for o in offers if o["price_list"]), None)
        if offer:
            print("picked", offer["bet_type"], "on", event["event_name"])
            break
        ws.send(json.dumps(["unregister_event", event["sport"], event["event_id"]]))
    if offer is None:
        raise SystemExit("no priced offers right now")

    # 5. register a betslip (bet_type verbatim), then read the quote off the socket
    resp = requests.post(f"{API}/betslips/", headers=HEADERS, json={
        "sport": offer["sport"],
        "event_id": offer["event_id"],
        "bet_type": offer["bet_type"],
        "betslip_type": "normal",
    }).json()
    if resp["status"] != "ok":
        raise SystemExit(f"betslip rejected: {resp}")
    betslip = resp["data"]

    quote = None
    while quote is None:
        frame = json.loads(ws.recv())
        for entry in frame["data"]:
            if entry[0] == "pmm" and entry[1]["betslip_id"] == betslip["betslip_id"]:
                if entry[1]["price_list"]:
                    quote = entry[1]
    print("quoted", quote["price_list"][0])

    # 6. place an order at the quote's best price
    best = quote["price_list"][0]["effective"]
    resp = requests.post(f"{API}/orders/", headers=HEADERS, json={
        "betslip_id": betslip["betslip_id"],
        "price": best["price"],
        "stake": ["USDT", 10.0],
        "duration": 5.0,
    }).json()
    if resp["status"] != "ok":
        raise SystemExit(f"order rejected: {resp}")
    order = resp["data"]
    print("placed order", order["order_id"], "status:", order["status"])

    # 7. watch it on the same socket
    while True:
        frame = json.loads(ws.recv())
        for entry in frame["data"]:
            if entry[0] == "order" and entry[1]["order_id"] == order["order_id"]:
                o = entry[1]
                print("order:", o["status"], o.get("close_reason"))
                if o["status"] in ("done", "failed"):
                    raise SystemExit(0)
```

From here: the **Streaming API** endpoint documents every message on the
socket, **Betslips** and **Orders** cover the remaining endpoints
(including parlays and lay orders), and **Heartbeats** provides a
dead-man's switch for automated trading.

## Response format

All JSON responses share a common envelope:

```json
{ "status": "ok", "data": ... }
```

On error:

```json
{ "status": "error", "code": "<code>", "data": <details> }
```

`data` may be `null`, a string, or an object — depending on the code. See
the **Errors** section below for the most common shapes.

## Errors

Error responses always include `status: "error"` and a stable string
`code` that clients should branch on. The HTTP status conveys the
category; `code` narrows it down.

### Common codes

| HTTP | `code` | When |
|------|--------|------|
| 400 | `validation_error` | Request body or query failed validation. `data.validation_errors` is a `{ field: [reason, ...] }` map; cross-field problems land in `non_field_errors`. A rejected list field is index-keyed instead: `{ field: { "0": [reason, ...] } }`. |
| 400 | `order_closed` | `POST /v2/orders/{id}/close/` on an order that exists but is already closed or settled. Distinct from `not_found`, which means the order id is unknown. |
| 401 | `auth_error` | API key missing, malformed, or rejected. Also used by login flows for 2FA / inactive / locked accounts. |
| 403 | `forbidden` | The key is valid but is not allowed to perform this action. |
| 404 | `not_found` | The addressed resource (betslip, order, heartbeat, token, session) does not exist or is not visible to this key. |
| 409 | `order_already_created` | A `request_uuid` from `POST /v2/orders/` was reused. `data` includes the existing `order_id`. |
| 409 | `limit_reached` | A per-customer cap was hit (e.g. maximum API tokens). `data.detail` describes the cap. |
| 429 | `throttled` | Rate limit hit — see **Rate limiting** below. `data` is `{ "message": "...", "retry_after": <seconds> }` and a `Retry-After` header is sent. |
| 500 | `server_error` | Unexpected internal error. `data` is `["An error has occurred, token:", "<token>"]`; quote the token if you contact support. |
| 503 | (no body envelope) | magic-api could not reach the upstream — body is `{ "detail": "Service unavailable" }`. |

For `validation_error` responses, branch on the inner reason — the
keys of `data.validation_errors` (`non_field_errors` for cross-field
rejections, otherwise the offending field name). Each endpoint
documents the concrete codes it emits next to its 400 response.

## Rate limiting

Limits are per **account**: all of an account's API keys draw from
one budget. The window is sliding — capacity frees up as earlier
requests age out, with no calendar-aligned reset.

| Applies to | Limit |
|------------|-------|
| All endpoints | 100 requests/second burst, 1200 requests/minute sustained |
| `POST /v2/betslips/` | 10 requests/second |
| `POST /v2/orders/` | 10 requests/second |

The placement rows are dedicated budgets: a `POST /v2/betslips/` or
`POST /v2/orders/` call counts only against its own limit, not the
general one. There are no daily caps, and these are the only rate
limits — no separate per-IP limit applies.

A rejected request gets a `429` `throttled` error with the wait in
`Retry-After` and `data.retry_after` (integer seconds).

The WebSocket stream has no message-rate limit; its connection-level
limits (registered-event cap, slow-reader disconnect) are documented
on the **Streaming API** endpoint.

Limits can be adjusted per account — contact support if your
integration needs more headroom.

## Currencies

Stake fields are `[currency, amount]` tuples. Stakes in responses are
always returned in **USDT**:

```json
["USDT", 115.38]
```

## Price ticks

All prices lie on a fixed tick schedule. The tick (the smallest step
between two valid prices) widens as the decimal price grows:

| Price (cents) | Decimal price | Tick |
|---------------|---------------|------|
| 50c - 99c     | 1.01 - 2      | 0.01 |
| 33.3c - 50c   | 2 - 3         | 0.02 |
| 25c - 33.3c   | 3 - 4         | 0.05 |
| 16.7c - 25c   | 4 - 6         | 0.10 |
| 10c - 16.7c   | 6 - 10        | 0.20 |
| 5c - 10c      | 10 - 20       | 0.50 |
| 3.3c - 5c     | 20 - 30       | 1    |
| 2c - 3.3c     | 30 - 50       | 2    |
| 1c - 2c       | 50 - 100      | 5    |
| 0.1c - 1c     | 100 - 1000    | 10   |

Cents are the implied probability of a price: `cents = 100 / decimal
price`. The band boundaries are exact in decimal price; the cents labels
are only approximate, so always match a price to its band by the decimal
value.

### Order prices

A price you submit on an order is snapped onto this schedule. An off-tick
price is rounded to the nearest valid tick that does not tighten your
limit: **down** for back (`for`) orders, **up** for lay (`against`)
orders. The snapped price is the one the order runs with and the one
reported back in the order response.

### Feed prices

Every price delivered on the stream (`price_list`, offers, pmms) is
already on the schedule, so a price quoted straight from the feed is
always valid and is never re-rounded.

## Sports & bet types

Every betslip, order and bet carries a `sport` and a `bet_type`. They
are short, opaque-looking strings whose grammar is described here.

### Sport codes

`sport` is a lowercase string. Current values:

| Code | Sport |
|------|-------|
| `fb` | Football, full 90 minutes |
| `fb_ht` | Football, first half only |
| `fb_2h` | Football, second half only |
| `fb_et` | Football, extra time only |
| `fb_corn` | Football corners (90 min) |
| `fb_corn_ht` | Football corners (1st half) |
| `fb_book` | Football yellow cards (90 min) |
| `fb_htft` | Football combined half-time / full-time result |
| `basket` | Basketball, full match |
| `basket_ht` | Basketball, first half |
| `basket_2h` | Basketball, second half |
| `basket_q1` | Basketball, 1st quarter |
| `basket_q2` | Basketball, 2nd quarter |
| `basket_q3` | Basketball, 3rd quarter |
| `basket_q4` | Basketball, 4th quarter |
| `tennis` | Tennis |
| `tt` | Table tennis |
| `ih` | Ice hockey |
| `af` | American football |
| `rl` | Rugby league |
| `ru` | Rugby union |
| `arf` | Australian rules football |
| `hand` | Handball |
| `volley` | Volleyball |
| `baseball` | Baseball |
| `cricket` | Cricket |
| `darts` | Darts |
| `snooker` | Snooker |
| `boxing` | Boxing |
| `mma` | Mixed martial arts |
| `golf` | Golf |
| `cycling` | Cycling |
| `moto` | Motorsport |
| `horse` | Horse racing |
| `dog` | Greyhound racing |
| `esports` | Esports |
| `politics` | Political markets |
| `specials` | Specials / novelty markets |

On accumulator (parlay) orders and betslips, `sport` is the literal
string `parlay` and the per-leg sport sits inside each `legs[]` entry.

Treat the table above as informational, not as a closed enum — new
sports are added over time. Do not hard-fail on unknown codes.

### Bet type grammar

`bet_type` is a comma-separated string. The first token is the
direction:

- `for` — back the outcome (you win if it happens).
- `against` — lay the outcome (you win if it doesn't).

The remaining tokens identify the market and its parameters.
**Handicaps always refer to the home team.**

**Asian handicap lines are integers equal to 4 × the actual line.**
This keeps the wire format integer-only across `0.25`-step lines:

| Wire integer | Real line |
|--------------|-----------|
| `0`  | 0.0 |
| `2`  | 0.5 |
| `7`  | 1.75 |
| `8`  | 2.0 |
| `-4` | -1.0 |
| `-21` | -5.25 |

### Markets

Match result:

| Bet type | Meaning |
|----------|---------|
| `for,h` / `for,d` / `for,a` | Home / Draw / Away win |
| `for,sd` | Score draw (any non-0–0 draw) |
| `for,win_90,h` | Home wins in 90 min (excluding extra time) |
| `for,dnb,h` | Home win, void if draw (draw-no-bet) |
| `for,hnb,a` | Away win, void if home wins (home-no-bet) |
| `for,anb,h` | Home win, void if away wins (away-no-bet) |
| `for,ml,h` | Moneyline — home wins, draw is void |
| `for,dc,h,d` | Double chance: home or draw |
| `for,uswin,h` | US-style home win (draw is half-stake split) |
| `for,awdw,h` | Asian win/draw/win |
| `for,ko,h` | Home team to kick off |
| `for,qualify,h` | Home team to qualify |

Goals (totals):

| Bet type | Meaning |
|----------|---------|
| `for,over,2.5` / `for,under,2.5` | Over/under non-integer line |
| `for,overeq,3` / `for,undereq,3` | Over/under integer line, inclusive |
| `for,exact_total,3` | Exactly 3 goals |
| `for,exact_total,3,inf` | 3 or more goals |
| `for,gr,1,3` | Goal range 1–3 inclusive (use `inf` for ∞) |
| `for,teamgr,h,0,2` | Home team scores 0–2 |
| `for,odd` / `for,even` | Total goals odd / even |
| `for,odd,h` / `for,even,a` | Per-team odd / even |

Asian handicaps (lines as 4 × the actual line):

| Bet type | Meaning |
|----------|---------|
| `for,ah,h,-4` | Asian handicap, home -1.0 |
| `for,ahover,7` / `for,ahunder,7` | Asian over/under 1.75 goals |
| `for,tahover,h,2` / `for,tahunder,a,2` | Team Asian over/under 0.5 goals |
| `for,eh,h,1` | English handicap, home +1 |

Correct score and margins:

| Bet type | Meaning |
|----------|---------|
| `for,cs,2,1` | Correct score 2–1 |
| `for,othercs,3,3` | Any score outside `home ≤ 3 AND away ≤ 3` |
| `for,othercs,1,1,3,3` | Any score outside both ranges |
| `for,wm,h,2,2` | Home wins by exactly 2 |
| `for,wm,h,2,inf` | Home wins by 2+ |
| `for,wmo,h,1,2.5` | Home wins by 1 + over 2.5 goals |
| `for,awm,1` | Absolute margin 1 (either side) |
| `for,wg,h,2` | Home wins and scores ≥ 2 |
| `for,quatro,h,o,2.5` | Home wins AND over 2.5 goals |
| `for,moou,h,over,2.5` | Match-result + over/under combo |
| `for,mo_both_score,h,yes` | Home wins AND both teams score |
| `for,aou,h,3` | Betfair "any other unquoted", home, max draw at 3–3 |

Score / clean sheet:

| Bet type | Meaning |
|----------|---------|
| `for,score,both,yes` / `for,score,both,no` | Both teams (don't) score |
| `for,score,either` / `for,score,neither` / `for,score,one` | Score patterns |
| `for,score,h,yes` / `for,score,h,no` | Home (does not) score |
| `for,clean,h` | Home clean sheet |
| `for,clean,both` / `for,clean,either` / `for,clean,neither` / `for,clean,one` | Clean-sheet patterns |
| `for,fg,no_goal` | No goals (first goal markets) |
| `for,swm,no_goal` / `for,swm,sd` | Score-and-margin: no goal / score draw |

### Tennis

Tennis bet types include a period and a void rule:

```
for,tset,<period>,<void_rule>,<unit>[,<market>,<args>...]
```

- `<period>` — `1`–`5` (a specific set) or `all` (whole match).
- `<void_rule>` — `vwhole`, `vsetN`, `vgameN` — when the bet voids
  if a player retires.
- `<unit>` — `set` or `game`, optionally followed by a market and args.

Examples:

- `for,tset,all,vset1,p1` — player 1 to win the match (voids unless
  set 1 completes).
- `for,tset,1,vwhole,p1` — player 1 to win set 1.
- `for,tset,all,vwhole,game,ahover,62` — total games in the match
  over 15.5.

### Time-period sports (other than tennis)

Bets on a specific period of a match use one of these tokens:

| Token | Meaning |
|-------|---------|
| `tp,<period>` | Generic period — `<period>` is `all`, `reg`, or `1`–`9` |
| `tperiod,<n>` | Specific period (e.g. ice hockey, hand-ball) |
| `thalf,<n>` | First or second half |
| `tquarter,<n>` | Quarter (basketball, NFL) |
| `tinnings,<n>` | Inning (baseball) — `<n>` is integer or `all` |
| `tmap,<n>` | Map (esports) — `<n>` is `1`–`5` |

The token is followed by an optional `sub,<subsport>` modifier
(used for things like darts 180-counts) and then the regular
market and its arguments:

```
for,<period_token>[,sub,<subsport>],<market>[,<args>...]
```

Examples:

- `for,tp,all,ahunder,16` — total under 4.0 across all periods.
- `for,thalf,1,ah,h,0` — Asian handicap, home 0.0, in the first half.
- `for,tquarter,2,wdw,h` — home to win the second quarter.
- `for,tmap,1,ahover,42` — esports, total kills on map 1 over 10.5.
- `for,tp,all,sub,180,ahover,8` — darts, over 2.0 180s.

The legacy aliases `tall`, `treg`, `tp1`, `tp2`, … are no longer
accepted; use the tokens above.

### Multirunner (outright) events

For events with many runners (horse racing, golf, etc.):

- `for,win,<team_id>` — runner to win outright.
- `for,top,<n>,<team_id>` — runner to finish in the top `<n>`
  (e.g. `for,top,3,1042` means runner 1042 to place top-3).

### Validating a bet type

Call `GET /v2/sports/{sport}/bet_types/{bet_type}/` with the
candidate string. A 200 response includes a human-readable
`bet_type_description` and the win/loss payoff grid; a 400
(`invalid_bet_type`) means the string did not parse. This endpoint
cannot validate multirunner bet types (`for,win,...`, `for,top,...`);
they need an event context and always return the 400.

## Idempotency

`POST /v2/orders/` accepts an optional `request_uuid`. Retrying the same
request with the same UUID will not create a duplicate order, and the
order can be retrieved by UUID from `GET /v2/orders/tracked/{uuid}/` for
several days after placement (until the order is purged upstream).

## Machine-readable docs

The canonical OpenAPI spec is served at:

- `GET /v2/openapi.json` (parsed JSON)
- `GET /v2/openapi.yaml` (raw YAML)

Both URLs return the same schema rendered on this page.

For LLMs and coding agents:

- `GET /llms.txt` — index of machine-readable documentation
  ([llms.txt](https://llmstxt.org/) format)
- `GET /docs.md` (alias `GET /llms-full.txt`) — this entire reference as a
  single Markdown document
- `GET /docs` with an `Accept: text/markdown` header returns the Markdown
  reference instead of HTML

All paths also work behind the `/magic-api` ingress prefix
(e.g. `/magic-api/llms.txt`, `/magic-api/v2/openapi.yaml`).



## Path Table

| Method | Path | Description |
| --- | --- | --- |
| GET | [/v2/betslips/](#getv2betslips) | List betslips |
| POST | [/v2/betslips/](#postv2betslips) | Create betslip |
| GET | [/v2/betslips/{betslip_id}/](#getv2betslipsbetslip_id) | Get betslip |
| POST | [/v2/betslips/{betslip_id}/refresh/](#postv2betslipsbetslip_idrefresh) | Refresh betslip |
| GET | [/v2/orders/](#getv2orders) | List orders |
| POST | [/v2/orders/](#postv2orders) | Create order |
| GET | [/v2/orders/updates/](#getv2ordersupdates) | Order updates |
| GET | [/v2/orders/{order_id}/](#getv2ordersorder_id) | Get order |
| GET | [/v2/orders/tracked/{uuid}/](#getv2orderstrackeduuid) | Get order by UUID |
| POST | [/v2/orders/{order_id}/close/](#postv2ordersorder_idclose) | Close an order |
| POST | [/v2/orders/close_many/](#postv2ordersclose_many) | Close multiple orders |
| POST | [/v2/orders/close_all/](#postv2ordersclose_all) | Close all open orders |
| GET | [/v2/orders/position/](#getv2ordersposition) | Calculate position |
| GET | [/v2/balance/](#getv2balance) | Account balance |
| POST | [/v2/heartbeats/](#postv2heartbeats) | Create heartbeat |
| GET | [/v2/heartbeats/](#getv2heartbeats) | List heartbeats |
| GET | [/v2/heartbeats/{heartbeat_id}/](#getv2heartbeatsheartbeat_id) | Get heartbeat |
| DELETE | [/v2/heartbeats/{heartbeat_id}/](#deletev2heartbeatsheartbeat_id) | Cancel heartbeat |
| POST | [/v2/heartbeats/{heartbeat_id}/refresh/](#postv2heartbeatsheartbeat_idrefresh) | Refresh heartbeat |
| GET | [/v2/xrates/](#getv2xrates) | Exchange rates |
| GET | [/v2/sports/{sport}/bet_types/{bet_type}/](#getv2sportssportbet_typesbet_type) | Bet type info |
| GET | [/v2/stream](#getv2stream) | WebSocket stream |

## Reference Table

| Name | Path | Description |
| --- | --- | --- |
| ApiKeyHeader | [#/components/securitySchemes/ApiKeyHeader](#componentssecurityschemesapikeyheader) | API key created from the magic-markets website |
| Error400 | [#/components/responses/Error400](#componentsresponseserror400) | Validation failed — see `data.validation_errors`. |
| Error401 | [#/components/responses/Error401](#componentsresponseserror401) | Authentication failed — API key missing, malformed, or rejected. |
| Error403 | [#/components/responses/Error403](#componentsresponseserror403) | Authorization failed — the key is valid but cannot perform this action. |
| Error404 | [#/components/responses/Error404](#componentsresponseserror404) | Resource not found or not visible to this key. |
| Error429 | [#/components/responses/Error429](#componentsresponseserror429) | Rate limit hit. Honour the `Retry-After` header. |
| Error500 | [#/components/responses/Error500](#componentsresponseserror500) | Unexpected internal error. Quote `data[1]` (the token) when contacting support. |
| StakeTuple | [#/components/schemas/StakeTuple](#componentsschemasstaketuple) | [currency, amount] — e.g. ["USDT", 115.38] |
| ErrorEnvelope | [#/components/schemas/ErrorEnvelope](#componentsschemaserrorenvelope) | Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses. |
| PriceLevel | [#/components/schemas/PriceLevel](#componentsschemaspricelevel) |  |
| BetslipCreateResponse | [#/components/schemas/BetslipCreateResponse](#componentsschemasbetslipcreateresponse) | Create (POST) response; carries no prices. Poll GET or watch the stream for the quote. |
| BetslipResponse | [#/components/schemas/BetslipResponse](#componentsschemasbetslipresponse) |  |
| BetslipCreateEnvelope | [#/components/schemas/BetslipCreateEnvelope](#componentsschemasbetslipcreateenvelope) |  |
| BetslipEnvelope | [#/components/schemas/BetslipEnvelope](#componentsschemasbetslipenvelope) |  |
| BetslipListEnvelope | [#/components/schemas/BetslipListEnvelope](#componentsschemasbetsliplistenvelope) |  |
| BetslipCreateRequest | [#/components/schemas/BetslipCreateRequest](#componentsschemasbetslipcreaterequest) |  |
| EventResultMatch | [#/components/schemas/EventResultMatch](#componentsschemaseventresultmatch) | Football and other two-half sports. `ht_*` are the half-time score and are omitted for single-period sports; `ft_*` are the full-time score. |
| EventResultTennis | [#/components/schemas/EventResultTennis](#componentsschemaseventresulttennis) | Tennis. `setN_pM` is player M's game count in set N. |
| EventResultHockey | [#/components/schemas/EventResultHockey](#componentsschemaseventresulthockey) | Ice hockey. `tpN_*` are the three period scores; `tall_*` the regulation total; `pen_*` the penalty-shootout score. |
| EventResultTableTennis | [#/components/schemas/EventResultTableTennis](#componentsschemaseventresulttabletennis) | Table tennis. `gameN_*` is the point score in game N (up to 7 games). |
| EventResultMultirunner | [#/components/schemas/EventResultMultirunner](#componentsschemaseventresultmultirunner) | Multirunner (outright) events. |
| EventInfo | [#/components/schemas/EventInfo](#componentsschemaseventinfo) |  |
| ParlayLegList | [#/components/schemas/ParlayLegList](#componentsschemasparlayleglist) |  |
| ParlayLeg | [#/components/schemas/ParlayLeg](#componentsschemasparlayleg) |  |
| BetResponse | [#/components/schemas/BetResponse](#componentsschemasbetresponse) | Individual bet within an order |
| OrderResponse | [#/components/schemas/OrderResponse](#componentsschemasorderresponse) |  |
| OrderEnvelope | [#/components/schemas/OrderEnvelope](#componentsschemasorderenvelope) |  |
| OrderListEnvelope | [#/components/schemas/OrderListEnvelope](#componentsschemasorderlistenvelope) |  |
| OrderCreateRequest | [#/components/schemas/OrderCreateRequest](#componentsschemasordercreaterequest) |  |
| BalanceResponse | [#/components/schemas/BalanceResponse](#componentsschemasbalanceresponse) |  |
| BalanceEnvelope | [#/components/schemas/BalanceEnvelope](#componentsschemasbalanceenvelope) |  |
| HeartbeatResponse | [#/components/schemas/HeartbeatResponse](#componentsschemasheartbeatresponse) |  |
| HeartbeatEnvelope | [#/components/schemas/HeartbeatEnvelope](#componentsschemasheartbeatenvelope) |  |
| HeartbeatListEnvelope | [#/components/schemas/HeartbeatListEnvelope](#componentsschemasheartbeatlistenvelope) | List response. Note the data is wrapped under a `heartbeats` key, unlike other list endpoints which return a flat array. |
| HeartbeatCancelEnvelope | [#/components/schemas/HeartbeatCancelEnvelope](#componentsschemasheartbeatcancelenvelope) | Cancel response. `data` is always `null`. |
| XRate | [#/components/schemas/XRate](#componentsschemasxrate) |  |
| XRatesEnvelope | [#/components/schemas/XRatesEnvelope](#componentsschemasxratesenvelope) |  |
| BetTypeInfoResponse | [#/components/schemas/BetTypeInfoResponse](#componentsschemasbettypeinforesponse) |  |
| BetTypeInfoEnvelope | [#/components/schemas/BetTypeInfoEnvelope](#componentsschemasbettypeinfoenvelope) |  |
| PositionGrid | [#/components/schemas/PositionGrid](#componentsschemaspositiongrid) | Profit or loss per scoreline, in USDT: `values[home_score][away_score]`. The grid is square and its size depends on the sport. |
| PositionComponentTotal | [#/components/schemas/PositionComponentTotal](#componentsschemaspositioncomponenttotal) | Position for a standard bet type. |
| PositionCustomBetTotal | [#/components/schemas/PositionCustomBetTotal](#componentsschemaspositioncustombettotal) | Position for a custom bet type, which carries its own grid instead of prices. |
| PositionCashoutInfo | [#/components/schemas/PositionCashoutInfo](#componentsschemaspositioncashoutinfo) | Cashout valuation for the position. Present on every non-multirunner position. Cashout is offered on football only; on every other sport, and whenever cashout is otherwise unavailable, `allowed` is false and every value below is null. |
| PositionResponse | [#/components/schemas/PositionResponse](#componentsschemaspositionresponse) | Aggregate profit/loss position over the filtered orders. `sport`, `event_id` and `event_info` are present once the filters match at least one order. |
| PositionEnvelope | [#/components/schemas/PositionEnvelope](#componentsschemaspositionenvelope) |  |

## Path Details

***

### [GET]/v2/betslips/

- Summary  
List betslips

- Description  
Returns all open betslip IDs for the authenticated customer.

#### Responses

- 200 List of betslip IDs

`application/json`

```typescript
{
  status?: enum[ok]
  data?: string[]
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [POST]/v2/betslips/

- Summary  
Create betslip

- Description  
Create a new betslip. For normal/lay bets supply `sport`, `event_id`, and `bet_type`. For parlays supply a `legs` array instead.  
  
The response carries **no prices**: quotes are gathered asynchronously. Poll `GET /v2/betslips/{betslip_id}/` until `price_list` populates (typically a couple of seconds; watch `expiry_ts`), or read the quote off the stream.

#### RequestBody

- application/json

```typescript
{
  // Sport code (required for normal/lay) — see "Sports & bet types" in the introduction.
  sport?: string
  // Event ID (required for normal/lay)
  event_id?: string
  // Bet type string (required for normal/lay) — see "Sports & bet types" in the introduction.
  bet_type?: string
  legs: {
    sport: string
    event_id: string
    bet_type: string
  }[]
  betslip_type?: enum[normal, lay, parlay] //default: normal
  equivalent_bets?: boolean //default: true
  user_data?: string | null
  // When true, only liquidity sources that do not hold bets in danger status are used. When false or omitted, all available liquidity sources are used.
  exclude_danger?: boolean
}
```

#### Responses

- 201 Betslip created

`application/json`

```typescript
{
  status?: enum[ok]
  // Create (POST) response; carries no prices. Poll GET or watch the stream for the quote.
  data: {
    betslip_id?: string
    // Sport code — see "Sports & bet types" in the introduction.
    sport?: string
    // Event identifier, e.g. 2026-06-15,1001,2002
    event_id?: string
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    // Human-readable label, e.g. Home, Over 1.5 (Asian)
    bet_type_description?: string
    // Unix timestamp when the betslip expires
    expiry_ts?: number
    is_open?: boolean
    close_reason?: string | null
    // Whether equivalent bets are included
    equivalent_bets?: boolean
    customer_username?: string
    // Customer's base currency code
    customer_ccy?: string
    betslip_type?: enum[normal, lay, parlay]
    // Parlay legs (only present for parlay betslips)
    legs?: #/components/schemas/ParlayLegList | null
    user_data?: string | null
  }
}
```

- Examples

  - normal

```json
{
  "summary": "Normal bet",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "bs-spread-002",
      "sport": "fb",
      "event_id": "2026-06-15,3003,4004",
      "bet_type": "for,ahover,7",
      "bet_type_description": "Over 1.5 (Asian)",
      "expiry_ts": 1781234999,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": false,
      "customer_username": "user1",
      "customer_ccy": "USDT",
      "betslip_type": "normal",
      "user_data": null
    }
  }
}
```

  - parlay

```json
{
  "summary": "Parlay — accumulator",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "0e644fdcce7f45c0997427410703efb6",
      "sport": "parlay",
      "event_id": "",
      "bet_type": "parlay",
      "bet_type_description": "Karlsruher SC AND Draw AND SV Darmstadt 1898 AND Anastasia Potapova",
      "expiry_ts": 1775837930.718644,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": false,
      "customer_username": "user3",
      "customer_ccy": "USDT",
      "betslip_type": "parlay",
      "user_data": null,
      "legs": [
        {
          "id": 1,
          "sport": "fb",
          "event_id": "2026-04-10,224,204",
          "bet_type": "for,h",
          "bet_type_description": "Karlsruher SC",
          "price": null,
          "outcome": null
        },
        {
          "id": 2,
          "sport": "fb",
          "event_id": "2026-04-11,1027,211",
          "bet_type": "for,d",
          "bet_type_description": "Draw",
          "price": null,
          "outcome": null
        },
        {
          "id": 3,
          "sport": "fb",
          "event_id": "2026-04-11,1080,205",
          "bet_type": "for,h",
          "bet_type_description": "SV Darmstadt 1898",
          "price": null,
          "outcome": null
        },
        {
          "id": 4,
          "sport": "tennis",
          "event_id": "2026-04-10,80623,10056707",
          "bet_type": "for,tset,all,vset1,p1",
          "bet_type_description": "Anastasia Potapova",
          "price": null,
          "outcome": null
        }
      ]
    }
  }
}
```

- 400 Validation failed

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- Examples

  - invalid_bet_type

```json
{
  "summary": "bet_type did not parse",
  "value": {
    "status": "error",
    "code": "validation_error",
    "data": {
      "validation_errors": {
        "bet_type": [
          "invalid_bet_type"
        ]
      }
    }
  }
}
```

  - event_not_found

```json
{
  "summary": "Event does not exist",
  "value": {
    "status": "error",
    "code": "validation_error",
    "data": {
      "validation_errors": {
        "non_field_errors": [
          "event_not_found"
        ]
      }
    }
  }
}
```

- 401 undefined

- 403 Per-customer open-betslip cap exceeded

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [GET]/v2/betslips/{betslip_id}/

- Summary  
Get betslip

- Description  
Returns a single betslip with prices and stakes in USDT. `price_list` may be empty until quotes arrive, or when nothing is currently quoting the selection.

#### Responses

- 200 Betslip with prices

`application/json`

```typescript
{
  status?: enum[ok]
  data?: #/components/schemas/BetslipCreateResponse & {
     price_list: {
       effective: {
         // Decimal price
         price?: number
         // Minimum stake at this price level, or null
         min?: #/components/schemas/StakeTuple | null
         // Maximum stake at this price level, or null
         max?: #/components/schemas/StakeTuple | null
       }
     }[]
     // Sum of max stakes across all price levels, or null if no prices
     total?: #/components/schemas/StakeTuple | null
   }
}
```

- Examples

  - normal

```json
{
  "summary": "Normal — multiple price levels",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "bs-spread-002",
      "sport": "fb",
      "event_id": "2026-06-15,3003,4004",
      "bet_type": "for,ahover,7",
      "bet_type_description": "Over 1.5 (Asian)",
      "expiry_ts": 1781234999,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": false,
      "customer_username": "user1",
      "customer_ccy": "USDT",
      "betslip_type": "normal",
      "price_list": [
        {
          "effective": {
            "price": 7.4,
            "min": [
              "USDT",
              5.769
            ],
            "max": [
              "USDT",
              92.304
            ]
          }
        },
        {
          "effective": {
            "price": 4.2,
            "min": [
              "USDT",
              3.4614
            ],
            "max": [
              "USDT",
              173.07
            ]
          }
        },
        {
          "effective": {
            "price": 2.1,
            "min": null,
            "max": [
              "USDT",
              346.14
            ]
          }
        }
      ],
      "total": [
        "USDT",
        611.514
      ],
      "user_data": null
    }
  }
}
```

  - normal_single_level

```json
{
  "summary": "Normal — single price level",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "bs-single-001",
      "sport": "fb",
      "event_id": "2026-06-15,1001,2002",
      "bet_type": "for,h",
      "bet_type_description": "Home",
      "expiry_ts": 1781234567,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": true,
      "customer_username": "user1",
      "customer_ccy": "USDT",
      "betslip_type": "normal",
      "price_list": [
        {
          "effective": {
            "price": 3.25,
            "min": [
              "USDT",
              3.4614
            ],
            "max": [
              "USDT",
              519.21
            ]
          }
        }
      ],
      "total": [
        "USDT",
        519.21
      ],
      "user_data": null
    }
  }
}
```

  - lay

```json
{
  "summary": "Lay — betting against",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "e54ac4c325c64ba2a5eefc08b3408b49",
      "sport": "fb",
      "event_id": "2026-03-17,22,328",
      "bet_type": "against,h",
      "bet_type_description": "Home",
      "expiry_ts": 1789700000,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": false,
      "customer_username": "user2",
      "customer_ccy": "USDT",
      "betslip_type": "lay",
      "price_list": [
        {
          "effective": {
            "price": 2.26,
            "min": [
              "USDT",
              0.1128
            ],
            "max": [
              "USDT",
              10685.0521
            ]
          }
        },
        {
          "effective": {
            "price": 2.24,
            "min": [
              "USDT",
              0.1128
            ],
            "max": [
              "USDT",
              5191.5088
            ]
          }
        },
        {
          "effective": {
            "price": 2.22,
            "min": [
              "USDT",
              0.1128
            ],
            "max": [
              "USDT",
              1833.0947
            ]
          }
        }
      ],
      "total": [
        "USDT",
        17709.6556
      ],
      "user_data": null
    }
  }
}
```

  - parlay

```json
{
  "summary": "Parlay — accumulator",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "0e644fdcce7f45c0997427410703efb6",
      "sport": "parlay",
      "event_id": "",
      "bet_type": "parlay",
      "bet_type_description": "Karlsruher SC AND Draw AND SV Darmstadt 1898 AND Anastasia Potapova",
      "expiry_ts": 1775837930.718644,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": false,
      "customer_username": "user3",
      "customer_ccy": "USDT",
      "betslip_type": "parlay",
      "price_list": [],
      "total": null,
      "legs": [
        {
          "id": 1,
          "sport": "fb",
          "event_id": "2026-04-10,224,204",
          "bet_type": "for,h",
          "bet_type_description": "Karlsruher SC",
          "price": null,
          "outcome": null
        },
        {
          "id": 2,
          "sport": "fb",
          "event_id": "2026-04-11,1027,211",
          "bet_type": "for,d",
          "bet_type_description": "Draw",
          "price": null,
          "outcome": null
        },
        {
          "id": 3,
          "sport": "fb",
          "event_id": "2026-04-11,1080,205",
          "bet_type": "for,h",
          "bet_type_description": "SV Darmstadt 1898",
          "price": null,
          "outcome": null
        },
        {
          "id": 4,
          "sport": "tennis",
          "event_id": "2026-04-10,80623,10056707",
          "bet_type": "for,tset,all,vset1,p1",
          "bet_type_description": "Anastasia Potapova",
          "price": null,
          "outcome": null
        }
      ]
    }
  }
}
```

  - no_prices

```json
{
  "summary": "Quotes not yet arrived (poll again)",
  "value": {
    "status": "ok",
    "data": {
      "betslip_id": "bs-empty-005",
      "sport": "fb",
      "event_id": "2026-08-10,9009,1010",
      "bet_type": "for,a",
      "bet_type_description": "Away",
      "expiry_ts": 1783000000,
      "is_open": true,
      "close_reason": null,
      "equivalent_bets": false,
      "customer_username": "user4",
      "customer_ccy": "USDT",
      "betslip_type": "normal",
      "price_list": [],
      "total": null,
      "user_data": null
    }
  }
}
```

- 401 undefined

- 403 undefined

- 404 Betslip not found or not visible to this key

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [POST]/v2/betslips/{betslip_id}/refresh/

- Summary  
Refresh betslip

- Description  
Extend the betslip expiration timeout.

#### Responses

- 200 Betslip expiration extended

`application/json`

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/orders/

- Summary  
List orders

- Description  
Returns a paginated list of orders for the authenticated customer.

#### Parameters(Query)

```typescript
page?: integer //default: 1
```

```typescript
page_size?: integer //default: 25
```

```typescript
status?: string[]
```

```typescript
sport?: string[]
```

```typescript
event_id?: string[]
```

```typescript
order_type?: string[]
```

```typescript
date_from?: string
```

```typescript
date_to?: string
```

```typescript
search?: string
```

#### Responses

- 200 Paginated order list

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }[]
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [POST]/v2/orders/

- Summary  
Create order

- Description  
Places a new order on an existing betslip.

#### RequestBody

- application/json

```typescript
{
  betslip_id: string
  // Desired decimal price. Off-tick prices are rounded down for back (`for`) orders and up for lay (`against`) orders; see "Price ticks".
  price: number
  // #/components/schemas/StakeTuple
  stake?: string | number[]
  // Order duration in seconds (default 15)
  duration?: number
  // How the order interacts with the exchange. `make_and_take`: fill against the best available liquidity first; any remaining stake is advertised on the exchange at your price, while the order keeps taking newly available liquidity. `take_only`: only consume available liquidity; remaining stake is never advertised and other orders cannot match against it. `dark`: like `make_and_take`, but the advertised remaining stake is hidden from other customers - they can still match it when their price crosses yours, but they cannot see your price until then. There is no post-only mode: no order type rests in the book without also taking crossing liquidity.
  exchange_mode?: enum[make_and_take, take_only, dark] //default: make_and_take
  // Keep order open when event goes in-play
  keep_open_ir?: boolean
  user_data?: string | null
  // Idempotency key
  request_uuid?: string
  accept_partial_fill?: boolean //default: true
  accept_better_price?: boolean //default: true
  force_want_price?: boolean
  // `dark` orders only (rejected on other modes): the minimum total stake another order must request to be allowed to match yours at your price point. Cannot exceed the order's own stake. Set it non-zero to stop small probe orders from discovering your price.
  min_taker_want_stake?: #/components/schemas/StakeTuple | null
  // Placement-time score assertion, `[home, away]`. When present, the order is rejected with `validation_error` / `non_field_errors: ["event_scores_dont_match"]` unless the value matches the live score the exchange holds for the event - use it to avoid placing on a price that has not reacted to a goal yet. Only meaningful for football: for sports without a goal-style running score, and while no score is known yet, the server assumes `[0, 0]` and rejects any other value. Omit the field to skip the check.
  current_score?: integer[]
  // When true, only liquidity sources that do not hold bets in danger status are used. When false or omitted, all available liquidity sources are used.
  exclude_danger?: boolean
  // Optional per-source minimum stakes, keyed by source. A source is only used if it can take at least its minimum. Values are `[currency, amount]` tuples.
  bookie_min_stakes: {
  }
  // Optional caller-supplied tag recorded against the order.
  placer_type?: string | null
}
```

#### Responses

- 201 Order created

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }
}
```

- 400 Validation failed

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 401 undefined

- 403 undefined

- 409 Idempotency conflict — `request_uuid` already used

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [GET]/v2/orders/updates/

- Summary  
Order updates

- Description  
Returns orders updated within the given time range. Both `updated_at_from` and `updated_at_to` must be **at least 60 seconds in the past**, and the window (`updated_at_to − updated_at_from`) must not exceed **70 minutes**. For longer syncs, page through successive 70-minute windows.

#### Parameters(Query)

```typescript
updated_at_from: string
```

```typescript
updated_at_to: string
```

#### Responses

- 200 Recently updated orders

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }[]
}
```

- 400 Time window violates the 60-second / 70-minute rules

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- Examples

  - too_recent

```json
{
  "summary": "Timestamp is not at least 60 seconds in the past",
  "value": {
    "status": "error",
    "code": "validation_error",
    "data": {
      "validation_errors": {
        "updated_at_from": [
          "updated_at_from too recent"
        ],
        "updated_at_to": [
          "updated_at_to too recent"
        ]
      }
    }
  }
}
```

  - window_too_wide

```json
{
  "summary": "Window exceeds 70 minutes",
  "value": {
    "status": "error",
    "code": "validation_error",
    "data": {
      "validation_errors": {
        "non_field_errors": [
          "The date range cannot exceed 70 minutes"
        ]
      }
    }
  }
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/orders/{order_id}/

- Summary  
Get order

- Description  
Returns a single order by ID with all stakes in USDT.

#### Responses

- 200 Single order

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }
}
```

- 401 undefined

- 403 undefined

- 404 Order not found or not visible to this key

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [GET]/v2/orders/tracked/{uuid}/

- Summary  
Get order by UUID

- Description  
Retrieve an order using the `request_uuid` from order creation instead of the order ID. Available for several days after placement (until the order is purged upstream).

#### Responses

- 200 Order found

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }
}
```

- 400 The path value is not a valid UUID

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 401 undefined

- 403 undefined

- 404 No order with this `request_uuid`

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [POST]/v2/orders/{order_id}/close/

- Summary  
Close an order

- Description  
Close (cancel) a single open order. The order's lifecycle update is delivered on the WebSocket as an `["order", …]` entry with `closed: true` and `close_reason: "cancelled"`. The response `data` field is always `null`.

#### Responses

- 200 Order closed.

`application/json`

- 400 The order exists but is already closed or settled (`order_closed`).

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 401 undefined

- 403 undefined

- 404 The order id is unknown (`not_found`).

- 429 undefined

- 500 undefined

***

### [POST]/v2/orders/close_many/

- Summary  
Close multiple orders

- Description  
Close multiple orders synchronously. Maximum 500 order IDs per request.

#### RequestBody

- application/json

```typescript
{
  order_ids?: integer[]
}
```

#### Responses

- 200 Close results. `closed` and `not_found` are each a list of order-id lists (grouped by the upstream batch); already-closed and unknown ids both land in `not_found`.

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    closed?: integer[][]
    not_found?: integer[][]
  }
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [POST]/v2/orders/close_all/

- Summary  
Close all open orders

- Description  
Request cancellation of all open orders. Optionally filter by sport and/or event.

#### RequestBody

- application/json

```typescript
{
  // Only close orders on this sport — see "Sports & bet types" in the introduction.
  sport?: string
  // Only close orders on this event (requires sport)
  event_id?: string
}
```

#### Responses

- 202 Cancellation accepted. `data` is a list of the order-id lists being cancelled; an empty result is `[[]]`.

`application/json`

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/orders/position/

- Summary  
Calculate position

- Description  
Calculate profit/loss position based on filtered orders. Accepts the same query parameters as the list orders endpoint.

#### Parameters(Query)

```typescript
status?: string[]
```

```typescript
sport?: string[]
```

```typescript
event_id?: string[]
```

```typescript
order_type?: string[]
```

```typescript
date_from?: string
```

```typescript
date_to?: string
```

```typescript
search?: string
```

```typescript
include_cashout_info?: boolean
```

#### Responses

- 200 Position calculation

`application/json`

```typescript
{
  status?: enum[ok]
  // Aggregate profit/loss position over the filtered orders. `sport`, `event_id` and `event_info` are present once the filters match at least one order.
  data: {
    // Payoff from the matched bets, or null when every cell is zero
    payoff_grid: #/components/schemas/PositionGrid | null
    // Position per bet type, keyed by the unflipped bet type string
    totals: {
    }
    // Number of bets that could not be projected onto the grid
    unknown_bets_num: integer
    // Payoff from the bets counted in `unknown_bets_num`
    unknown_grid: #/components/schemas/PositionGrid | null
    sport?: string
    event_id?: string
    event_info: {
      event_type?: enum[normal, multirunner, parlay]
      // null for parlay orders
      event_id?: string | null
      event_name?: string | null
      // Normal events only
      home_id?: integer | null
      home_team?: string | null
      // Normal events only
      away_id?: integer | null
      away_team?: string | null
      competition_id?: integer | null
      competition_name?: string | null
      // ISO country code (e.g. XE for England)
      competition_country?: string | null
      start_time?: string | null
      date?: string | null
      // Match or race result. The shape depends on the sport - see the `EventResult*` schemas. Null while the result is unknown.
      result?: #/components/schemas/EventResultMatch | #/components/schemas/EventResultTennis | #/components/schemas/EventResultHockey | #/components/schemas/EventResultTableTennis | #/components/schemas/EventResultMultirunner | null
      // Runner list (multirunner events only)
      teams?: {
         team_id?: integer
         name?: string
       }[] | null
      // Race end time (multirunner events only)
      end_time?: string | null
      // Sub-event info for each leg (parlay orders only)
      leg_event_infos?: array | null
    }
    // Cashout valuation for the position. Present on every non-multirunner position. Cashout is offered on football only; on every other sport, and whenever cashout is otherwise unavailable, `allowed` is false and every value below is null.
    cashout_info: {
      allowed: boolean
      // Why cashout is unavailable, when it can be shared: `position_already_flat` or `insufficient_credit`.
      reason: string | null
      // Amount offered to close the position now
      valuation: #/components/schemas/StakeTuple | null
      // Stake currently at risk on the position
      stake: #/components/schemas/StakeTuple | null
      // Change in smart credit that taking the cashout would cause
      smart_credit_delta: #/components/schemas/StakeTuple | null
      // Payoff grid the cashout is valued against
      position: #/components/schemas/PositionGrid | null
    }
  }
}
```

- 400 The filters match orders from more than one event or sport

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/balance/

- Summary  
Account balance

- Description  
Returns the authenticated user's current balance, total stake on open bets, and smart credit. All values are `["USDT", amount]` tuples. The same three figures are pushed on the stream as the `balance` message; `open_stake` is positive.

#### Responses

- 200 Account balance

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    // #/components/schemas/StakeTuple
    balance?: string | number[]
    open_stake:#/components/schemas/StakeTuple
    // Smart credit, or null when the account has none
    smart_credit: #/components/schemas/StakeTuple | null
  }
}
```

- Examples

  - with_smart_credit

```json
{
  "summary": "Account with smart credit",
  "value": {
    "status": "ok",
    "data": {
      "balance": [
        "USDT",
        10000.1
      ],
      "open_stake": [
        "USDT",
        152.55
      ],
      "smart_credit": [
        "USDT",
        1000
      ]
    }
  }
}
```

  - without_smart_credit

```json
{
  "summary": "Account without smart credit",
  "value": {
    "status": "ok",
    "data": {
      "balance": [
        "USDT",
        10000.1
      ],
      "open_stake": [
        "USDT",
        152.55
      ],
      "smart_credit": null
    }
  }
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [POST]/v2/heartbeats/

- Summary  
Create heartbeat

- Description  
Open a new heartbeat timer. If the timer expires before being refreshed or cancelled, every open order on the account is closed, including orders placed by other sessions or API keys; only orders created after the expiry instant are spared. Closing cancels the unfilled stake and withdraws unmatched liquidity advertised on the exchange; bets that already matched are unaffected. Expiry is evaluated server-side about once per second.

#### RequestBody

- application/json

```typescript
{
  // Seconds before the heartbeat expires
  timeout: integer
}
```

#### Responses

- 200 Heartbeat created

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    heartbeat_id?: string
    expiry_time?: string
  }
}
```

- 400 `timeout` outside the 10–300 second range

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/heartbeats/

- Summary  
List heartbeats

- Description  
Returns all currently open heartbeats. **Note:** unlike other list endpoints (`/v2/orders/`, `/v2/betslips/`) which return a flat array under `data`, this endpoint wraps the array under `data.heartbeats` for historical reasons. Clients must special-case this shape.

#### Responses

- 200 Open heartbeats

`application/json`

```typescript
// List response. Note the data is wrapped under a `heartbeats` key, unlike other list endpoints which return a flat array.
{
  status?: enum[ok]
  data: {
    heartbeats: {
      heartbeat_id?: string
      expiry_time?: string
    }[]
  }
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/heartbeats/{heartbeat_id}/

- Summary  
Get heartbeat

- Description  
Returns information about a single heartbeat.

#### Responses

- 200 Heartbeat info

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    heartbeat_id?: string
    expiry_time?: string
  }
}
```

- 401 undefined

- 403 undefined

- 404 Heartbeat not found or not visible to this key

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [DELETE]/v2/heartbeats/{heartbeat_id}/

- Summary  
Cancel heartbeat

- Description  
Cancel an active heartbeat. Cancelling disarms the timer without closing any orders; only expiry triggers the order close-out. The response `data` field is always `null`.

#### Responses

- 200 Heartbeat cancelled

`application/json`

```typescript
// Cancel response. `data` is always `null`.
{
  status?: enum[ok]
  data?: null
}
```

- 401 undefined

- 403 undefined

- 404 Heartbeat not found or already cancelled

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [POST]/v2/heartbeats/{heartbeat_id}/refresh/

- Summary  
Refresh heartbeat

- Description  
Extend the heartbeat expiration timeout. A heartbeat that has already expired cannot be refreshed - its close-out has been triggered; open a new heartbeat instead.

#### Responses

- 200 Heartbeat extended

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    heartbeat_id?: string
    expiry_time?: string
  }
}
```

- 401 undefined

- 403 undefined

- 404 Heartbeat not found, expired, or cancelled

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 429 undefined

- 500 undefined

***

### [GET]/v2/xrates/

- Summary  
Exchange rates

- Description  
Returns current exchange rates.

#### Responses

- 200 Exchange rate list

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    // Currency code (e.g. USD, EUR, USDT)
    ccy?: string
    // Exchange rate to USDT
    rate?: number
  }[]
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/sports/{sport}/bet_types/{bet_type}/

- Summary  
Bet type info

- Description  
Returns information about a bet type including the win/loss payout grid.

#### Parameters(Query)

```typescript
home_team?: string
```

```typescript
away_team?: string
```

#### Responses

- 200 Bet type information

`application/json`

```typescript
{
  status?: enum[ok]
  data: {
    // Display name for the sport (e.g. "Football")
    sport?: string
    // Human-readable bet type label
    bet_type_description?: string
    winloss_grid?: string[][]
  }
}
```

- 400 The `bet_type` string did not parse. Note this endpoint uses the `invalid_bet_type` code with the offending value under `data.bet_type`, not the usual `validation_error` shape. It also cannot validate multirunner bet types (`for,win,...`, `for,top,...`), which need an event context and return this error.

`application/json`

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

- 401 undefined

- 403 undefined

- 429 undefined

- 500 undefined

***

### [GET]/v2/stream

- Summary  
WebSocket stream

- Description  
**WebSocket endpoint** — upgrade an HTTP connection to receive  
real-time prices. This is a separate service from the REST API.  
  
### Connection  
  
Authenticate with an API key:  
  
```  
ws://<host>/v2/stream?api_key=<api_key>  
```  
  
**Query parameters:**  
  
| Param | Required | Description |  
|-------|----------|-------------|  
| `api_key` | yes | API key (the `X-Api-Key` value) |  
| `lang` | no | Language code: `en` (default), `ko`, `zh-hans` |  
  
The server does not restrict the `Origin` header, so browser-based  
clients - including pages opened from `file://` - can connect  
directly.  
  
### Wire format  
  
**Every** frame sent by the server is a batch envelope:  
  
```json  
{"ts": 1586042815.269000, "data": [ <message>, <message>, … ]}  
```  
  
- `ts` — Unix timestamp in seconds with microsecond precision,  
  stamped by the server when the frame is written.  
- `data` — one or more messages. The `["offer", …]`,  
  `["response", …]`, `["event", …]`,  
  `["remove_event", …]`, `["sync", …]`, live-event-state and  
  account-update arrays shown below are the individual `data[]`  
  entries; they are never sent as bare top-level frames.  
  
Multiple messages may be batched into a single envelope — e.g. a  
register snapshot and its ok `["response", …]` together, or offers alongside  
account updates. Batching boundaries are not semantically  
meaningful — iterate `data[]` and dispatch on each entry's leading  
type tag (`entry[0]`); never rely on ordering, grouping, or a type  
appearing exactly once per frame.  
  
### Initial sync  
  
After connecting, the server sends a snapshot of the  
currently-priced events followed by a `["sync", {…}]` marker  
whose payload carries the `session_id` of this stream (useful  
when correlating with REST errors or contacting support). Each  
event is an `["event", {…}]` entry — a flat object carrying the  
identifiers and metadata — delivered inside the envelope:  
  
```json  
{"ts": 1586042815.269000, "data": [  
  ["event", {"event_type": "normal", "sport": "fb", "event_id": "2026-06-15,1001,2002", "competition_id": 1, "competition_name": "England Premier League", "competition_country": "XE", "home": "Arsenal", "away": "Chelsea", "event_name": "Arsenal vs. Chelsea", "ir_status": "pre_event", "start_time": "2026-06-15T15:00:00Z"}],  
  ["event", {"event_type": "normal", "sport": "tn", "event_id": "2026-06-16,501,502", ...}],  
  ["event", {"event_type": "multirunner", "sport": "af", "event_id": "2026-02-23,multirunner,100364405", "competition_id": 545, "competition_name": "USA NFL", "competition_country": "US", "teams": [{"team_id": 21614, "name": "Arizona Cardinals"}, {"team_id": 21615, "name": "Atlanta Falcons"}], "event_name": "NFL Super Bowl Winner", "start_time": "2026-02-23T21:00:00Z", "end_time": "2027-02-14T21:00:00Z"}],  
  ["sync", {"session_id": "…"}]  
]}  
```  
  
Two event shapes appear. A `normal` (match) event carries `home` and  
`away`. A `multirunner` (outright / futures) event has no `home`/`away`;  
instead it carries a `teams` array (`[{"team_id", "name"}, …]`, one per  
runner) and an `end_time`. Dispatch on `event_type`; read the runner list  
from `teams` for multirunners.  
  
The snapshot is not the full fixture list: it contains only events  
that currently have live prices. The dump may span several  
envelopes; `["sync", …]` is the last `data[]` entry of the final one.  
From then on, changed events are re-sent as `["event", {…}]`  
entries, and an event whose prices disappear is delivered as  
`["remove_event", {"sport": …, "event_id": …, …}]`.  
  
### Live event state  
  
Events that are in play may additionally produce state updates as  
`data[]` entries. Payloads are sport-specific; the football shapes:  
  
```json  
["event_time", {"sport": "fb", "event_id": "…", "time": ["1h", 23]}]  
["event_score", {"sport": "fb", "event_id": "…", "score": [1, 0]}]  
["event_red_cards", {"sport": "fb", "event_id": "…", "score": [0, 1]}]  
```  
  
- `time` — `[period, minutes]`, where the football periods are  
  `"1h"`, `"2h"` and `"ht"`; `null` when no clock is available.  
- `score` — `[home, away]` (the `event_red_cards` payload reuses  
  the `score` key for the red-card counts).  
- `["ir_info", {…}]` carries a full in-running state snapshot for  
  an event (fields vary by sport), and  
  `["remove_ir_info", {"sport": …, "event_id": …}]` signals the  
  state is gone — treat both as informational.  
- `["event_exchange_dark_liquidity", {"sport": …, "event_id": …,  
  "lines": {…}}]` — a rough estimate of additional liquidity  
  available per line on the event, beyond the published offers.  
  Informational.  
  
### Commands  
  
**Register for offers on an event:**  
```json  
["register_event", "<sport>", "<event_id>"]  
```  
  
On success the server immediately sends one `["offer", …]` per  
active bet type on the event (the snapshot), then an ok response —  
typically batched in one envelope:  
`{"ts": …, "data": [["offer", {…}], ["offer", {…}],  
["response", {"status": "ok", "data": null}]]}`.  
From then on, whenever the offers on the event change, the  
full current set is re-broadcast (with the affected bet types  
updated) and any bet type that has lost all liquidity is  
delivered as `["remove_offer", …]`.  
  
**Unregister:**  
```json  
["unregister_event", "<sport>", "<event_id>"]  
```  
Server responds with `["response", {"status": "ok", "data":  
null}]` — also when the event was not registered (unregistering is  
idempotent). No further `offer` / `remove_offer` messages are sent  
for that event.  
  
**List currently registered events:**  
```json  
["list_registered_events"]  
```  
Server responds with the full set of `(sport, event_id)` pairs  
the session is registered for:  
```json  
["response", {"status": "ok", "data": {  
  "registered_events": [  
    ["fb", "2026-06-15,1001,2002"],  
    ["tennis", "2026-06-16,501,502"]  
  ]  
}}]  
```  
  
**Keepalive (echo):**  
```json  
["echo", "any-payload"]  
→ ["response", {"status": "ok", "data": ["any-payload"]}]  
```  
Arguments are optional, may be any JSON values, and are echoed  
back verbatim in `data`. The server also sends an `["info", …]`  
entry every few seconds, so an idle connection still receives  
regular traffic.  
  
### `offer` / `remove_offer` messages  
  
Each `offer` describes the available stake at every price for one  
`(sport, event_id, bet_type)` triple. The `bet_type` string fully  
identifies the market side (it encodes the market, handicap,  
outcome and for/against direction), so each triple is a distinct,  
independently-updated offer — for and against on the same  
selection arrive as two separate `offer` messages with different  
`bet_type` values.  
  
```json  
["offer", {  
  "sport": "fb",  
  "event_id": "2026-06-15,1001,2002",  
  "bet_type": "for,ah,h,1",  
  "market_type": "ah",  
  "in_running": false,  
  "price_list": [  
    {"effective": {"price": 2.0, "min": ["USDT", 5.0], "max": ["USDT", 150.0]}},  
    {"effective": {"price": 1.99, "min": null, "max": ["USDT", 80.0]}}  
  ]  
}]  
```  
  
Each `price_list` entry is  
`{"effective": {"price": <decimal>, "min": <stake|null>, "max": <stake>}}`;  
stakes are `["USDT", amount]` arrays. The `min` and `max` keys are  
always present:  
  
- `min` — the minimum stake accepted at that price; `null` when  
  there is no minimum.  
- `max` — the total stake available at that price. Always a  
  `["USDT", amount]` pair (a price with no available stake is not  
  published).  
  
Entries are ordered by `price` (the decimal odds) **descending**,  
with at most one entry per price.  
  
`remove_offer` carries only the `(sport, event_id, bet_type)`  
triple — that bet type has no remaining liquidity for the event:  
  
```json  
["remove_offer", {  
  "sport": "fb",  
  "event_id": "2026-06-15,1001,2002",  
  "bet_type": "for,ah,h,1"  
}]  
```  
  
### Account update messages  
  
Account-level updates arrive on the same WebSocket, as plain  
entries inside the same `{"ts": …, "data": […]}` envelope that  
carries `offer` / `remove_offer` — siblings of the market-data  
messages.  
  
```json  
{"ts": 1586042815.269000, "data": [  
  ["balance", {"balance": ["EUR", 10000.1], "open_stake": ["EUR", 152.55]}],  
  ["xrate", {"ccy": "EUR", "rate": 1.1347}],  
  ["order", {...}],  
  ["bet", {...}],  
  ["pmm", {...}],  
  ["betslip", {...}],  
  ["info", {...}]  
]}  
```  
  
`data[]` may contain these account entry types:  
  
- `balance`, `xrate` — amounts are in your account's native  
  currency.  
- `order` — `want_stake`, `stake` and `profit_loss` are in USDT;  
  each entry of nested `bets[]` follows the `bet` format.  
- `bet` — `want_stake`, `got_stake`, `profit_loss` and  
  `status.response_pmm.effective.min`/`max` are in USDT.  
- `pmm`, `betslip` — the live quote and state of an open betslip  
  (see the Quickstart). `price_list` entries follow the same  
  `{"effective": {"price", "min", "max"}}` format as `offer`  
  messages; `price_list` and `total` are in USDT, prices sorted  
  descending.  
- `betslip_closed` — `{"betslip_id": …, "close_reason": …}`. The  
  betslip expired (betslips are short-lived) or was closed; no  
  further `pmm` quotes will arrive for it. Create a new betslip  
  to re-quote the selection.  
- `info` — feed status; `registered_events` is the number of  
  events currently registered on this connection.  
- `clear_events` — the server lost its upstream market data feed:  
  discard all event, offer and live-state data you hold. A fresh  
  snapshot (events, then `["sync", …]`) follows when the feed  
  recovers.  
  
The order, presence, and count of entry types within `data[]` are  
not contractual — the example above shows one possible ordering  
only. Dispatch on each entry's type tag.  
  
**`balance` message fields:**  
- `balance`: `[currency, amount]` — current account balance in the customer's native currency.  
- `open_stake`: `[currency, amount]` — total stake across all unsettled bets, in the same currency.  
  
### Errors  
  
Recoverable command-level errors arrive in-band on the open  
connection. The socket stays up; you can keep sending commands.  
Like every other message, the error element is a `data[]` entry  
inside the envelope (possibly batched with other messages):  
  
```json  
{"ts": 1586042815.269000, "data": [["response", {"status": "error", "code": "<code>"}]]}  
```  
  
Codes emitted directly by the stream:  
  
| Code | When |  
|------|------|  
| `bad_json` | Frame is not valid JSON, not a JSON array, or an empty array. |  
| `invalid_input` | The command name is not a string or not recognised, or its argument shape is wrong for the command. |  
| `already_registered` | `register_event` for an event already registered on this session. |  
| `customer_event_limit_exceeded` | `register_event` would exceed your registered-events limit (counted across all your connections). Unregister something first. |  
| `invalid_customer` | `register_event` while the feed does not recognise your customer record (e.g. not yet propagated after a server restart). Retry after a short backoff; contact support if it persists. |  
| `system_error` | Transient server-side failure — retry after a short backoff. |  
  
Note there is no "unknown event" error: registering an event the  
feed has no prices for succeeds with an empty snapshot, and  
`unregister_event` of an unregistered event returns ok. Treat any  
other code string as **opaque** — log it and retry after a short  
backoff.  
  
**Authentication** is enforced at the HTTP handshake: a missing *or*  
invalid `api_key` makes the WebSocket upgrade fail with a non-101 HTTP  
response, which clients see as a handshake error (for example the  
`websockets` library raises `InvalidStatus`). An invalid key takes  
slightly longer to reject than a missing one, since it is checked  
server-side. Verify the key against a REST endpoint before opening the  
socket.  
  
Three classes of failure drop an *established* connection silently  
(raw TCP close, no WebSocket close frame, no in-band error):  
  
- **Backpressure** — the client is reading too slowly and the  
  server's outbound buffer overflows. Reconnect and resume.  
- **I/O error** — any read or write failure on the socket.  
- **Internal error** — a rare server-side failure; not  
  client-triggerable and observably identical to an I/O error.  
  Reconnect with backoff.  


#### Parameters(Query)

```typescript
api_key: string
```

```typescript
lang?: enum[en, ko, zh-hans] //default: en
```

#### Responses

- 101 Switching protocols — WebSocket connection established

- default Failures are delivered in-band as `["response", {"status": "error", "code": …}]` entries inside the `{"ts": …, "data": […]}` envelope on the open WebSocket, or by silent TCP close — see the **Errors** section above.

## References

### #/components/securitySchemes/ApiKeyHeader

```typescript
{
  "type": "apiKey",
  "in": "header",
  "name": "X-Api-Key",
  "description": "API key created from the magic-markets website"
}
```

### #/components/responses/Error400

- application/json

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/responses/Error401

- application/json

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/responses/Error403

- application/json

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/responses/Error404

- application/json

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/responses/Error429

- application/json

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/responses/Error500

- application/json

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/schemas/StakeTuple

```typescript
string | number[]
```

### #/components/schemas/ErrorEnvelope

```typescript
// Standard error response. The shape of `data` varies by `code` — see the **Errors** section in the introduction and the named examples on each endpoint's error responses.
{
  status: enum[error]
  // Stable machine-readable error code (e.g. `validation_error`, `not_found`, `forbidden`).
  code: string
}
```

### #/components/schemas/PriceLevel

```typescript
{
  effective: {
    // Decimal price
    price?: number
    // Minimum stake at this price level, or null
    min?: #/components/schemas/StakeTuple | null
    // Maximum stake at this price level, or null
    max?: #/components/schemas/StakeTuple | null
  }
}
```

### #/components/schemas/BetslipCreateResponse

```typescript
// Create (POST) response; carries no prices. Poll GET or watch the stream for the quote.
{
  betslip_id?: string
  // Sport code — see "Sports & bet types" in the introduction.
  sport?: string
  // Event identifier, e.g. 2026-06-15,1001,2002
  event_id?: string
  // Bet type string — see "Sports & bet types" in the introduction.
  bet_type?: string
  // Human-readable label, e.g. Home, Over 1.5 (Asian)
  bet_type_description?: string
  // Unix timestamp when the betslip expires
  expiry_ts?: number
  is_open?: boolean
  close_reason?: string | null
  // Whether equivalent bets are included
  equivalent_bets?: boolean
  customer_username?: string
  // Customer's base currency code
  customer_ccy?: string
  betslip_type?: enum[normal, lay, parlay]
  // Parlay legs (only present for parlay betslips)
  legs?: #/components/schemas/ParlayLegList | null
  user_data?: string | null
}
```

### #/components/schemas/BetslipResponse

```typescript
undefined?: #/components/schemas/BetslipCreateResponse & {
   price_list: {
     effective: {
       // Decimal price
       price?: number
       // Minimum stake at this price level, or null
       min?: #/components/schemas/StakeTuple | null
       // Maximum stake at this price level, or null
       max?: #/components/schemas/StakeTuple | null
     }
   }[]
   // Sum of max stakes across all price levels, or null if no prices
   total?: #/components/schemas/StakeTuple | null
 }
```

### #/components/schemas/BetslipCreateEnvelope

```typescript
{
  status?: enum[ok]
  // Create (POST) response; carries no prices. Poll GET or watch the stream for the quote.
  data: {
    betslip_id?: string
    // Sport code — see "Sports & bet types" in the introduction.
    sport?: string
    // Event identifier, e.g. 2026-06-15,1001,2002
    event_id?: string
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    // Human-readable label, e.g. Home, Over 1.5 (Asian)
    bet_type_description?: string
    // Unix timestamp when the betslip expires
    expiry_ts?: number
    is_open?: boolean
    close_reason?: string | null
    // Whether equivalent bets are included
    equivalent_bets?: boolean
    customer_username?: string
    // Customer's base currency code
    customer_ccy?: string
    betslip_type?: enum[normal, lay, parlay]
    // Parlay legs (only present for parlay betslips)
    legs?: #/components/schemas/ParlayLegList | null
    user_data?: string | null
  }
}
```

### #/components/schemas/BetslipEnvelope

```typescript
{
  status?: enum[ok]
  data?: #/components/schemas/BetslipCreateResponse & {
     price_list: {
       effective: {
         // Decimal price
         price?: number
         // Minimum stake at this price level, or null
         min?: #/components/schemas/StakeTuple | null
         // Maximum stake at this price level, or null
         max?: #/components/schemas/StakeTuple | null
       }
     }[]
     // Sum of max stakes across all price levels, or null if no prices
     total?: #/components/schemas/StakeTuple | null
   }
}
```

### #/components/schemas/BetslipListEnvelope

```typescript
{
  status?: enum[ok]
  data?: string[]
}
```

### #/components/schemas/BetslipCreateRequest

```typescript
{
  // Sport code (required for normal/lay) — see "Sports & bet types" in the introduction.
  sport?: string
  // Event ID (required for normal/lay)
  event_id?: string
  // Bet type string (required for normal/lay) — see "Sports & bet types" in the introduction.
  bet_type?: string
  legs: {
    sport: string
    event_id: string
    bet_type: string
  }[]
  betslip_type?: enum[normal, lay, parlay] //default: normal
  equivalent_bets?: boolean //default: true
  user_data?: string | null
  // When true, only liquidity sources that do not hold bets in danger status are used. When false or omitted, all available liquidity sources are used.
  exclude_danger?: boolean
}
```

### #/components/schemas/EventResultMatch

```typescript
// Football and other two-half sports. `ht_*` are the half-time score and are omitted for single-period sports; `ft_*` are the full-time score.
{
  ht_home?: integer | null
  ht_away?: integer | null
  ft_home?: integer | null
  ft_away?: integer | null
}
```

### #/components/schemas/EventResultTennis

```typescript
// Tennis. `setN_pM` is player M's game count in set N.
{
  set1_p1?: integer | null
  set1_p2?: integer | null
  set2_p1?: integer | null
  set2_p2?: integer | null
  set3_p1?: integer | null
  set3_p2?: integer | null
  set4_p1?: integer | null
  set4_p2?: integer | null
  set5_p1?: integer | null
  set5_p2?: integer | null
  // 1 or 2 if a player retired, else 0 or null
  who_retired?: integer | null
}
```

### #/components/schemas/EventResultHockey

```typescript
// Ice hockey. `tpN_*` are the three period scores; `tall_*` the regulation total; `pen_*` the penalty-shootout score.
{
  tp1_home?: integer | null
  tp1_away?: integer | null
  tp2_home?: integer | null
  tp2_away?: integer | null
  tp3_home?: integer | null
  tp3_away?: integer | null
  tall_home?: integer | null
  tall_away?: integer | null
  pen_home?: integer | null
  pen_away?: integer | null
}
```

### #/components/schemas/EventResultTableTennis

```typescript
// Table tennis. `gameN_*` is the point score in game N (up to 7 games).
{
  game1_home?: integer | null
  game1_away?: integer | null
  game2_home?: integer | null
  game2_away?: integer | null
  game3_home?: integer | null
  game3_away?: integer | null
  game4_home?: integer | null
  game4_away?: integer | null
  game5_home?: integer | null
  game5_away?: integer | null
  game6_home?: integer | null
  game6_away?: integer | null
  game7_home?: integer | null
  game7_away?: integer | null
}
```

### #/components/schemas/EventResultMultirunner

```typescript
// Multirunner (outright) events.
{
  runner_results: {
    team_id?: integer
    // 1=first, 2=second, 0=unknown, -1=void, -2=non-runner, -3=eliminated
    position?: integer
  }[]
  non_runner_count?: integer
}
```

### #/components/schemas/EventInfo

```typescript
{
  event_type?: enum[normal, multirunner, parlay]
  // null for parlay orders
  event_id?: string | null
  event_name?: string | null
  // Normal events only
  home_id?: integer | null
  home_team?: string | null
  // Normal events only
  away_id?: integer | null
  away_team?: string | null
  competition_id?: integer | null
  competition_name?: string | null
  // ISO country code (e.g. XE for England)
  competition_country?: string | null
  start_time?: string | null
  date?: string | null
  // Match or race result. The shape depends on the sport - see the `EventResult*` schemas. Null while the result is unknown.
  result?: #/components/schemas/EventResultMatch | #/components/schemas/EventResultTennis | #/components/schemas/EventResultHockey | #/components/schemas/EventResultTableTennis | #/components/schemas/EventResultMultirunner | null
  // Runner list (multirunner events only)
  teams?: {
     team_id?: integer
     name?: string
   }[] | null
  // Race end time (multirunner events only)
  end_time?: string | null
  // Sub-event info for each leg (parlay orders only)
  leg_event_infos?: array | null
}
```

### #/components/schemas/ParlayLegList

```typescript
{
  id?: integer
  // Sport code — see "Sports & bet types" in the introduction.
  sport?: string
  event_id?: string
  // Bet type string — see "Sports & bet types" in the introduction.
  bet_type?: string
  bet_type_description?: string
  price?: number | null
  // Leg settlement: `w` won, `l` lost, `v` void, `v/w` win-void, `l/v` void-loss, `l/w` loss-win (half outcomes), `unknown`, or null before settlement.
  outcome?: enum[w, l, v, v/w, l/v, l/w, unknown, ]
}[]
```

### #/components/schemas/ParlayLeg

```typescript
{
  id?: integer
  // Sport code — see "Sports & bet types" in the introduction.
  sport?: string
  event_id?: string
  // Bet type string — see "Sports & bet types" in the introduction.
  bet_type?: string
  bet_type_description?: string
  price?: number | null
  // Leg settlement: `w` won, `l` lost, `v` void, `v/w` win-void, `l/v` void-loss, `l/w` loss-win (half outcomes), `unknown`, or null before settlement.
  outcome?: enum[w, l, v, v/w, l/v, l/w, unknown, ]
}
```

### #/components/schemas/BetResponse

```typescript
// Individual bet within an order
{
  bet_id?: integer
  order_id?: integer
  order_ccy_rate?: number
  // Bet status
  status: {
    // e.g. matched, failed, settled
    code?: string
    // Human-readable failure reason; present only when `code` is `failed`
    reason?: string
    response_pmm?: #/components/schemas/PriceLevel | null
  }
  // Sport code — see "Sports & bet types" in the introduction.
  sport?: string
  event_id?: string | null
  // Bet type string — see "Sports & bet types" in the introduction.
  bet_type?: string
  ccy_rate?: number
  want_price?: number
  got_price?: number | null
  // #/components/schemas/StakeTuple
  want_stake?: string | number[]
  got_stake:#/components/schemas/StakeTuple
  profit_loss?: #/components/schemas/StakeTuple | null
  reconciled?: boolean | null
  exchange_role?: enum[maker, taker, ]
}
```

### #/components/schemas/OrderResponse

```typescript
{
  order_id?: integer
  // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
  order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
  // Bet type string — see "Sports & bet types" in the introduction.
  bet_type?: string
  bet_type_description?: string
  // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
  sport?: string
  want_price?: number
  // #/components/schemas/StakeTuple
  want_stake?: string | number[]
  // Exchange rate at order time
  ccy_rate?: number
  placement_time?: string
  expiry_time?: string
  closed?: boolean
  // Reason the order closed, e.g. order_filled, timed_out; null while open
  close_reason?: string | null
  event_info?: #/components/schemas/EventInfo | null
  // Individual bet within an order
  bets: {
    bet_id?: integer
    order_id?: integer
    order_ccy_rate?: number
    // Bet status
    status: {
      // e.g. matched, failed, settled
      code?: string
      // Human-readable failure reason; present only when `code` is `failed`
      reason?: string
      response_pmm?: #/components/schemas/PriceLevel | null
    }
    // Sport code — see "Sports & bet types" in the introduction.
    sport?: string
    event_id?: string | null
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    ccy_rate?: number
    want_price?: number
    got_price?: number | null
    want_stake:#/components/schemas/StakeTuple
    got_stake:#/components/schemas/StakeTuple
    profit_loss?: #/components/schemas/StakeTuple | null
    reconciled?: boolean | null
    exchange_role?: enum[maker, taker, ]
  }[]
  user_data?: string | null
  // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
  status?: string
  keep_open_ir?: boolean
  // Exchange interaction mode as passed at creation; null on older orders
  exchange_mode?: enum[make_and_take, take_only, dark, ]
  // Achieved price (null while open)
  price?: number | null
  // Aggregate stake across matched bets
  stake?: #/components/schemas/StakeTuple | null
  profit_loss?: #/components/schemas/StakeTuple | null
  bet_bar_values?: object | null
  legs?: #/components/schemas/ParlayLegList | null
}
```

### #/components/schemas/OrderEnvelope

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }
}
```

### #/components/schemas/OrderListEnvelope

```typescript
{
  status?: enum[ok]
  data: {
    order_id?: integer
    // Order type. `normal`, `lay` and `parlay` are the types placed through this API; `brokerage`, `cashout` and `custom` can appear on orders created by other channels. Treat it as an open set.
    order_type?: enum[normal, lay, parlay, brokerage, cashout, custom]
    // Bet type string — see "Sports & bet types" in the introduction.
    bet_type?: string
    bet_type_description?: string
    // Sport code, or `parlay` for accumulators — see "Sports & bet types" in the introduction.
    sport?: string
    want_price?: number
    // #/components/schemas/StakeTuple
    want_stake?: string | number[]
    // Exchange rate at order time
    ccy_rate?: number
    placement_time?: string
    expiry_time?: string
    closed?: boolean
    // Reason the order closed, e.g. order_filled, timed_out; null while open
    close_reason?: string | null
    event_info?: #/components/schemas/EventInfo | null
    // Individual bet within an order
    bets: {
      bet_id?: integer
      order_id?: integer
      order_ccy_rate?: number
      // Bet status
      status: {
        // e.g. matched, failed, settled
        code?: string
        // Human-readable failure reason; present only when `code` is `failed`
        reason?: string
        response_pmm?: #/components/schemas/PriceLevel | null
      }
      // Sport code — see "Sports & bet types" in the introduction.
      sport?: string
      event_id?: string | null
      // Bet type string — see "Sports & bet types" in the introduction.
      bet_type?: string
      ccy_rate?: number
      want_price?: number
      got_price?: number | null
      want_stake:#/components/schemas/StakeTuple
      got_stake:#/components/schemas/StakeTuple
      profit_loss?: #/components/schemas/StakeTuple | null
      reconciled?: boolean | null
      exchange_role?: enum[maker, taker, ]
    }[]
    user_data?: string | null
    // Order lifecycle status: open, pending, failed, partial_void, full_void, done, or reconciled
    status?: string
    keep_open_ir?: boolean
    // Exchange interaction mode as passed at creation; null on older orders
    exchange_mode?: enum[make_and_take, take_only, dark, ]
    // Achieved price (null while open)
    price?: number | null
    // Aggregate stake across matched bets
    stake?: #/components/schemas/StakeTuple | null
    profit_loss?: #/components/schemas/StakeTuple | null
    bet_bar_values?: object | null
    legs?: #/components/schemas/ParlayLegList | null
  }[]
}
```

### #/components/schemas/OrderCreateRequest

```typescript
{
  betslip_id: string
  // Desired decimal price. Off-tick prices are rounded down for back (`for`) orders and up for lay (`against`) orders; see "Price ticks".
  price: number
  // #/components/schemas/StakeTuple
  stake?: string | number[]
  // Order duration in seconds (default 15)
  duration?: number
  // How the order interacts with the exchange. `make_and_take`: fill against the best available liquidity first; any remaining stake is advertised on the exchange at your price, while the order keeps taking newly available liquidity. `take_only`: only consume available liquidity; remaining stake is never advertised and other orders cannot match against it. `dark`: like `make_and_take`, but the advertised remaining stake is hidden from other customers - they can still match it when their price crosses yours, but they cannot see your price until then. There is no post-only mode: no order type rests in the book without also taking crossing liquidity.
  exchange_mode?: enum[make_and_take, take_only, dark] //default: make_and_take
  // Keep order open when event goes in-play
  keep_open_ir?: boolean
  user_data?: string | null
  // Idempotency key
  request_uuid?: string
  accept_partial_fill?: boolean //default: true
  accept_better_price?: boolean //default: true
  force_want_price?: boolean
  // `dark` orders only (rejected on other modes): the minimum total stake another order must request to be allowed to match yours at your price point. Cannot exceed the order's own stake. Set it non-zero to stop small probe orders from discovering your price.
  min_taker_want_stake?: #/components/schemas/StakeTuple | null
  // Placement-time score assertion, `[home, away]`. When present, the order is rejected with `validation_error` / `non_field_errors: ["event_scores_dont_match"]` unless the value matches the live score the exchange holds for the event - use it to avoid placing on a price that has not reacted to a goal yet. Only meaningful for football: for sports without a goal-style running score, and while no score is known yet, the server assumes `[0, 0]` and rejects any other value. Omit the field to skip the check.
  current_score?: integer[]
  // When true, only liquidity sources that do not hold bets in danger status are used. When false or omitted, all available liquidity sources are used.
  exclude_danger?: boolean
  // Optional per-source minimum stakes, keyed by source. A source is only used if it can take at least its minimum. Values are `[currency, amount]` tuples.
  bookie_min_stakes: {
  }
  // Optional caller-supplied tag recorded against the order.
  placer_type?: string | null
}
```

### #/components/schemas/BalanceResponse

```typescript
{
  // #/components/schemas/StakeTuple
  balance?: string | number[]
  open_stake:#/components/schemas/StakeTuple
  // Smart credit, or null when the account has none
  smart_credit: #/components/schemas/StakeTuple | null
}
```

### #/components/schemas/BalanceEnvelope

```typescript
{
  status?: enum[ok]
  data: {
    // #/components/schemas/StakeTuple
    balance?: string | number[]
    open_stake:#/components/schemas/StakeTuple
    // Smart credit, or null when the account has none
    smart_credit: #/components/schemas/StakeTuple | null
  }
}
```

### #/components/schemas/HeartbeatResponse

```typescript
{
  heartbeat_id?: string
  expiry_time?: string
}
```

### #/components/schemas/HeartbeatEnvelope

```typescript
{
  status?: enum[ok]
  data: {
    heartbeat_id?: string
    expiry_time?: string
  }
}
```

### #/components/schemas/HeartbeatListEnvelope

```typescript
// List response. Note the data is wrapped under a `heartbeats` key, unlike other list endpoints which return a flat array.
{
  status?: enum[ok]
  data: {
    heartbeats: {
      heartbeat_id?: string
      expiry_time?: string
    }[]
  }
}
```

### #/components/schemas/HeartbeatCancelEnvelope

```typescript
// Cancel response. `data` is always `null`.
{
  status?: enum[ok]
  data?: null
}
```

### #/components/schemas/XRate

```typescript
{
  // Currency code (e.g. USD, EUR, USDT)
  ccy?: string
  // Exchange rate to USDT
  rate?: number
}
```

### #/components/schemas/XRatesEnvelope

```typescript
{
  status?: enum[ok]
  data: {
    // Currency code (e.g. USD, EUR, USDT)
    ccy?: string
    // Exchange rate to USDT
    rate?: number
  }[]
}
```

### #/components/schemas/BetTypeInfoResponse

```typescript
{
  // Display name for the sport (e.g. "Football")
  sport?: string
  // Human-readable bet type label
  bet_type_description?: string
  winloss_grid?: string[][]
}
```

### #/components/schemas/BetTypeInfoEnvelope

```typescript
{
  status?: enum[ok]
  data: {
    // Display name for the sport (e.g. "Football")
    sport?: string
    // Human-readable bet type label
    bet_type_description?: string
    winloss_grid?: string[][]
  }
}
```

### #/components/schemas/PositionGrid

```typescript
// Profit or loss per scoreline, in USDT: `values[home_score][away_score]`. The grid is square and its size depends on the sport.
{
  // Always USDT
  ccy_code: string
  values?: number[][]
}
```

### #/components/schemas/PositionComponentTotal

```typescript
// Position for a standard bet type.
{
  // Bet type rendered for display
  bet_type_description: string
  // Average matched price, or null if nothing matched
  got_price: number | null
  // #/components/schemas/StakeTuple
  got_stake?: string | number[]
  // Average price of the bets whose outcome is still unknown
  unknown_price: number | null
  unknown_stake:#/components/schemas/StakeTuple
}
```

### #/components/schemas/PositionCustomBetTotal

```typescript
// Position for a custom bet type, which carries its own grid instead of prices.
{
  // Bet type rendered for display
  bet_type_description: string
  payoff_grid: #/components/schemas/PositionGrid | null
  // #/components/schemas/StakeTuple
  got_stake?: string | number[]
}
```

### #/components/schemas/PositionCashoutInfo

```typescript
// Cashout valuation for the position. Present on every non-multirunner position. Cashout is offered on football only; on every other sport, and whenever cashout is otherwise unavailable, `allowed` is false and every value below is null.
{
  allowed: boolean
  // Why cashout is unavailable, when it can be shared: `position_already_flat` or `insufficient_credit`.
  reason: string | null
  // Amount offered to close the position now
  valuation: #/components/schemas/StakeTuple | null
  // Stake currently at risk on the position
  stake: #/components/schemas/StakeTuple | null
  // Change in smart credit that taking the cashout would cause
  smart_credit_delta: #/components/schemas/StakeTuple | null
  // Payoff grid the cashout is valued against
  position: #/components/schemas/PositionGrid | null
}
```

### #/components/schemas/PositionResponse

```typescript
// Aggregate profit/loss position over the filtered orders. `sport`, `event_id` and `event_info` are present once the filters match at least one order.
{
  // Payoff from the matched bets, or null when every cell is zero
  payoff_grid: #/components/schemas/PositionGrid | null
  // Position per bet type, keyed by the unflipped bet type string
  totals: {
  }
  // Number of bets that could not be projected onto the grid
  unknown_bets_num: integer
  // Payoff from the bets counted in `unknown_bets_num`
  unknown_grid: #/components/schemas/PositionGrid | null
  sport?: string
  event_id?: string
  event_info: {
    event_type?: enum[normal, multirunner, parlay]
    // null for parlay orders
    event_id?: string | null
    event_name?: string | null
    // Normal events only
    home_id?: integer | null
    home_team?: string | null
    // Normal events only
    away_id?: integer | null
    away_team?: string | null
    competition_id?: integer | null
    competition_name?: string | null
    // ISO country code (e.g. XE for England)
    competition_country?: string | null
    start_time?: string | null
    date?: string | null
    // Match or race result. The shape depends on the sport - see the `EventResult*` schemas. Null while the result is unknown.
    result?: #/components/schemas/EventResultMatch | #/components/schemas/EventResultTennis | #/components/schemas/EventResultHockey | #/components/schemas/EventResultTableTennis | #/components/schemas/EventResultMultirunner | null
    // Runner list (multirunner events only)
    teams?: {
       team_id?: integer
       name?: string
     }[] | null
    // Race end time (multirunner events only)
    end_time?: string | null
    // Sub-event info for each leg (parlay orders only)
    leg_event_infos?: array | null
  }
  // Cashout valuation for the position. Present on every non-multirunner position. Cashout is offered on football only; on every other sport, and whenever cashout is otherwise unavailable, `allowed` is false and every value below is null.
  cashout_info: {
    allowed: boolean
    // Why cashout is unavailable, when it can be shared: `position_already_flat` or `insufficient_credit`.
    reason: string | null
    // Amount offered to close the position now
    valuation: #/components/schemas/StakeTuple | null
    // Stake currently at risk on the position
    stake: #/components/schemas/StakeTuple | null
    // Change in smart credit that taking the cashout would cause
    smart_credit_delta: #/components/schemas/StakeTuple | null
    // Payoff grid the cashout is valued against
    position: #/components/schemas/PositionGrid | null
  }
}
```

### #/components/schemas/PositionEnvelope

```typescript
{
  status?: enum[ok]
  // Aggregate profit/loss position over the filtered orders. `sport`, `event_id` and `event_info` are present once the filters match at least one order.
  data: {
    // Payoff from the matched bets, or null when every cell is zero
    payoff_grid: #/components/schemas/PositionGrid | null
    // Position per bet type, keyed by the unflipped bet type string
    totals: {
    }
    // Number of bets that could not be projected onto the grid
    unknown_bets_num: integer
    // Payoff from the bets counted in `unknown_bets_num`
    unknown_grid: #/components/schemas/PositionGrid | null
    sport?: string
    event_id?: string
    event_info: {
      event_type?: enum[normal, multirunner, parlay]
      // null for parlay orders
      event_id?: string | null
      event_name?: string | null
      // Normal events only
      home_id?: integer | null
      home_team?: string | null
      // Normal events only
      away_id?: integer | null
      away_team?: string | null
      competition_id?: integer | null
      competition_name?: string | null
      // ISO country code (e.g. XE for England)
      competition_country?: string | null
      start_time?: string | null
      date?: string | null
      // Match or race result. The shape depends on the sport - see the `EventResult*` schemas. Null while the result is unknown.
      result?: #/components/schemas/EventResultMatch | #/components/schemas/EventResultTennis | #/components/schemas/EventResultHockey | #/components/schemas/EventResultTableTennis | #/components/schemas/EventResultMultirunner | null
      // Runner list (multirunner events only)
      teams?: {
         team_id?: integer
         name?: string
       }[] | null
      // Race end time (multirunner events only)
      end_time?: string | null
      // Sub-event info for each leg (parlay orders only)
      leg_event_infos?: array | null
    }
    // Cashout valuation for the position. Present on every non-multirunner position. Cashout is offered on football only; on every other sport, and whenever cashout is otherwise unavailable, `allowed` is false and every value below is null.
    cashout_info: {
      allowed: boolean
      // Why cashout is unavailable, when it can be shared: `position_already_flat` or `insufficient_credit`.
      reason: string | null
      // Amount offered to close the position now
      valuation: #/components/schemas/StakeTuple | null
      // Stake currently at risk on the position
      stake: #/components/schemas/StakeTuple | null
      // Change in smart credit that taking the cashout would cause
      smart_credit_delta: #/components/schemas/StakeTuple | null
      // Payoff grid the cashout is valued against
      position: #/components/schemas/PositionGrid | null
    }
  }
}
```