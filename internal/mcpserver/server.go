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

// Options configures the MCP server.
type Options struct {
	// AllowTrading registers the tools that create betslips, place orders and
	// close orders. Off by default.
	AllowTrading bool

	// Version is reported to the MCP client.
	Version string

	// SnapshotTimeout bounds how long the event and offer tools wait on the
	// stream before giving up.
	SnapshotTimeout time.Duration
}

// Server wires the API client into an MCP server.
type Server struct {
	client *magicmarkets.Client
	cfg    *config.Config
	opts   Options
}

// New builds the MCP server.
func New(client *magicmarkets.Client, cfg *config.Config, opts Options) *Server {
	if opts.SnapshotTimeout <= 0 {
		opts.SnapshotTimeout = 30 * time.Second
	}
	return &Server{client: client, cfg: cfg, opts: opts}
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
// carry the caller's key in X-Api-Key; that value is used for upstream API
// calls. /health and /live are unauthenticated for probes.
func (s *Server) ServeHTTP(ctx context.Context, addr string) error {
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
func (s *Server) httpHandler() http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		key := strings.TrimSpace(r.Header.Get(magicmarkets.APIKeyHeader))
		if key == "" {
			return nil
		}
		cfg := *s.cfg
		cfg.APIKey = key
		client := magicmarkets.New(cfg.APIURL, key, cfg.Timeout,
			magicmarkets.WithUserAgent("magicmarkets-cli/"+s.opts.Version))
		return New(client, &cfg, s.opts).newMCP()
	}, streamableHTTPOptions())

	mux := http.NewServeMux()
	mux.Handle(HTTPPath, requireAPIKeyHeader(streamable))
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
// X-Api-Key, so we do not need a transport session.
//
// JSONResponse: return a single application/json body rather than
// text/event-stream for tool results.
func streamableHTTPOptions() *mcp.StreamableHTTPOptions {
	return &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	}
}

// requireAPIKeyHeader rejects requests with no X-Api-Key. The key is the
// caller's Magic Markets credential, not a server-side secret.
func requireAPIKeyHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get(magicmarkets.APIKeyHeader)) == "" {
			http.Error(w, "missing "+magicmarkets.APIKeyHeader, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
			"Off-tick order prices are rounded so they never tighten your limit: down for back ('for') orders, "+
			"up for lay ('against') orders. Use this to know the price an order will actually run with.",
		hints(true, false, true, false), s.snapPrice)

	// Read-only: discovery over the stream.
	addTool(m, "list_events",
		"List the events that currently have live prices. "+
			"The REST API has no event-listing endpoint, so this opens the WebSocket, reads the initial "+
			"snapshot and disconnects — it takes a few seconds. The snapshot is not the full fixture list: "+
			"it holds only events the feed is pricing right now. "+
			"Use the returned sport and event_id with list_event_offers to see bet types and prices.",
		hints(true, false, false, true), s.listEvents)
	addTool(m, "list_event_offers",
		"List the priced bet types on one event, with the stake available at each price. "+
			"Opens the WebSocket, registers the event, reads the offer snapshot and disconnects. "+
			"Each offer covers one (sport, event_id, bet_type) triple; back and lay on the same selection are "+
			"separate offers. Pass a returned bet_type verbatim to create_betslip — never construct one by hand. "+
			"Prices are ordered best first and are already on the tick schedule.",
		hints(true, false, false, true), s.listEventOffers)

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
	addTool(m, "place_order",
		"Place an order against a betslip. THIS SPENDS REAL MONEY. "+
			"Requires an existing betslip from create_betslip. The price is snapped onto the tick schedule "+
			"(down for back, up for lay) and the snapped value is what the order runs with. "+
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

	dir := magicmarkets.Back
	if in.BetType != "" {
		dir = magicmarkets.DirectionOf(in.BetType)
	} else if in.Direction == string(magicmarkets.Lay) {
		dir = magicmarkets.Lay
	}

	snapped := magicmarkets.SnapPrice(in.Price, dir)
	return nil, snapPriceResult{
		Requested:    in.Price,
		Direction:    string(dir),
		Snapped:      snapped,
		Tick:         magicmarkets.TickAt(snapped),
		AlreadyValid: magicmarkets.IsOnTick(in.Price),
		ImpliedCents: magicmarkets.ImpliedCents(snapped),
	}, nil
}

func (s *Server) listEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, listEventsResult, error) {
	stream, err := s.dial(ctx)
	if err != nil {
		return nil, listEventsResult{}, err
	}
	defer stream.Close()

	events, err := stream.Snapshot(ctx, s.opts.SnapshotTimeout)
	if err != nil && len(events) == 0 {
		return nil, listEventsResult{}, err
	}

	wanted := map[string]bool{}
	for _, sp := range parseCSV(in.Sport) {
		wanted[strings.ToLower(sp)] = true
	}
	needle := strings.ToLower(in.Search)

	limit := int(in.Limit)
	if limit <= 0 {
		limit = 50
	}

	out := make([]event, 0, limit)
	for _, e := range events {
		if len(wanted) > 0 && !wanted[strings.ToLower(e.Sport)] {
			continue
		}
		if in.InPlay && (e.IRStatus == "" || e.IRStatus == "pre_event") {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(
			e.EventName+" "+e.Home+" "+e.Away+" "+e.CompetitionName+" "+e.EventID), needle) {
			continue
		}
		row := eventFromStream(e)
		out = append(out, row)
		if len(out) >= limit {
			break
		}
	}

	return nil, listEventsResult{
		Count:     len(out),
		TotalSeen: len(events),
		Events:    out,
		NextStep:  "Call list_event_offers with a sport and event_id to see priced bet types.",
	}, nil
}

