// Package mcpserver exposes the Magic Markets API as MCP tools over stdio, so
// an LLM agent can read prices and manage orders.
//
// Read-only tools are always registered. Tools that spend money — creating
// betslips, placing orders, closing orders — are registered only when trading is
// explicitly enabled, so the default configuration cannot place a bet.
package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"magicmarkets-cli/internal/config"
	"magicmarkets-cli/internal/magicmarkets"
)

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

func (s *Server) getBalance(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	bal, err := s.client.GetBalance(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]any{
		"balance":    stakeMap(&bal.Balance),
		"open_stake": stakeMap(&bal.OpenStake),
		"available":  bal.Balance.Amount - bal.OpenStake.Amount,
	}
	if bal.SmartCredit != nil {
		out["smart_credit"] = stakeMap(bal.SmartCredit)
	}
	return nil, out, nil
}

func (s *Server) getXRates(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	rates, err := s.client.GetXRates(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, rates, nil
}

type getPositionInput struct {
	Sport              string `json:"sport,omitempty" jsonschema:"Sport code filter, e.g. 'fb'."`
	EventID            string `json:"event_id,omitempty" jsonschema:"Event ID filter, e.g. '2026-06-15,1001,2002'."`
	Status             string `json:"status,omitempty" jsonschema:"Comma-separated status filter: open, pending, done, failed."`
	IncludeCashoutInfo bool   `json:"include_cashout_info,omitempty" jsonschema:"Include a cashout valuation (football only)."`
}

func (s *Server) getPosition(ctx context.Context, _ *mcp.CallToolRequest, in getPositionInput) (*mcp.CallToolResult, any, error) {
	filter := magicmarkets.OrderFilter{
		Sport:   nonEmpty(in.Sport),
		EventID: nonEmpty(in.EventID),
		Status:  parseCSV(in.Status),
	}
	pos, err := s.client.GetPosition(ctx, filter, in.IncludeCashoutInfo)
	if err != nil {
		return nil, nil, err
	}
	return nil, pos, nil
}

type validateBetTypeInput struct {
	Sport    string `json:"sport" jsonschema:"Sport code, e.g. 'fb'."`
	BetType  string `json:"bet_type" jsonschema:"Bet type string, e.g. 'for,h'."`
	HomeTeam string `json:"home_team,omitempty" jsonschema:"Home team name, for display labels."`
	AwayTeam string `json:"away_team,omitempty" jsonschema:"Away team name, for display labels."`
}

func (s *Server) validateBetType(ctx context.Context, _ *mcp.CallToolRequest, in validateBetTypeInput) (*mcp.CallToolResult, any, error) {
	if in.Sport == "" || in.BetType == "" {
		return nil, nil, fmt.Errorf("sport and bet_type are required")
	}
	info, err := s.client.GetBetTypeInfo(ctx, in.Sport, in.BetType, in.HomeTeam, in.AwayTeam)
	if err != nil {
		if magicmarkets.HasCode(err, magicmarkets.CodeValidationError) {
			return nil, map[string]any{
				"valid": false,
				"error": err.Error(),
			}, nil
		}
		return nil, nil, err
	}
	return nil, map[string]any{
		"valid":                true,
		"sport":                info.Sport,
		"bet_type":             in.BetType,
		"bet_type_description": info.BetTypeDescription,
		"direction":            string(magicmarkets.DirectionOf(in.BetType)),
		"winloss_grid":         info.WinLossGrid,
	}, nil
}

type snapPriceInput struct {
	Price     float64 `json:"price" jsonschema:"Decimal price, between 1.01 and 1000."`
	BetType   string  `json:"bet_type,omitempty" jsonschema:"Bet type string; its direction decides the rounding."`
	Direction string  `json:"direction,omitempty" jsonschema:"'for' (back, rounds down) or 'against' (lay, rounds up). Defaults to 'for'. Ignored when bet_type is given."`
}

func (s *Server) snapPrice(_ context.Context, _ *mcp.CallToolRequest, in snapPriceInput) (*mcp.CallToolResult, any, error) {
	if in.Price <= 0 {
		return nil, nil, fmt.Errorf("price must be positive")
	}

	dir := magicmarkets.Back
	if in.BetType != "" {
		dir = magicmarkets.DirectionOf(in.BetType)
	} else if in.Direction == string(magicmarkets.Lay) {
		dir = magicmarkets.Lay
	}

	snapped := magicmarkets.SnapPrice(in.Price, dir)
	return nil, map[string]any{
		"requested":     in.Price,
		"direction":     string(dir),
		"snapped":       snapped,
		"tick":          magicmarkets.TickAt(snapped),
		"already_valid": magicmarkets.IsOnTick(in.Price),
		"implied_cents": magicmarkets.ImpliedCents(snapped),
	}, nil
}

type listEventsInput struct {
	Sport  string  `json:"sport,omitempty" jsonschema:"Comma-separated sport codes to keep, e.g. 'fb,tennis'."`
	Search string  `json:"search,omitempty" jsonschema:"Case-insensitive match on event, team or competition name."`
	Limit  float64 `json:"limit,omitempty" jsonschema:"Maximum events to return (default 50)."`
	InPlay bool    `json:"in_play,omitempty" jsonschema:"Only events that are in play."`
}

func (s *Server) listEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, any, error) {
	stream, err := s.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()

	events, err := stream.Snapshot(ctx, s.opts.SnapshotTimeout)
	if err != nil && len(events) == 0 {
		return nil, nil, err
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

	out := make([]map[string]any, 0, limit)
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
		row := map[string]any{
			"sport":            e.Sport,
			"event_id":         e.EventID,
			"event_type":       e.EventType,
			"event_name":       e.EventName,
			"competition_name": e.CompetitionName,
			"country":          e.CompetitionCountry,
			"ir_status":        e.IRStatus,
		}
		if !e.StartTime.IsZero() {
			row["start_time"] = e.StartTime.Format(time.RFC3339)
		}
		if e.EventType == "multirunner" {
			row["runner_count"] = len(e.Teams)
		} else {
			row["home"] = e.Home
			row["away"] = e.Away
		}
		out = append(out, row)
		if len(out) >= limit {
			break
		}
	}

	return nil, map[string]any{
		"count":      len(out),
		"total_seen": len(events),
		"events":     out,
		"next_step":  "Call list_event_offers with a sport and event_id to see priced bet types.",
	}, nil
}

