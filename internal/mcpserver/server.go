// Package mcpserver exposes the Magic Markets API as MCP tools over stdio or
// the streamable HTTP transport, so an LLM agent can read prices and manage
// orders.
//
// Read-only tools are always registered. Tools that spend money — creating
// betslips, placing orders, closing orders — are registered only when trading is
// explicitly enabled, so the default configuration cannot place a bet.
package mcpserver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"magicmarkets-cli/internal/config"
	"magicmarkets-cli/internal/magicmarkets"
)

// HTTPPath is where the streamable HTTP transport is mounted.
const HTTPPath = "/mcp"

// serverCardPath is where an operator-supplied server card is served — see
// servercard.go.
const serverCardPath = HTTPPath + "/server-card"

// Options configures the MCP server.
type Options struct {
	// AllowTrading registers the tools that create betslips, place orders and
	// close orders. Off by default.
	AllowTrading bool

	// Version is reported to the MCP client.
	Version string

	// PublicURL is the externally-reachable base of this server, without a
	// trailing /mcp. Advertised in OAuth protected-resource metadata so a
	// remote client can complete the Magic Markets Authorization Code flow.
	// When empty, metadata uses the incoming request's host.
	PublicURL string

	// OAuthIssuer is the Magic Markets Authorization Server this host proxies
	// /authorize and /token to. Protected-resource metadata still names *this*
	// server as the AS so Claude's DCR hits /register here — see oauth.go.
	OAuthIssuer string
}

// Server wires the API client into an MCP server.
type Server struct {
	client     *magicmarkets.Client
	cfg        *config.Config
	opts       Options
	seal       *sealer
	httpClient *http.Client
}

// New builds the MCP server.
func New(client *magicmarkets.Client, cfg *config.Config, opts Options) *Server {
	if opts.OAuthIssuer == "" {
		if cfg != nil && cfg.OAuthIssuer != "" {
			opts.OAuthIssuer = cfg.OAuthIssuer
		} else {
			opts.OAuthIssuer = config.DefaultOAuthIssuer
		}
	}
	if opts.PublicURL == "" && cfg != nil {
		opts.PublicURL = cfg.MCPPublicURL
	}
	secret := ""
	if cfg != nil {
		secret = cfg.OAuthProxySecret
	}
	if secret == "" {
		secret = randomSecret()
	}
	return &Server{
		client:     client,
		cfg:        cfg,
		opts:       opts,
		seal:       newSealer(secret),
		httpClient: http.DefaultClient,
	}
}

// Serve registers every tool and serves MCP over stdio.
//
// stdout carries the JSON-RPC stream, so all logging must go to stderr.
func (s *Server) Serve() error {
	return s.newMCP().Run(context.Background(), &mcp.StdioTransport{})
}

// ServeHTTP registers every tool and serves MCP over the streamable HTTP
// transport at addr, until ctx is cancelled.
//
// The process does not hold a Magic Markets API key. Each request must
// carry the caller's credential as X-Api-Key or Authorization: Bearer. An
// API key is forwarded to the upstream API and stream unchanged; a Bearer
// token is resolved via POST {OAuthIssuer}/oauth2/firebase-token to the
// magic-metadata-jwt and session values they actually require — see
// magicmarkets.MeResolver. /health and /live are unauthenticated for probes.
// OAuth discovery names
// this process as the Authorization Server so Claude's DCR hits /register
// here; /authorize and /token proxy to the Magic Markets issuer — see
// oauth.go.
func (s *Server) ServeHTTP(ctx context.Context, addr string) error {
	if s.cfg != nil && s.cfg.OAuthProxySecret == "" {
		log.Printf("[mcp] MAGICMARKETS_OAUTH_PROXY_SECRET is not set — OAuth proxy state (DCR client_ids, " +
			"in-flight logins, one-time codes) is sealed with a key generated for this process only. " +
			"A single-replica deployment is fine; a multi-replica one must set this so a request routed " +
			"to a different replica can still decode state minted by another")
	}
	httpServer := &http.Server{Addr: addr, Handler: s.httpHandler()}

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	}
}

