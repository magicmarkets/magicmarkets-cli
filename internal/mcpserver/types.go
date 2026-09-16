package mcpserver

// MCP-facing types. These are the tool input/output contract — not the Magic
// Markets API models. Map to and from internal/magicmarkets in mapper.go.
//
// Shared records (stake, order, betslip, …) are reused across tools. Tool
// envelopes wrap those records. Keep this graph acyclic so the MCP SDK can
// infer an object output schema; do not embed API types and do not use
// map[string]any.

// ---------- shared records (input and output) ----------

type stake struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}

type rate struct {
	Ccy  string  `json:"ccy"`
	Rate float64 `json:"rate"`
}

type priceLevel struct {
	Price float64 `json:"price"`
	Min   *stake  `json:"min,omitempty"`
	Max   *stake  `json:"max,omitempty"`
}

type parlayLeg struct {
	ID                 int      `json:"id,omitempty"`
	Sport              string   `json:"sport"`
	EventID            string   `json:"event_id"`
	BetType            string   `json:"bet_type"`
	BetTypeDescription string   `json:"bet_type_description,omitempty"`
	Price              *float64 `json:"price,omitempty"`
	Outcome            string   `json:"outcome,omitempty"`
}

// eventSummary is a non-recursive event description (parlay legs, nested
// EventInfo on the API).
type eventSummary struct {
	EventType       string `json:"event_type"`
	EventID         string `json:"event_id,omitempty"`
	EventName       string `json:"event_name"`
	HomeTeam        string `json:"home_team,omitempty"`
	AwayTeam        string `json:"away_team,omitempty"`
	CompetitionName string `json:"competition_name,omitempty"`
	StartTime       string `json:"start_time,omitempty"`
}

type eventInfo struct {
	EventType          string         `json:"event_type"`
	EventID            string         `json:"event_id,omitempty"`
	EventName          string         `json:"event_name"`
	HomeTeam           string         `json:"home_team,omitempty"`
	AwayTeam           string         `json:"away_team,omitempty"`
	CompetitionName    string         `json:"competition_name"`
	CompetitionCountry string         `json:"competition_country"`
	StartTime          string         `json:"start_time,omitempty"`
	Date               string         `json:"date,omitempty"`
	RunnerCount        int            `json:"runner_count,omitempty"`
	Legs               []eventSummary `json:"legs,omitempty"`
}

type bet struct {
	BetID        int64    `json:"bet_id"`
	OrderID      int64    `json:"order_id"`
	Status       string   `json:"status"`
	StatusReason string   `json:"status_reason,omitempty"`
	Sport        string   `json:"sport"`
	EventID      string   `json:"event_id,omitempty"`
	BetType      string   `json:"bet_type"`
	WantPrice    float64  `json:"want_price"`
	GotPrice     *float64 `json:"got_price,omitempty"`
	WantStake    *stake   `json:"want_stake,omitempty"`
	GotStake     *stake   `json:"got_stake,omitempty"`
	ProfitLoss   *stake   `json:"profit_loss,omitempty"`
	ExchangeRole string   `json:"exchange_role,omitempty"`
}

type order struct {
	OrderID            int64       `json:"order_id"`
	OrderType          string      `json:"order_type"`
	BetType            string      `json:"bet_type"`
	BetTypeDescription string      `json:"bet_type_description"`
	Sport              string      `json:"sport"`
	WantPrice          float64     `json:"want_price"`
	WantStake          *stake      `json:"want_stake,omitempty"`
	PlacementTime      string      `json:"placement_time,omitempty"`
	ExpiryTime         string      `json:"expiry_time,omitempty"`
	Closed             bool        `json:"closed"`
	CloseReason        string      `json:"close_reason,omitempty"`
	Event              *eventInfo  `json:"event,omitempty"`
	Bets               []bet       `json:"bets,omitempty"`
	UserData           string      `json:"user_data,omitempty"`
	Status             string      `json:"status"`
	KeepOpenIR         bool        `json:"keep_open_ir"`
	ExchangeMode       string      `json:"exchange_mode,omitempty"`
	Price              *float64    `json:"price,omitempty"`
	Stake              *stake      `json:"stake,omitempty"`
	ProfitLoss         *stake      `json:"profit_loss,omitempty"`
	Legs               []parlayLeg `json:"legs,omitempty"`
}

type heartbeat struct {
	HeartbeatID string `json:"heartbeat_id"`
	ExpiryTime  string `json:"expiry_time"`
}

type betslip struct {
	BetslipID          string       `json:"betslip_id"`
	Sport              string       `json:"sport"`
	EventID            string       `json:"event_id"`
	BetType            string       `json:"bet_type"`
	BetTypeDescription string       `json:"bet_type_description"`
	BetslipType        string       `json:"betslip_type"`
	IsOpen             bool         `json:"is_open"`
	ExpiresAt          string       `json:"expires_at"`
	ExpiresInSeconds   int          `json:"expires_in_seconds"`
	Prices             []priceLevel `json:"prices"`
	BestPrice          *float64     `json:"best_price,omitempty"`
	TotalAvailable     *stake       `json:"total_available,omitempty"`
	CloseReason        string       `json:"close_reason,omitempty"`
	Legs               []parlayLeg  `json:"legs,omitempty"`
	Warning            string       `json:"warning,omitempty"`
	NextStep           string       `json:"next_step,omitempty"`
}