type listEventOffersInput struct {
	Sport      string  `json:"sport" jsonschema:"Sport code, e.g. 'fb'."`
	EventID    string  `json:"event_id" jsonschema:"Event ID, e.g. '2026-06-15,1001,2002'."`
	MarketType string  `json:"market_type,omitempty" jsonschema:"Only this market type, e.g. 'ah'."`
	Limit      float64 `json:"limit,omitempty" jsonschema:"Maximum offers to return (default 100)."`
}

func (s *Server) listEventOffers(ctx context.Context, _ *mcp.CallToolRequest, in listEventOffersInput) (*mcp.CallToolResult, any, error) {
	if in.Sport == "" || in.EventID == "" {
		return nil, nil, fmt.Errorf("sport and event_id are required")
	}

	stream, err := s.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()

	// The register acknowledgement is indistinguishable from the opening
	// dump unless the snapshot is drained first.
	if _, err := stream.Snapshot(ctx, s.opts.SnapshotTimeout); err != nil {
		return nil, nil, fmt.Errorf("waiting for initial sync: %w", err)
	}

	offers, err := stream.CollectOffers(ctx, in.Sport, in.EventID, s.opts.SnapshotTimeout)
	if err != nil {
		return nil, nil, err
	}

	limit := int(in.Limit)
	if limit <= 0 {
		limit = 100
	}

	out := make([]map[string]any, 0, len(offers))
	for _, o := range offers {
		if in.MarketType != "" && !strings.EqualFold(o.MarketType, in.MarketType) {
			continue
		}
		out = append(out, map[string]any{
			"bet_type":    o.BetType,
			"market_type": o.MarketType,
			"in_running":  o.InRunning,
			"prices":      priceLevels(o.PriceList),
			"best_price":  bestPrice(o.PriceList),
		})
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["bet_type"]) < fmt.Sprint(out[j]["bet_type"])
	})

	return nil, map[string]any{
		"sport":     in.Sport,
		"event_id":  in.EventID,
		"count":     len(out),
		"offers":    out,
		"next_step": "Pass a bet_type verbatim to create_betslip, then place_order against the betslip.",
	}, nil
}

