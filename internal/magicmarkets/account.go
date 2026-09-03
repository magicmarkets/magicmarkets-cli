package magicmarkets

import (
	"context"
	"net/url"
)

// GetBalance returns the account balance and the stake on open bets.
func (c *Client) GetBalance(ctx context.Context) (*Balance, error) {
	var out Balance
	if err := c.get(ctx, "/balance/", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetXRates returns exchange rates to USDT.
//
// This is the cheapest authenticated endpoint, which makes it the right way to
// verify an API key: the WebSocket closes silently on a bad key, so always
// check here first.
func (c *Client) GetXRates(ctx context.Context) ([]XRate, error) {
	var out []XRate
	if err := c.get(ctx, "/xrates/", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyKey reports whether the configured API key is accepted.
func (c *Client) VerifyKey(ctx context.Context) error {
	_, err := c.GetXRates(ctx)
	return err
}

// GetBetTypeInfo parses and validates a bet_type string against a sport.
//
// A successful response carries a human-readable description and the win/loss
// payoff grid. A validation_error means the string did not parse.
//
// homeTeam and awayTeam are optional and only affect display labels.
func (c *Client) GetBetTypeInfo(ctx context.Context, sport, betType, homeTeam, awayTeam string) (*BetTypeInfo, error) {
	q := url.Values{}
	if homeTeam != "" {
		q.Set("home_team", homeTeam)
	}
	if awayTeam != "" {
		q.Set("away_team", awayTeam)
	}

	path := "/sports/" + url.PathEscape(sport) + "/bet_types/" + url.PathEscape(betType) + "/"
	var out BetTypeInfo
	if err := c.get(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