type event struct {
	Sport           string `json:"sport"`
	EventID         string `json:"event_id"`
	EventType       string `json:"event_type"`
	EventName       string `json:"event_name"`
	CompetitionName string `json:"competition_name"`
	Country         string `json:"country"`
	IRStatus        string `json:"ir_status"`
	StartTime       string `json:"start_time,omitempty"`
	Home            string `json:"home,omitempty"`
	Away            string `json:"away,omitempty"`
	RunnerCount     int    `json:"runner_count,omitempty"`
}

type offer struct {
	BetType    string       `json:"bet_type"`
	MarketType string       `json:"market_type"`
	InRunning  bool         `json:"in_running"`
	Prices     []priceLevel `json:"prices"`
	BestPrice  *float64     `json:"best_price,omitempty"`
}

type positionGrid struct {
	CcyCode string      `json:"ccy_code"`
	Values  [][]float64 `json:"values"`
}

type positionTotal struct {
	BetType            string        `json:"bet_type"`
	BetTypeDescription string        `json:"bet_type_description"`
	GotPrice           *float64      `json:"got_price,omitempty"`
	GotStake           *stake        `json:"got_stake,omitempty"`
	UnknownPrice       *float64      `json:"unknown_price,omitempty"`
	UnknownStake       *stake        `json:"unknown_stake,omitempty"`
	PayoffGrid         *positionGrid `json:"payoff_grid,omitempty"`
}

type cashoutInfo struct {
	Allowed          bool          `json:"allowed"`
	Reason           string        `json:"reason,omitempty"`
	Valuation        *stake        `json:"valuation,omitempty"`
	Stake            *stake        `json:"stake,omitempty"`
	SmartCreditDelta *stake        `json:"smart_credit_delta,omitempty"`
	Position         *positionGrid `json:"position,omitempty"`
}

type position struct {
	PayoffGrid     *positionGrid   `json:"payoff_grid,omitempty"`
	Totals         []positionTotal `json:"totals"`
	UnknownBetsNum int             `json:"unknown_bets_num"`
	UnknownGrid    *positionGrid   `json:"unknown_grid,omitempty"`
	Sport          string          `json:"sport"`
	EventID        string          `json:"event_id"`
	Event          *eventInfo      `json:"event,omitempty"`
	Cashout        *cashoutInfo    `json:"cashout_info,omitempty"`
}

// ---------- tool inputs ----------

type getPositionInput struct {
	Sport              string `json:"sport,omitempty" jsonschema:"Sport code filter, e.g. 'fb'."`
	EventID            string `json:"event_id,omitempty" jsonschema:"Event ID filter, e.g. '2026-06-15,1001,2002'."`
	Status             string `json:"status,omitempty" jsonschema:"Comma-separated status filter: open, pending, done, failed."`
	IncludeCashoutInfo bool   `json:"include_cashout_info,omitempty" jsonschema:"Include a cashout valuation (football only)."`
}

type validateBetTypeInput struct {
	Sport    string `json:"sport" jsonschema:"Sport code, e.g. 'fb'."`
	BetType  string `json:"bet_type" jsonschema:"Bet type string, e.g. 'for,h'."`
	HomeTeam string `json:"home_team,omitempty" jsonschema:"Home team name, for display labels."`
	AwayTeam string `json:"away_team,omitempty" jsonschema:"Away team name, for display labels."`
}

type snapPriceInput struct {
	Price     float64 `json:"price" jsonschema:"Decimal price, between 1.01 and 1000."`
	BetType   string  `json:"bet_type,omitempty" jsonschema:"Bet type string; its direction decides the rounding."`
	Direction string  `json:"direction,omitempty" jsonschema:"'for' (back, rounds down) or 'against' (lay, rounds up). Defaults to 'for'. Ignored when bet_type is given."`
}

type listEventsInput struct {
	Sport  string  `json:"sport,omitempty" jsonschema:"Comma-separated sport codes to keep, e.g. 'fb,tennis'."`
	Search string  `json:"search,omitempty" jsonschema:"Case-insensitive match on event, team or competition name."`
	Limit  float64 `json:"limit,omitempty" jsonschema:"Maximum events to return (default 50)."`
	InPlay bool    `json:"in_play,omitempty" jsonschema:"Only events that are in play."`
}