type listOrdersInput struct {
	Status    string  `json:"status,omitempty" jsonschema:"Comma-separated: open, pending, done, failed."`
	Sport     string  `json:"sport,omitempty" jsonschema:"Comma-separated sport codes."`
	EventID   string  `json:"event_id,omitempty" jsonschema:"Comma-separated event IDs."`
	OrderType string  `json:"order_type,omitempty" jsonschema:"Comma-separated: normal, lay, parlay."`
	DateFrom  string  `json:"date_from,omitempty" jsonschema:"Start of range, ISO 8601."`
	DateTo    string  `json:"date_to,omitempty" jsonschema:"End of range, ISO 8601."`
	Search    string  `json:"search,omitempty" jsonschema:"Free-text search."`
	Page      float64 `json:"page,omitempty" jsonschema:"Page number (default 1)."`
	PageSize  float64 `json:"page_size,omitempty" jsonschema:"Results per page (default 25)."`
}

func (s *Server) listOrders(ctx context.Context, _ *mcp.CallToolRequest, in listOrdersInput) (*mcp.CallToolResult, any, error) {
	page := int(in.Page)
	if page == 0 {
		page = 1
	}
	pageSize := int(in.PageSize)
	if pageSize == 0 {
		pageSize = 25
	}
	filter := magicmarkets.OrderFilter{
		Status:    parseCSV(in.Status),
		Sport:     parseCSV(in.Sport),
		EventID:   parseCSV(in.EventID),
		OrderType: parseCSV(in.OrderType),
		DateFrom:  in.DateFrom,
		DateTo:    in.DateTo,
		Search:    in.Search,
	}
	orders, err := s.client.ListOrders(ctx, filter, page, pageSize)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"count": len(orders), "orders": orders}, nil
}

type getOrderInput struct {
	OrderID     float64 `json:"order_id,omitempty" jsonschema:"Numeric order ID."`
	RequestUUID string  `json:"request_uuid,omitempty" jsonschema:"The request_uuid used at creation."`
}