func (s *Server) listEventOffers(ctx context.Context, _ *mcp.CallToolRequest, in listEventOffersInput) (*mcp.CallToolResult, listEventOffersResult, error) {
	if in.Sport == "" || in.EventID == "" {
		return nil, listEventOffersResult{}, fmt.Errorf("sport and event_id are required")
	}

	stream, err := s.dial(ctx)
	if err != nil {
		return nil, listEventOffersResult{}, err
	}
	defer stream.Close()

	// The register acknowledgement is indistinguishable from the opening
	// dump unless the snapshot is drained first.
	if _, err := stream.Snapshot(ctx, s.opts.SnapshotTimeout); err != nil {
		return nil, listEventOffersResult{}, fmt.Errorf("waiting for initial sync: %w", err)
	}

	offers, err := stream.CollectOffers(ctx, in.Sport, in.EventID, s.opts.SnapshotTimeout)
	if err != nil {
		return nil, listEventOffersResult{}, err
	}

	limit := int(in.Limit)
	if limit <= 0 {
		limit = 100
	}

	out := make([]offer, 0, len(offers))
	for _, o := range offers {
		if in.MarketType != "" && !strings.EqualFold(o.MarketType, in.MarketType) {
			continue
		}
		out = append(out, offerFromStream(o))
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].BetType < out[j].BetType
	})

	return nil, listEventOffersResult{
		Sport:    in.Sport,
		EventID:  in.EventID,
		Count:    len(out),
		Offers:   out,
		NextStep: "Pass a bet_type verbatim to create_betslip, then place_order against the betslip.",
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

	// Look the betslip up so the price is snapped in the correct direction.
	bs, err := s.client.GetBetslip(ctx, in.BetslipID)
	if err != nil {
		return nil, placeOrderResult{}, fmt.Errorf("look up betslip %s: %w", in.BetslipID, err)
	}

	dir := magicmarkets.DirectionOf(bs.BetType)
	snapped := magicmarkets.SnapPrice(in.Price, dir)

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

// dial opens a stream connection for the discovery tools.
func (s *Server) dial(ctx context.Context) (*magicmarkets.Stream, error) {
	return magicmarkets.Dial(ctx, s.cfg.WSURL, s.cfg.APIKey, s.cfg.Lang)
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
