package magicmarkets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Exchange modes control how an order interacts with the exchange. There is
// no post-only mode: no order type rests in the book without also taking
// crossing liquidity.
const (
	// ExchangeMakeAndTake fills against the best available liquidity first;
	// any remaining stake is advertised at your price and keeps taking newly
	// available liquidity. This is the default.
	ExchangeMakeAndTake = "make_and_take"
	// ExchangeTakeOnly only consumes available liquidity; remaining stake is
	// never advertised and other orders cannot match against it.
	ExchangeTakeOnly = "take_only"
	// ExchangeDark behaves like ExchangeMakeAndTake, but the advertised
	// remaining stake is hidden from other customers until their price
	// crosses yours.
	ExchangeDark = "dark"
)

// Order statuses.
const (
	StatusOpen    = "open"
	StatusPending = "pending"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// CreateOrderRequest is the body of POST /v2/orders/.
type CreateOrderRequest struct {
	BetslipID string `json:"betslip_id"`

	// Price is the desired decimal price. Off-tick prices are snapped down for
	// back orders and up for lay orders; see SnapPrice.
	Price float64 `json:"price"`

	Stake Stake `json:"stake"`

	// Duration is how long the order stays open, in seconds.
	Duration float64 `json:"duration"`

	ExchangeMode string `json:"exchange_mode,omitempty"`

	// KeepOpenIR keeps the order open when the event goes in-play.
	KeepOpenIR bool `json:"keep_open_ir,omitempty"`

	UserData string `json:"user_data,omitempty"`

	// RequestUUID is an idempotency key. Retrying with the same UUID will not
	// create a duplicate order, and the order stays retrievable by UUID for
	// six hours.
	RequestUUID string `json:"request_uuid,omitempty"`

	// AcceptPartialFill and AcceptBetterPrice default to true server-side, so
	// they are only sent when explicitly set.
	AcceptPartialFill *bool  `json:"accept_partial_fill,omitempty"`
	AcceptBetterPrice *bool  `json:"accept_better_price,omitempty"`
	ForceWantPrice    bool   `json:"force_want_price,omitempty"`
	MinTakerWantStake *Stake `json:"min_taker_want_stake,omitempty"`

	// CurrentScore is a placement-time [home, away] score assertion. When
	// set, the order is rejected unless it matches the live score the
	// exchange holds for the event — use it to avoid placing on a price that
	// has not reacted to a goal yet. Omit to skip the check.
	CurrentScore *[2]int `json:"current_score,omitempty"`

	ExcludeDanger bool `json:"exclude_danger,omitempty"`

	// BookieMinStakes gives optional per-source minimum stakes, keyed by
	// source. A source is only used if it can take at least its minimum.
	BookieMinStakes map[string]Stake `json:"bookie_min_stakes,omitempty"`

	// PlacerType is an optional caller-supplied tag recorded against the order.
	PlacerType string `json:"placer_type,omitempty"`
}

// Validate checks the request before it reaches the API.
func (r *CreateOrderRequest) Validate() error {
	if r.BetslipID == "" {
		return fmt.Errorf("betslip_id is required")
	}
	if r.Price < MinPrice || r.Price > MaxPrice {
		return fmt.Errorf("price %g is outside the valid range %g–%g", r.Price, MinPrice, MaxPrice)
	}
	if r.Stake.Amount <= 0 {
		return fmt.Errorf("stake must be positive, got %g", r.Stake.Amount)
	}
	if r.Stake.Currency == "" {
		return fmt.Errorf("stake currency is required")
	}
	if r.Duration <= 0 {
		return fmt.Errorf("duration must be positive, got %g", r.Duration)
	}
	switch r.ExchangeMode {
	case "", ExchangeMakeAndTake, ExchangeTakeOnly, ExchangeDark:
	default:
		return fmt.Errorf("invalid exchange_mode %q (want %s, %s or %s)",
			r.ExchangeMode, ExchangeMakeAndTake, ExchangeTakeOnly, ExchangeDark)
	}
	if r.CurrentScore != nil {
		for _, s := range r.CurrentScore {
			if s < 0 || s > 32767 {
				return fmt.Errorf("current_score values must be between 0 and 32767, got %v", *r.CurrentScore)
			}
		}
	}
	return nil
}

// OrderFilter holds the query parameters shared by the order list and position
// endpoints.
type OrderFilter struct {
	Status    []string
	Sport     []string
	EventID   []string
	OrderType []string
	DateFrom  string
	DateTo    string
	Search    string
}

func (f OrderFilter) values() url.Values {
	q := url.Values{}
	for _, v := range f.Status {
		q.Add("status", v)
	}
	for _, v := range f.Sport {
		q.Add("sport", v)
	}
	for _, v := range f.EventID {
		q.Add("event_id", v)
	}
	for _, v := range f.OrderType {
		q.Add("order_type", v)
	}
	if f.DateFrom != "" {
		q.Set("date_from", f.DateFrom)
	}
	if f.DateTo != "" {
		q.Set("date_to", f.DateTo)
	}
	if f.Search != "" {
		q.Set("search", f.Search)
	}
	return q
}

// CreateOrder places an order against an existing betslip.
func (c *Client) CreateOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out Order
	if err := c.post(ctx, "/orders/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOrders returns a page of orders matching the filter.
func (c *Client) ListOrders(ctx context.Context, f OrderFilter, page, pageSize int) ([]Order, error) {
	q := f.values()
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if pageSize > 0 {
		q.Set("page_size", strconv.Itoa(pageSize))
	}
	var out []Order
	if err := c.get(ctx, "/orders/", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetOrder retrieves a single order.
func (c *Client) GetOrder(ctx context.Context, orderID int64) (*Order, error) {
	var out Order
	if err := c.get(ctx, "/orders/"+strconv.FormatInt(orderID, 10)+"/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetOrderByUUID retrieves an order by the request_uuid it was created with.
// Works for up to six hours after placement.
func (c *Client) GetOrderByUUID(ctx context.Context, uuid string) (*Order, error) {
	var out Order
	if err := c.get(ctx, "/orders/tracked/"+url.PathEscape(uuid)+"/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OrderUpdates returns orders updated within a time window.
//
// Both bounds must be at least 60 seconds in the past and no more than 70
// minutes apart — the API rejects anything else.
func (c *Client) OrderUpdates(ctx context.Context, from, to time.Time) ([]Order, error) {
	if !to.After(from) {
		return nil, fmt.Errorf("updated_at_to must be after updated_at_from")
	}
	if to.Sub(from) > 70*time.Minute {
		return nil, fmt.Errorf("window is %s; the API allows at most 70 minutes", to.Sub(from).Round(time.Second))
	}
	if time.Since(to) < 60*time.Second {
		return nil, fmt.Errorf("updated_at_to must be at least 60 seconds in the past")
	}

	q := url.Values{}
	q.Set("updated_at_from", from.UTC().Format(time.RFC3339))
	q.Set("updated_at_to", to.UTC().Format(time.RFC3339))

	var out []Order
	if err := c.get(ctx, "/orders/updates/", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CloseOrder cancels a single open order.
//
// The response carries no data — the order's lifecycle update is delivered on
// the WebSocket as an ["order", ...] entry with closed: true. Call GetOrder
// afterwards to read the final state.
//
// An order that exists but is already closed or settled returns an APIError
// with CodeOrderClosed, which is distinct from CodeNotFound.
func (c *Client) CloseOrder(ctx context.Context, orderID int64) error {
	return c.post(ctx, "/orders/"+strconv.FormatInt(orderID, 10)+"/close/", nil, nil)
}

// CloseManyOrders cancels up to 500 orders by ID.
func (c *Client) CloseManyOrders(ctx context.Context, orderIDs []int64) (json.RawMessage, error) {
	if len(orderIDs) == 0 {
		return nil, fmt.Errorf("no order IDs given")
	}
	if len(orderIDs) > 500 {
		return nil, fmt.Errorf("close_many accepts at most 500 order IDs, got %d", len(orderIDs))
	}
	body := struct {
		OrderIDs []int64 `json:"order_ids"`
	}{OrderIDs: orderIDs}

	var out json.RawMessage
	if err := c.post(ctx, "/orders/close_many/", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CloseAllOrders cancels every open order, optionally narrowed to one sport or
// one event. Narrowing by event requires the sport too.
func (c *Client) CloseAllOrders(ctx context.Context, sport, eventID string) (json.RawMessage, error) {
	if eventID != "" && sport == "" {
		return nil, fmt.Errorf("closing by event requires a sport as well")
	}
	body := struct {
		Sport   string `json:"sport,omitempty"`
		EventID string `json:"event_id,omitempty"`
	}{Sport: sport, EventID: eventID}

	var out json.RawMessage
	if err := c.post(ctx, "/orders/close_all/", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPosition computes the aggregate P&L position over the matching orders.
//
// includeCashout adds a cashout valuation, which is offered on football only.
func (c *Client) GetPosition(ctx context.Context, f OrderFilter, includeCashout bool) (*Position, error) {
	q := f.values()
	if includeCashout {
		q.Set("include_cashout_info", "true")
	}
	var out Position
	if err := c.get(ctx, "/orders/position/", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