func (s *Server) getOrder(ctx context.Context, _ *mcp.CallToolRequest, in getOrderInput) (*mcp.CallToolResult, any, error) {
	if in.RequestUUID != "" {
		order, err := s.client.GetOrderByUUID(ctx, in.RequestUUID)
		if err != nil {
			return nil, nil, err
		}
		return nil, order, nil
	}
	id := int64(in.OrderID)
	if id == 0 {
		return nil, nil, fmt.Errorf("either order_id or request_uuid is required")
	}
	order, err := s.client.GetOrder(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return nil, order, nil
}

func (s *Server) listBetslips(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	ids, err := s.client.ListBetslips(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"count": len(ids), "betslip_ids": ids}, nil
}

type getBetslipInput struct {
	BetslipID   string  `json:"betslip_id" jsonschema:"Betslip ID."`
	WaitSeconds float64 `json:"wait_seconds,omitempty" jsonschema:"Poll up to this long for a quote (default 0)."`
}

func (s *Server) getBetslip(ctx context.Context, _ *mcp.CallToolRequest, in getBetslipInput) (*mcp.CallToolResult, any, error) {
	if in.BetslipID == "" {
		return nil, nil, fmt.Errorf("betslip_id is required")
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
		return nil, nil, err
	}
	out := betslipMap(bs)
	if err != nil {
		out["warning"] = err.Error()
	}
	return nil, out, nil
}

// ---------- trading tools (opt-in) ----------

type createBetslipInput struct {
	Sport         string   `json:"sport" jsonschema:"Sport code, e.g. 'fb'."`
	EventID       string   `json:"event_id" jsonschema:"Event ID, e.g. '2026-06-15,1001,2002'."`
	BetType       string   `json:"bet_type" jsonschema:"Bet type string, copied from list_event_offers."`
	BetslipType   string   `json:"betslip_type,omitempty" jsonschema:"'normal' (default) or 'lay'."`
	UserData      string   `json:"user_data,omitempty" jsonschema:"Opaque tag stored with the betslip (max 512 chars)."`
	ExcludeDanger bool     `json:"exclude_danger,omitempty" jsonschema:"Only quote from sources holding no bets in danger status."`
	WaitSeconds   *float64 `json:"wait_seconds,omitempty" jsonschema:"Poll up to this long for a quote (default 5)."`
}

func (s *Server) createBetslip(ctx context.Context, _ *mcp.CallToolRequest, in createBetslipInput) (*mcp.CallToolResult, any, error) {
	betslipType := in.BetslipType
	if betslipType == "" {
		betslipType = magicmarkets.BetslipNormal
	}
	reqBody := magicmarkets.CreateBetslipRequest{
		Sport:         in.Sport,
		EventID:       in.EventID,
		BetType:       in.BetType,
		BetslipType:   betslipType,
		UserData:      in.UserData,
		ExcludeDanger: in.ExcludeDanger,
	}
	bs, err := s.client.CreateBetslip(ctx, reqBody)
	if err != nil {
		return nil, nil, err
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

	out := betslipMap(bs)
	if warning != "" {
		out["warning"] = warning
	}
	out["next_step"] = "Call place_order with this betslip_id, a price from the price list, and a stake."
	return nil, out, nil
}

type placeOrderInput struct {
	BetslipID         string  `json:"betslip_id" jsonschema:"Betslip to order against."`
	Price             float64 `json:"price" jsonschema:"Desired decimal price."`
	Stake             float64 `json:"stake" jsonschema:"Stake amount in USDT."`
	Duration          float64 `json:"duration,omitempty" jsonschema:"Seconds the order stays open (default 15)."`
	ExchangeMode      string  `json:"exchange_mode,omitempty" jsonschema:"'make_and_take' (default), 'take_only' or 'dark'."`
	RequestUUID       string  `json:"request_uuid,omitempty" jsonschema:"Idempotency key. Strongly recommended."`
	UserData          string  `json:"user_data,omitempty" jsonschema:"Opaque tag stored with the order (max 512 chars)."`
	KeepOpenIR        bool    `json:"keep_open_ir,omitempty" jsonschema:"Keep the order open when the event goes in-play."`
	AcceptPartialFill *bool   `json:"accept_partial_fill,omitempty" jsonschema:"Accept a partial fill (default true)."`
	AcceptBetterPrice *bool   `json:"accept_better_price,omitempty" jsonschema:"Accept a better price (default true)."`
}

func (s *Server) placeOrder(ctx context.Context, _ *mcp.CallToolRequest, in placeOrderInput) (*mcp.CallToolResult, any, error) {
	if in.BetslipID == "" {
		return nil, nil, fmt.Errorf("betslip_id is required")
	}

	// Look the betslip up so the price is snapped in the correct direction.
	bs, err := s.client.GetBetslip(ctx, in.BetslipID)
	if err != nil {
		return nil, nil, fmt.Errorf("look up betslip %s: %w", in.BetslipID, err)
	}

	dir := magicmarkets.DirectionOf(bs.BetType)
	snapped := magicmarkets.SnapPrice(in.Price, dir)

	duration := in.Duration
	if duration == 0 {
		duration = 15
	}
	orderReq := magicmarkets.CreateOrderRequest{
		BetslipID:    in.BetslipID,
		Price:        snapped,
		Stake:        magicmarkets.USDT(in.Stake),
		Duration:     duration,
		ExchangeMode: in.ExchangeMode,
		RequestUUID:  in.RequestUUID,
		UserData:     in.UserData,
		KeepOpenIR:   in.KeepOpenIR,
	}
	if in.AcceptPartialFill != nil && !*in.AcceptPartialFill {
		orderReq.AcceptPartialFill = in.AcceptPartialFill
	}
	if in.AcceptBetterPrice != nil && !*in.AcceptBetterPrice {
		orderReq.AcceptBetterPrice = in.AcceptBetterPrice
	}

	order, err := s.client.CreateOrder(ctx, orderReq)
	if err != nil {
		// A reused idempotency key means the order already exists.
		if magicmarkets.HasCode(err, magicmarkets.CodeOrderAlreadyCreated) && orderReq.RequestUUID != "" {
			if existing, gerr := s.client.GetOrderByUUID(ctx, orderReq.RequestUUID); gerr == nil {
				return nil, map[string]any{
					"order":   existing,
					"warning": "this request_uuid had already created an order; returning the existing one",
				}, nil
			}
		}
		return nil, nil, err
	}

	out := map[string]any{"order": order}
	if snapped != in.Price {
		out["price_snapped"] = map[string]any{
			"requested": in.Price,
			"used":      snapped,
			"reason":    "off-tick price rounded onto the tick schedule",
		}
	}
	return nil, out, nil
}

type closeOrderInput struct {
	OrderID float64 `json:"order_id" jsonschema:"Order ID to cancel."`
}

func (s *Server) closeOrder(ctx context.Context, _ *mcp.CallToolRequest, in closeOrderInput) (*mcp.CallToolResult, any, error) {
	id := int64(in.OrderID)
	if id == 0 {
		return nil, nil, fmt.Errorf("order_id is required")
	}
	if err := s.client.CloseOrder(ctx, id); err != nil {
		if magicmarkets.HasCode(err, magicmarkets.CodeOrderClosed) {
			return nil, nil, fmt.Errorf("order %d is already closed or settled", id)
		}
		return nil, nil, err
	}

	// The close response carries no data, so re-read the order.
	order, err := s.client.GetOrder(ctx, id)
	if err != nil {
		return nil, map[string]any{
			"order_id": id,
			"closed":   true,
			"warning":  fmt.Sprintf("could not re-read the order: %v", err),
		}, nil
	}
	return nil, map[string]any{"order_id": id, "closed": true, "order": order}, nil
}

type closeAllOrdersInput struct {
	Sport   string `json:"sport,omitempty" jsonschema:"Only orders on this sport."`
	EventID string `json:"event_id,omitempty" jsonschema:"Only orders on this event (requires sport)."`
}

func (s *Server) closeAllOrders(ctx context.Context, _ *mcp.CallToolRequest, in closeAllOrdersInput) (*mcp.CallToolResult, any, error) {
	result, err := s.client.CloseAllOrders(ctx, in.Sport, in.EventID)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"result": result}, nil
}

type createHeartbeatInput struct {
	Timeout float64 `json:"timeout" jsonschema:"Seconds before expiry (10-300)."`
}

func (s *Server) createHeartbeat(ctx context.Context, _ *mcp.CallToolRequest, in createHeartbeatInput) (*mcp.CallToolResult, any, error) {
	hb, err := s.client.CreateHeartbeat(ctx, int(in.Timeout))
	if err != nil {
		return nil, nil, err
	}
	return nil, hb, nil
}

type heartbeatIDInput struct {
	HeartbeatID string `json:"heartbeat_id" jsonschema:"Heartbeat ID."`
}

func (s *Server) refreshHeartbeat(ctx context.Context, _ *mcp.CallToolRequest, in heartbeatIDInput) (*mcp.CallToolResult, any, error) {
	if in.HeartbeatID == "" {
		return nil, nil, fmt.Errorf("heartbeat_id is required")
	}
	hb, err := s.client.RefreshHeartbeat(ctx, in.HeartbeatID)
	if err != nil {
		return nil, nil, err
	}
	return nil, hb, nil
}

func (s *Server) listHeartbeats(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	hbs, err := s.client.ListHeartbeats(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"count": len(hbs), "heartbeats": hbs}, nil
}

func (s *Server) cancelHeartbeat(ctx context.Context, _ *mcp.CallToolRequest, in heartbeatIDInput) (*mcp.CallToolResult, any, error) {
	if in.HeartbeatID == "" {
		return nil, nil, fmt.Errorf("heartbeat_id is required")
	}
	if err := s.client.CancelHeartbeat(ctx, in.HeartbeatID); err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"heartbeat_id": in.HeartbeatID, "cancelled": true}, nil
}