// httpHandler builds the mux ServeHTTP listens on, split out so tests can
// exercise it without binding a real port.
//
// The /me resolver is built once here, not per request: its whole purpose is
// a cache ([magicmarkets.MeCacheTTL]) that survives across calls on the same
// access token, so it must outlive any single request — see [MeResolver].
func (s *Server) httpHandler() http.Handler {
	resolver := magicmarkets.NewMeResolver(s.opts.OAuthIssuer, s.cfg.SessionGroupID, s.cfg.FirebaseWebAPIKey, s.httpClient)

	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		cred, ok := magicmarkets.CredentialFromHeaders(r.Header)
		if !ok {
			return nil
		}
		cfg := *s.cfg
		// Per-request credential only. A Bearer call leaves APIKey empty so
		// the copied config does not keep a process-level key.
		cfg.APIKey = cred.APIKey
		clientOpts := []magicmarkets.Option{
			magicmarkets.WithUserAgent("magicmarkets-cli/" + s.opts.Version),
			magicmarkets.WithMeResolver(resolver),
			magicmarkets.WithBasicAuth(cfg.BasicAuth),
		}
		client := magicmarkets.NewWithCredential(cfg.APIURL, cred, cfg.Timeout, clientOpts...)
		return New(client, &cfg, s.opts).newMCP()
	}, streamableHTTPOptions())

	mux := http.NewServeMux()
	mux.Handle(HTTPPath, requireCallerCredential(s.resourceMetadataURL, streamable))
	s.mountOAuth(mux)
	mux.HandleFunc(serverCardPath, s.serveServerCard)
	mux.HandleFunc("/health", probeOK)
	mux.HandleFunc("/live", probeOK)
	return mux
}

func probeOK(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// streamableHTTPOptions is how we serve MCP over HTTP.
//
// Stateless: the hosted deployment runs more than one replica with no
// session affinity. Streamable HTTP sessions live in process memory, so a
// tools/call that lands on a different replica than initialize looks like
// "session not found". Every tool is a self-contained API call keyed by
// the caller's X-Api-Key or Authorization Bearer token, so we do not need a
// transport session.
//
// JSONResponse: return a single application/json body rather than
// text/event-stream for tool results.
func streamableHTTPOptions() *mcp.StreamableHTTPOptions {
	return &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	}
}