type listEventOffersInput struct {
	Sport      string  `json:"sport" jsonschema:"Sport code, e.g. 'fb'."`
	EventID    string  `json:"event_id" jsonschema:"Event ID, e.g. '2026-06-15,1001,2002'."`
	MarketType string  `json:"market_type,omitempty" jsonschema:"Only this market type, e.g. 'ah'."`
	Limit      float64 `json:"limit,omitempty" jsonschema:"Maximum offers to return (default 100)."`
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

type getOrderInput struct {
	OrderID     float64 `json:"order_id,omitempty" jsonschema:"Numeric order ID."`
	RequestUUID string  `json:"request_uuid,omitempty" jsonschema:"The request_uuid used at creation."`
}

type getBetslipInput struct {
	BetslipID   string  `json:"betslip_id" jsonschema:"Betslip ID."`
	WaitSeconds float64 `json:"wait_seconds,omitempty" jsonschema:"Poll up to this long for a quote (default 0)."`
}

type createBetslipInput struct {
	Sport         string   `json:"sport" jsonschema:"Sport code, e.g. 'fb'."`
	EventID       string   `json:"event_id" jsonschema:"Event ID, e.g. '2026-06-15,1001,2002'."`
	BetType       string   `json:"bet_type" jsonschema:"Bet type string, copied from list_event_offers."`
	BetslipType   string   `json:"betslip_type,omitempty" jsonschema:"'normal' (default) or 'lay'."`
	UserData      string   `json:"user_data,omitempty" jsonschema:"Opaque tag stored with the betslip (max 512 chars)."`
	ExcludeDanger bool     `json:"exclude_danger,omitempty" jsonschema:"Only quote from sources holding no bets in danger status."`
	WaitSeconds   *float64 `json:"wait_seconds,omitempty" jsonschema:"Poll up to this long for a quote (default 5)."`
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

type closeOrderInput struct {
	OrderID float64 `json:"order_id" jsonschema:"Order ID to cancel."`
}

type closeAllOrdersInput struct {
	Sport   string `json:"sport,omitempty" jsonschema:"Only orders on this sport."`
	EventID string `json:"event_id,omitempty" jsonschema:"Only orders on this event (requires sport)."`
}

type createHeartbeatInput struct {
	Timeout float64 `json:"timeout" jsonschema:"Seconds before expiry (10-300)."`
}

type heartbeatIDInput struct {
	HeartbeatID string `json:"heartbeat_id" jsonschema:"Heartbeat ID."`
}

// ---------- tool envelopes ----------

type balanceResult struct {
	Balance     stake   `json:"balance"`
	OpenStake   stake   `json:"open_stake"`
	Available   float64 `json:"available"`
	SmartCredit *stake  `json:"smart_credit,omitempty"`
}

type exchangeRatesResult struct {
	Count int    `json:"count"`
	Rates []rate `json:"rates"`
}

type validateBetTypeResult struct {
	Valid              bool       `json:"valid"`
	Error              string     `json:"error,omitempty"`
	Sport              string     `json:"sport,omitempty"`
	BetType            string     `json:"bet_type,omitempty"`
	BetTypeDescription string     `json:"bet_type_description,omitempty"`
	Direction          string     `json:"direction,omitempty"`
	WinLossGrid        [][]string `json:"winloss_grid,omitempty"`
}

type snapPriceResult struct {
	Requested    float64 `json:"requested"`
	Direction    string  `json:"direction"`
	Snapped      float64 `json:"snapped"`
	Tick         float64 `json:"tick"`
	AlreadyValid bool    `json:"already_valid"`
	ImpliedCents float64 `json:"implied_cents"`
}

type listEventsResult struct {
	Count     int     `json:"count"`
	TotalSeen int     `json:"total_seen"`
	Events    []event `json:"events"`
	NextStep  string  `json:"next_step"`
}

type listEventOffersResult struct {
	Sport    string  `json:"sport"`
	EventID  string  `json:"event_id"`
	Count    int     `json:"count"`
	Offers   []offer `json:"offers"`
	NextStep string  `json:"next_step"`
}

type listOrdersResult struct {
	Count  int     `json:"count"`
	Orders []order `json:"orders"`
}

type listBetslipsResult struct {
	Count      int      `json:"count"`
	BetslipIDs []string `json:"betslip_ids"`
}

type priceSnap struct {
	Requested float64 `json:"requested"`
	Used      float64 `json:"used"`
	Reason    string  `json:"reason"`
}

type placeOrderResult struct {
	Order        order      `json:"order"`
	Warning      string     `json:"warning,omitempty"`
	PriceSnapped *priceSnap `json:"price_snapped,omitempty"`
}

type closeOrderResult struct {
	OrderID int64  `json:"order_id"`
	Closed  bool   `json:"closed"`
	Order   *order `json:"order,omitempty"`
	Warning string `json:"warning,omitempty"`
}

type closeAllOrdersResult struct {
	Result string `json:"result"`
}

type listHeartbeatsResult struct {
	Count      int         `json:"count"`
	Heartbeats []heartbeat `json:"heartbeats"`
}

type cancelHeartbeatResult struct {
	HeartbeatID string `json:"heartbeat_id"`
	Cancelled   bool   `json:"cancelled"`
}