// ---------- helpers ----------

func addTool[In any](m *mcp.Server, name, desc string, ann *mcp.ToolAnnotations, h mcp.ToolHandlerFor[In, any]) {
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

func stakeMap(s *magicmarkets.Stake) map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{"currency": s.Currency, "amount": s.Amount}
}

func priceLevels(levels []magicmarkets.PriceLevel) []map[string]any {
	out := make([]map[string]any, 0, len(levels))
	for _, l := range levels {
		out = append(out, map[string]any{
			"price": l.Effective.Price,
			"min":   stakeMap(l.Effective.Min),
			"max":   stakeMap(l.Effective.Max),
		})
	}
	return out
}

func bestPrice(levels []magicmarkets.PriceLevel) any {
	if len(levels) == 0 {
		return nil
	}
	return levels[0].Effective.Price
}

func betslipMap(bs *magicmarkets.Betslip) map[string]any {
	out := map[string]any{
		"betslip_id":           bs.BetslipID,
		"sport":                bs.Sport,
		"event_id":             bs.EventID,
		"bet_type":             bs.BetType,
		"bet_type_description": bs.BetTypeDescription,
		"betslip_type":         bs.BetslipType,
		"is_open":              bs.IsOpen,
		"expires_at":           bs.ExpiresAt().Format(time.RFC3339),
		"expires_in_seconds":   int(time.Until(bs.ExpiresAt()).Seconds()),
		"prices":               priceLevels(bs.PriceList),
		"best_price":           bestPrice(bs.PriceList),
	}
	if bs.Total != nil {
		out["total_available"] = stakeMap(bs.Total)
	}
	if bs.CloseReason != nil && *bs.CloseReason != "" {
		out["close_reason"] = *bs.CloseReason
	}
	if len(bs.Legs) > 0 {
		out["legs"] = bs.Legs
	}
	return out
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