// requireCallerCredential rejects requests with neither X-Api-Key nor
// Authorization: Bearer. The value is the caller's Magic Markets credential,
// not a server-side secret.
func requireCallerCredential(resourceMetadata func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := magicmarkets.CredentialFromHeaders(r.Header); !ok {
			if url := resourceMetadata(r); url != "" {
				w.Header().Set("WWW-Authenticate",
					fmt.Sprintf(`Bearer realm="MagicMarkets", resource_metadata=%q`, url))
			}
			http.Error(w, "missing "+magicmarkets.APIKeyHeader+" or Authorization: Bearer", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) resourceMetadataURL(r *http.Request) string {
	return strings.TrimRight(s.publicBase(r), "/") + protectedResourceMCPPath
}

func (s *Server) publicBase(r *http.Request) string {
	if base := strings.TrimRight(s.opts.PublicURL, "/"); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ToolNames returns the tools this server would expose, sorted.
//
// It registers into a throwaway server rather than duplicating the list, so it
// cannot drift from what Serve actually exposes. Used by `magicmarkets mcp
// --print-tools` to show whether trading is enabled without needing a client.
func (s *Server) ToolNames() []string {
	tools, err := listRegisteredTools(s.newMCP())
	if err != nil {
		panic(err)
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// TradingEnabled reports whether the money-spending tools are registered.
func (s *Server) TradingEnabled() bool { return s.opts.AllowTrading }

func (s *Server) newMCP() *mcp.Server {
	m := mcp.NewServer(&mcp.Implementation{
		Name:    "magicmarkets",
		Version: s.opts.Version,
	}, nil)
	s.register(m)
	return m
}

// register wires the tool set onto an MCP server.
func (s *Server) register(m *mcp.Server) {
	// Read-only: account and reference.
	addTool(m, "get_balance",
		"Get the account balance, the stake committed to open bets, and smart credit. "+
			"All amounts are USDT.",
		hints(true, false, true, false), s.getBalance)
	addTool(m, "get_exchange_rates",
		"Get exchange rates to USDT. Also the cheapest way to verify the API key works.",
		hints(true, false, true, false), s.getXRates)
	addTool(m, "get_position",
		"Get the aggregate profit/loss position over the matching orders, including a "+
			"payoff grid per scoreline. Narrow it to one event for a readable result. "+
			"Cashout valuations are offered on football only.",
		hints(true, false, true, false), s.getPosition)
	addTool(m, "validate_bet_type",
		"Validate a bet_type string against a sport and get its human-readable description "+
			"plus win/loss grid. An error means the string did not parse. "+
			"Prefer copying bet_type values verbatim from list_event_offers rather than constructing them. "+
			"Grammar: the first token is the direction ('for' to back, 'against' to lay), then the market and "+
			"its arguments. Examples: 'for,h' home win, 'for,over,2.5' over 2.5 goals, 'for,ah,h,-4' Asian "+
			"handicap home -1.0 (Asian lines are integers equal to 4x the real line), 'for,cs,2,1' correct "+
			"score 2-1. Handicaps always refer to the home team.",
		hints(true, false, true, false), s.validateBetType)
	addTool(m, "snap_price",
		"Snap a decimal price onto the API's tick schedule and report the tick size and "+
			"implied probability. Runs locally with no API call. "+
			"On a normal betslip an off-tick limit moves up to the next tick (7.15 becomes 7.20), "+
			"whether the bet type starts with 'for' or 'against'. On a lay betslip it moves down (2.31 becomes 2.30). "+
			"A parlay price lies on no tick schedule and is left as is. "+
			"Use this to know the price an order will actually run with.",
		hints(true, false, true, false), s.snapPrice)

	// Read-only: market discovery over REST.
	addTool(m, "list_events",
		"List the events that currently have prices, filtered on the server by sport, competition, "+
			"in-play status and start-time window. Only priced events appear — this is not a fixture list. "+
			"search matches event, team and competition names; it is applied while paging, so a narrow "+
			"sport or time window makes it faster. When next_after is returned, pass it back as after "+
			"(with the same filters) for the next page. "+
			"Use the returned sport and event_id with list_event_offers to see bet types and prices.",
		hints(true, false, true, true), s.listEvents)
	addTool(m, "list_event_offers",
		"List the priced bet types on one or more events of one sport, with the stake available at each price. "+
			"Pass several event_ids to compare markets across matches in one call, and min_liquidity "+
			"(USDT) to keep only markets with real depth. "+
			"Each offer covers one (event_id, bet_type) pair; back and lay on the same selection are "+
			"separate offers. Pass a returned bet_type verbatim to create_betslip — never construct one by hand. "+
			"Prices are ordered best first and are already on the tick schedule. "+
			"Prices can trail the live market by a few seconds; the betslip quote is authoritative.",
		hints(true, false, true, true), s.listEventOffers)

	// Read-only: orders and betslips.
	addTool(m, "list_orders",
		"List orders, optionally filtered. Amounts are USDT.",
		hints(true, false, true, false), s.listOrders)
	addTool(m, "get_order",
		"Get one order by ID, or by the request_uuid it was created with. "+
			"Looking up by request_uuid works for six hours after placement and is the safe way to find out "+
			"whether a timed-out placement actually succeeded.",
		hints(true, false, true, false), s.getOrder)
	addTool(m, "list_betslips",
		"List the IDs of currently open betslips. Betslips are short-lived.",
		hints(true, false, true, false), s.listBetslips)
	addTool(m, "get_betslip",
		"Get a betslip and its current price list. "+
			"Quotes arrive asynchronously after creation, so a fresh betslip is normally unpriced — "+
			"set wait_seconds to poll until a price lands.",
		hints(true, false, true, false), s.getBetslip)

	if !s.opts.AllowTrading {
		return
	}

	// Money-spending tools, registered only on explicit opt-in.
	addTool(m, "create_betslip",
		"Create a betslip: register interest in one selection so it gets quoted. "+
			"This is step one of two — a betslip costs nothing and commits nothing, but an order needs one. "+
			"Take bet_type verbatim from list_event_offers. Betslips are short-lived and carry no price at "+
			"creation, so set wait_seconds to poll for the quote.",
		hints(false, false, false, true), s.createBetslip)
	addTool(m, "close_betslip",
		"Close an open betslip you no longer intend to trade on: its quotes stop and it stops counting "+
			"toward the open-betslip cap. Commits nothing and cancels no orders.",
		hints(false, false, false, true), s.closeBetslip)
	addTool(m, "place_order",
		"Place an order against a betslip. THIS SPENDS REAL MONEY. "+
			"Requires an existing betslip from create_betslip. An off-tick price is snapped onto the tick schedule "+
			"(up on a normal betslip, down on a lay betslip, never on a parlay) and the snapped value is what the order runs with. "+
			"Always pass request_uuid: it makes the call idempotent, so a retry after a timeout cannot create "+
			"a second order, and get_order can then find it by that uuid. "+
			"Confirm the stake with the user before calling this.",
		hints(false, true, false, true), s.placeOrder)
	addTool(m, "close_order",
		"Cancel one open order by ID. Already-settled orders return an order_closed error.",
		hints(false, true, true, true), s.closeOrder)
	addTool(m, "close_all_orders",
		"Cancel every open order, optionally narrowed to one sport or event. "+
			"Narrowing by event requires the sport too. Confirm with the user before calling this.",
		hints(false, true, true, true), s.closeAllOrders)
	addTool(m, "create_heartbeat",
		"Start a dead-man's switch. If it is not refreshed before it expires, every open "+
			"order is closed automatically. Timeout is 10-300 seconds. "+
			"Refresh it well before expiry with refresh_heartbeat.",
		hints(false, false, false, true), s.createHeartbeat)
	addTool(m, "refresh_heartbeat",
		"Extend a heartbeat's expiry, keeping the dead-man's switch from firing.",
		hints(false, false, false, true), s.refreshHeartbeat)
	addTool(m, "list_heartbeats",
		"List active heartbeats.",
		hints(true, false, true, false), s.listHeartbeats)
	addTool(m, "cancel_heartbeat",
		"Stop a heartbeat without closing any orders.",
		hints(false, false, true, true), s.cancelHeartbeat)
}

// ---------- read-only tools ----------

func (s *Server) getBalance(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, balanceResult, error) {
	bal, err := s.client.GetBalance(ctx)
	if err != nil {
		return nil, balanceResult{}, err
	}
	return nil, balanceFromAPI(bal), nil
}

func (s *Server) getXRates(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, exchangeRatesResult, error) {
	rates, err := s.client.GetXRates(ctx)
	if err != nil {
		return nil, exchangeRatesResult{}, err
	}
	mapped := ratesFromAPI(rates)
	return nil, exchangeRatesResult{Count: len(mapped), Rates: mapped}, nil
}

func (s *Server) getPosition(ctx context.Context, _ *mcp.CallToolRequest, in getPositionInput) (*mcp.CallToolResult, position, error) {
	pos, err := s.client.GetPosition(ctx, in.orderFilter(), in.IncludeCashoutInfo)
	if err != nil {
		return nil, position{}, err
	}
	return nil, positionFromAPI(pos), nil
}

func (s *Server) validateBetType(ctx context.Context, _ *mcp.CallToolRequest, in validateBetTypeInput) (*mcp.CallToolResult, validateBetTypeResult, error) {
	if in.Sport == "" || in.BetType == "" {
		return nil, validateBetTypeResult{}, fmt.Errorf("sport and bet_type are required")
	}
	info, err := s.client.GetBetTypeInfo(ctx, in.Sport, in.BetType, in.HomeTeam, in.AwayTeam)
	if err != nil {
		if magicmarkets.HasCode(err, magicmarkets.CodeValidationError) {
			return nil, validateBetTypeResult{Valid: false, Error: err.Error()}, nil
		}
		return nil, validateBetTypeResult{}, err
	}
	return nil, validateBetTypeResult{
		Valid:              true,
		Sport:              info.Sport,
		BetType:            in.BetType,
		BetTypeDescription: info.BetTypeDescription,
		Direction:          string(magicmarkets.DirectionOf(in.BetType)),
		WinLossGrid:        info.WinLossGrid,
	}, nil
}

func (s *Server) snapPrice(_ context.Context, _ *mcp.CallToolRequest, in snapPriceInput) (*mcp.CallToolResult, snapPriceResult, error) {
	if in.Price <= 0 {
		return nil, snapPriceResult{}, fmt.Errorf("price must be positive")
	}

	// The betslip type decides the rounding, not the bet type: an "against"
	// bet type on a normal betslip still rounds up.
	betslipType := in.BetslipType
	switch betslipType {
	case "":
		betslipType = magicmarkets.BetslipNormal
	case magicmarkets.BetslipNormal, magicmarkets.BetslipLay, magicmarkets.BetslipParlay:
	default:
		return nil, snapPriceResult{}, fmt.Errorf("betslip_type must be normal, lay or parlay, got %q", in.BetslipType)
	}

	snapped := magicmarkets.SnapOrderPrice(in.Price, betslipType)
	out := snapPriceResult{
		Requested:    in.Price,
		BetslipType:  betslipType,
		Snapped:      snapped,
		AlreadyValid: magicmarkets.IsOnTick(in.Price),
		ImpliedCents: magicmarkets.ImpliedCents(snapped),
	}
	if betslipType == magicmarkets.BetslipParlay {
		// An accumulator price is valid at full precision.
		out.AlreadyValid = true
	} else {
		out.Tick = magicmarkets.TickAt(snapped)
	}
	return nil, out, nil
}

func (s *Server) listEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, listEventsResult, error) {
	f, err := in.eventFilter(s.cfg.Lang)
	if err != nil {
		return nil, listEventsResult{}, err
	}
	limit := int(in.Limit)
	if limit <= 0 {
		limit = 50
	}
	needle := strings.ToLower(in.Search)

	// Without a search every event the server returns is kept, so a page of
	// limit+1 is enough to know whether more follow. A search filters
	// locally and has to scan full pages.
	pageSize := limit + 1
	if needle != "" {
		pageSize = magicmarkets.MaxMarketPage
	}

	out := make([]event, 0, limit)
	var (
		scanned int
		last    magicmarkets.StreamEvent
		more    bool
	)
	err = s.client.EachEvent(ctx, f, pageSize, in.After, func(e magicmarkets.StreamEvent) bool {
		if needle != "" && !eventMatches(e, needle) {
			scanned++
			last = e
			return true
		}
		if len(out) == limit {
			more = true
			return false
		}
		scanned++
		last = e
		out = append(out, eventFromStream(e))
		return true
	})
	if err != nil {
		return nil, listEventsResult{}, err
	}

	res := listEventsResult{
		Count:    len(out),
		Scanned:  scanned,
		Events:   out,
		NextStep: "Call list_event_offers with a sport and one or more event_ids to see priced bet types.",
	}
	if more {
		res.NextAfter = magicmarkets.EventCursor(last)
	}
	return nil, res, nil
}

// eventMatches reports whether any display field of e contains needle, which
// must already be lowercased.
func eventMatches(e magicmarkets.StreamEvent, needle string) bool {
	fields := []string{e.EventName, e.Home, e.Away, e.CompetitionName, e.CompetitionCountry, e.EventID}
	for _, t := range e.Teams {
		fields = append(fields, t.Name)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

func (s *Server) listEventOffers(ctx context.Context, _ *mcp.CallToolRequest, in listEventOffersInput) (*mcp.CallToolResult, listEventOffersResult, error) {
	f := in.offerFilter()
	if in.Sport == "" || len(f.EventID) == 0 {
		return nil, listEventOffersResult{}, fmt.Errorf("sport and event_id (or event_ids) are required")
	}

	offers, err := s.client.AllOffers(ctx, f)
	if err != nil {
		return nil, listEventOffersResult{}, err
	}

	limit := int(in.Limit)
	if limit <= 0 {
		limit = 100
	}
	truncated := len(offers) > limit
	if truncated {
		offers = offers[:limit]
	}

	out := make([]offer, 0, len(offers))
	for _, o := range offers {
		out = append(out, offerFromStream(o))
	}

	next := "Pass a bet_type verbatim to create_betslip, then place_order against the betslip."
	if truncated {
		next = "More offers exist than limit allowed: narrow with market_type, bet_types or min_liquidity, or raise limit. " + next
	}
	return nil, listEventOffersResult{
		Sport:     in.Sport,
		EventIDs:  f.EventID,
		Count:     len(out),
		Truncated: truncated,
		Offers:    out,
		NextStep:  next,
	}, nil
}

func (s *Server) listOrders(ctx context.Context, _ *mcp.CallToolRequest, in listOrdersInput) (*mcp.CallToolResult, listOrdersResult, error) {
	page := int(in.Page)
	if page == 0 {
		page = 1
	}
	pageSize := int(in.PageSize)
	if pageSize == 0 {
		pageSize = 25
	}
	orders, err := s.client.ListOrders(ctx, in.orderFilter(), page, pageSize)
	if err != nil {
		return nil, listOrdersResult{}, err
	}
	mapped := ordersFromAPI(orders)
	return nil, listOrdersResult{Count: len(mapped), Orders: mapped}, nil
}

func (s *Server) getOrder(ctx context.Context, _ *mcp.CallToolRequest, in getOrderInput) (*mcp.CallToolResult, order, error) {
	if in.RequestUUID != "" {
		o, err := s.client.GetOrderByUUID(ctx, in.RequestUUID)
		if err != nil {
			return nil, order{}, err
		}
		return nil, orderFromAPI(o), nil
	}
	id := int64(in.OrderID)
	if id == 0 {
		return nil, order{}, fmt.Errorf("either order_id or request_uuid is required")
	}
	o, err := s.client.GetOrder(ctx, id)
	if err != nil {
		return nil, order{}, err
	}
	return nil, orderFromAPI(o), nil
}

func (s *Server) listBetslips(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listBetslipsResult, error) {
	ids, err := s.client.ListBetslips(ctx)
	if err != nil {
		return nil, listBetslipsResult{}, err
	}
	if ids == nil {
		ids = []string{}
	}
	return nil, listBetslipsResult{Count: len(ids), BetslipIDs: ids}, nil
}

func (s *Server) getBetslip(ctx context.Context, _ *mcp.CallToolRequest, in getBetslipInput) (*mcp.CallToolResult, betslip, error) {
	if in.BetslipID == "" {
		return nil, betslip{}, fmt.Errorf("betslip_id is required")
	}

	wait := time.Duration(in.WaitSeconds) * time.Second
	var (
		bs  *magicmarkets.Betslip
		err error
	)
	if wait > 0 {
		bs, err = s.client.AwaitQuote(ctx, in.BetslipID, wait, 250*time.Millisecond)
	} else {
		bs, err = s.client.GetBetslip(ctx, in.BetslipID)
	}
	if bs == nil {
		return nil, betslip{}, err
	}
	out := betslipFromAPI(bs)
	if err != nil {
		out.Warning = err.Error()
	}
	return nil, out, nil
}

// ---------- trading tools (opt-in) ----------

func (s *Server) createBetslip(ctx context.Context, _ *mcp.CallToolRequest, in createBetslipInput) (*mcp.CallToolResult, betslip, error) {
	bs, err := s.client.CreateBetslip(ctx, in.request())
	if err != nil {
		return nil, betslip{}, err
	}

	wait := 5 * time.Second
	if in.WaitSeconds != nil {
		wait = time.Duration(*in.WaitSeconds) * time.Second
	}
	var warning string
	if wait > 0 {
		quoted, qerr := s.client.AwaitQuote(ctx, bs.BetslipID, wait, 250*time.Millisecond)
		if quoted != nil {
			bs = quoted
		}
		if qerr != nil {
			warning = qerr.Error()
		}
	}

	out := betslipFromAPI(bs)
	out.Warning = warning
	out.NextStep = "Call place_order with this betslip_id, a price from the price list, and a stake."
	return nil, out, nil
}

func (s *Server) placeOrder(ctx context.Context, _ *mcp.CallToolRequest, in placeOrderInput) (*mcp.CallToolResult, placeOrderResult, error) {
	if in.BetslipID == "" {
		return nil, placeOrderResult{}, fmt.Errorf("betslip_id is required")
	}

	// Look the betslip up: its betslip_type decides how the price is snapped.
	bs, err := s.client.GetBetslip(ctx, in.BetslipID)
	if err != nil {
		return nil, placeOrderResult{}, fmt.Errorf("look up betslip %s: %w", in.BetslipID, err)
	}

	snapped := magicmarkets.SnapOrderPrice(in.Price, bs.BetslipType)

	orderReq := in.request(snapped)

	created, err := s.client.CreateOrder(ctx, orderReq)
	if err != nil {
		// A reused idempotency key means the order already exists.
		if magicmarkets.HasCode(err, magicmarkets.CodeOrderAlreadyCreated) && orderReq.RequestUUID != "" {
			if existing, gerr := s.client.GetOrderByUUID(ctx, orderReq.RequestUUID); gerr == nil {
				return nil, placeOrderResult{
					Order:   orderFromAPI(existing),
					Warning: "this request_uuid had already created an order; returning the existing one",
				}, nil
			}
		}
		return nil, placeOrderResult{}, err
	}

	out := placeOrderResult{Order: orderFromAPI(created)}
	if snapped != in.Price {
		out.PriceSnapped = &priceSnap{
			Requested: in.Price,
			Used:      snapped,
			Reason:    "off-tick price rounded onto the tick schedule",
		}
	}
	return nil, out, nil
}

func (s *Server) closeOrder(ctx context.Context, _ *mcp.CallToolRequest, in closeOrderInput) (*mcp.CallToolResult, closeOrderResult, error) {
	id := int64(in.OrderID)
	if id == 0 {
		return nil, closeOrderResult{}, fmt.Errorf("order_id is required")
	}
	if err := s.client.CloseOrder(ctx, id); err != nil {
		if magicmarkets.HasCode(err, magicmarkets.CodeOrderClosed) {
			return nil, closeOrderResult{}, fmt.Errorf("order %d is already closed or settled", id)
		}
		return nil, closeOrderResult{}, err
	}

	// The close response carries no data, so re-read the order.
	o, err := s.client.GetOrder(ctx, id)
	if err != nil {
		return nil, closeOrderResult{
			OrderID: id,
			Closed:  true,
			Warning: fmt.Sprintf("could not re-read the order: %v", err),
		}, nil
	}
	mapped := orderFromAPI(o)
	return nil, closeOrderResult{OrderID: id, Closed: true, Order: &mapped}, nil
}

func (s *Server) closeAllOrders(ctx context.Context, _ *mcp.CallToolRequest, in closeAllOrdersInput) (*mcp.CallToolResult, closeAllOrdersResult, error) {
	result, err := s.client.CloseAllOrders(ctx, in.Sport, in.EventID)
	if err != nil {
		return nil, closeAllOrdersResult{}, err
	}
	return nil, closeAllOrdersResult{Result: string(result)}, nil
}

func (s *Server) closeBetslip(ctx context.Context, _ *mcp.CallToolRequest, in closeBetslipInput) (*mcp.CallToolResult, closeBetslipResult, error) {
	if in.BetslipID == "" {
		return nil, closeBetslipResult{}, fmt.Errorf("betslip_id is required")
	}
	if err := s.client.CloseBetslip(ctx, in.BetslipID); err != nil {
		if magicmarkets.HasCode(err, magicmarkets.CodeNotFound) {
			return nil, closeBetslipResult{}, fmt.Errorf("betslip %s is unknown, expired or already closed", in.BetslipID)
		}
		return nil, closeBetslipResult{}, err
	}
	return nil, closeBetslipResult{BetslipID: in.BetslipID, Closed: true}, nil
}

func (s *Server) createHeartbeat(ctx context.Context, _ *mcp.CallToolRequest, in createHeartbeatInput) (*mcp.CallToolResult, heartbeat, error) {
	hb, err := s.client.CreateHeartbeat(ctx, int(in.Timeout))
	if err != nil {
		return nil, heartbeat{}, err
	}
	return nil, heartbeatFromAPI(hb), nil
}

func (s *Server) refreshHeartbeat(ctx context.Context, _ *mcp.CallToolRequest, in heartbeatIDInput) (*mcp.CallToolResult, heartbeat, error) {
	if in.HeartbeatID == "" {
		return nil, heartbeat{}, fmt.Errorf("heartbeat_id is required")
	}
	hb, err := s.client.RefreshHeartbeat(ctx, in.HeartbeatID)
	if err != nil {
		return nil, heartbeat{}, err
	}
	return nil, heartbeatFromAPI(hb), nil
}

func (s *Server) listHeartbeats(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listHeartbeatsResult, error) {
	hbs, err := s.client.ListHeartbeats(ctx)
	if err != nil {
		return nil, listHeartbeatsResult{}, err
	}
	mapped := heartbeatsFromAPI(hbs)
	return nil, listHeartbeatsResult{Count: len(mapped), Heartbeats: mapped}, nil
}

func (s *Server) cancelHeartbeat(ctx context.Context, _ *mcp.CallToolRequest, in heartbeatIDInput) (*mcp.CallToolResult, cancelHeartbeatResult, error) {
	if in.HeartbeatID == "" {
		return nil, cancelHeartbeatResult{}, fmt.Errorf("heartbeat_id is required")
	}
	if err := s.client.CancelHeartbeat(ctx, in.HeartbeatID); err != nil {
		return nil, cancelHeartbeatResult{}, err
	}
	return nil, cancelHeartbeatResult{HeartbeatID: in.HeartbeatID, Cancelled: true}, nil
}

// ---------- helpers ----------

func addTool[In, Out any](m *mcp.Server, name, desc string, ann *mcp.ToolAnnotations, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(m, &mcp.Tool{Name: name, Description: desc, Annotations: ann}, h)
}

func hints(readOnly, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: boolPtr(destructive),
		IdempotentHint:  idempotent,
		OpenWorldHint:   boolPtr(openWorld),
	}
}

func boolPtr(v bool) *bool { return &v }

// listRegisteredTools walks tools/list against a throwaway in-memory session so
// ToolNames and tests see the same set Serve would advertise.
func listRegisteredTools(m *mcp.Server) ([]*mcp.Tool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, ct := mcp.NewInMemoryTransports()
	ss, err := m.Connect(ctx, st, nil)
	if err != nil {
		return nil, fmt.Errorf("probe server connect: %w", err)
	}
	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "magicmarkets-cli-probe", Version: "dev"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("probe client connect: %w", err)
	}
	defer cs.Close()

	var tools []*mcp.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// parseCSV splits a comma-separated argument, dropping blanks.
func parseCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// nonEmpty wraps a value in a slice, or returns nil when it is empty.
func nonEmpty(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{s}
}
