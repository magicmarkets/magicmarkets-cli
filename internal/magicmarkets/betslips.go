package magicmarkets

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// Betslip types.
const (
	BetslipNormal = "normal"
	BetslipLay    = "lay"
	BetslipParlay = "parlay"
)

// BetslipLeg is one leg of a parlay betslip request.
type BetslipLeg struct {
	Sport   string `json:"sport"`
	EventID string `json:"event_id"`
	BetType string `json:"bet_type"`
}

// CreateBetslipRequest is the body of POST /v2/betslips/.
//
// Sport, EventID and BetType are required for normal and lay betslips; Legs
// (2–10 entries) is required for parlays instead.
type CreateBetslipRequest struct {
	Sport   string `json:"sport,omitempty"`
	EventID string `json:"event_id,omitempty"`
	BetType string `json:"bet_type,omitempty"`

	Legs []BetslipLeg `json:"legs,omitempty"`

	// BetslipType is normal (default), lay, or parlay.
	BetslipType string `json:"betslip_type,omitempty"`

	// EquivalentBets includes equivalent bet types in the quote. Defaults to
	// true server-side, so it is only sent when explicitly set.
	EquivalentBets *bool `json:"equivalent_bets,omitempty"`

	UserData string `json:"user_data,omitempty"`

	// ExcludeDanger restricts quoting to liquidity sources that hold no bets
	// in danger status.
	ExcludeDanger bool `json:"exclude_danger,omitempty"`
}

// Validate checks the request shape before it reaches the API.
func (r *CreateBetslipRequest) Validate() error {
	if r.BetslipType == BetslipParlay {
		if n := len(r.Legs); n < 2 || n > 10 {
			return fmt.Errorf("parlay betslips need 2–10 legs, got %d", n)
		}
		for i, l := range r.Legs {
			if l.Sport == "" || l.EventID == "" || l.BetType == "" {
				return fmt.Errorf("leg %d: sport, event_id and bet_type are all required", i+1)
			}
		}
		return nil
	}
	if r.Sport == "" || r.EventID == "" || r.BetType == "" {
		return fmt.Errorf("sport, event_id and bet_type are required for %s betslips", orDefault(r.BetslipType, BetslipNormal))
	}
	if len(r.Legs) > 0 {
		return fmt.Errorf("legs are only valid on parlay betslips")
	}
	return nil
}

// CreateBetslip registers interest in a selection and returns the betslip.
//
// The response carries no prices — poll GetBetslip or watch the stream's
// ["pmm", ...] messages for the quote.
func (c *Client) CreateBetslip(ctx context.Context, req CreateBetslipRequest) (*Betslip, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out Betslip
	if err := c.post(ctx, "/betslips/", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListBetslips returns the IDs of all open betslips.
func (c *Client) ListBetslips(ctx context.Context) ([]string, error) {
	var out []string
	if err := c.get(ctx, "/betslips/", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetBetslip retrieves a betslip including its current price list.
func (c *Client) GetBetslip(ctx context.Context, betslipID string) (*Betslip, error) {
	var out Betslip
	if err := c.get(ctx, "/betslips/"+url.PathEscape(betslipID)+"/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefreshBetslip extends a betslip's expiry and returns its current state.
//
// The refresh response body is not specified, so the betslip is re-read
// afterwards rather than decoded from the refresh call. That keeps the returned
// state correct whether the API echoes the betslip or returns null.
func (c *Client) RefreshBetslip(ctx context.Context, betslipID string) (*Betslip, error) {
	if err := c.post(ctx, "/betslips/"+url.PathEscape(betslipID)+"/refresh/", nil, nil); err != nil {
		return nil, err
	}
	return c.GetBetslip(ctx, betslipID)
}

// AwaitQuote polls a betslip until it has at least one price, the betslip
// closes, or the timeout elapses.
//
// Quotes arrive asynchronously after creation, so a freshly created betslip is
// normally unpriced. Callers that hold a stream connection should watch for
// ["pmm", ...] instead of polling.
func (c *Client) AwaitQuote(ctx context.Context, betslipID string, timeout, interval time.Duration) (*Betslip, error) {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)

	for {
		bs, err := c.GetBetslip(ctx, betslipID)
		if err != nil {
			return nil, err
		}
		if len(bs.PriceList) > 0 {
			return bs, nil
		}
		if !bs.IsOpen {
			reason := "closed"
			if bs.CloseReason != nil && *bs.CloseReason != "" {
				reason = *bs.CloseReason
			}
			return bs, fmt.Errorf("betslip %s closed before a quote arrived (%s)", betslipID, reason)
		}
		if time.Now().After(deadline) {
			return bs, fmt.Errorf("no quote for betslip %s after %s", betslipID, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
