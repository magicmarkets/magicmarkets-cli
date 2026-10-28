package magicmarkets

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Market-data request bounds enforced by the API.
const (
	// MaxOfferEvents is how many events one GET /v2/offers/ request may name.
	MaxOfferEvents = 20
	// MaxMarketPage is the largest page GET /v2/events/ and /v2/offers/ return.
	MaxMarketPage = 5000
)

// In-running statuses an event carries, and that EventFilter.IRStatus accepts.
const (
	IRPreEvent  = "pre_event"
	IRInRunning = "in_running"
)

// EventFilter narrows GET /v2/events/. The zero value lists every priced event.
type EventFilter struct {
	Sport []string
	// CompetitionID and EventID require Sport; the API returns 400 otherwise.
	CompetitionID []int
	EventID       []string
	// IRStatus is IRPreEvent, IRInRunning, or empty for both.
	IRStatus string
	// StartFrom and StartTo bound start_time as a half-open window
	// [StartFrom, StartTo). An event with no start time is never inside one.
	StartFrom time.Time
	StartTo   time.Time
	// Lang is the language for competition and team names: en, zh-hans, ko.
	Lang string
}

func (f EventFilter) values() (url.Values, error) {
	if (len(f.CompetitionID) > 0 || len(f.EventID) > 0) && len(f.Sport) == 0 {
		return nil, fmt.Errorf("filtering events by competition or event ID requires a sport")
	}
	q := url.Values{}
	for _, v := range f.Sport {
		q.Add("sport", v)
	}
	for _, v := range f.CompetitionID {
		q.Add("competition_id", strconv.Itoa(v))
	}
	for _, v := range f.EventID {
		q.Add("event_id", v)
	}
	if f.IRStatus != "" {
		q.Set("ir_status", f.IRStatus)
	}
	if !f.StartFrom.IsZero() {
		q.Set("start_time_from", f.StartFrom.UTC().Format(time.RFC3339))
	}
	if !f.StartTo.IsZero() {
		q.Set("start_time_to", f.StartTo.UTC().Format(time.RFC3339))
	}
	if f.Lang != "" {
		q.Set("lang", f.Lang)
	}
	return q, nil
}

// EventCursor is the paging cursor GET /v2/events/ expects in after: the
// previous page's last "{sport},{event_id}".
func EventCursor(e StreamEvent) string {
	return e.Sport + "," + e.EventID
}

// ListEvents returns one page of the events that currently have prices, in
// (sport, event_id) order. Pass "" as after for the first page, then
// EventCursor of the previous page's last entry. A page shorter than limit is
// the last one.
//
// Each entry is the object the stream sends as ["event", {...}], so it decodes
// into StreamEvent.
func (c *Client) ListEvents(ctx context.Context, f EventFilter, limit int, after string) ([]StreamEvent, error) {
	q, err := f.values()
	if err != nil {
		return nil, err
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(min(limit, MaxMarketPage)))
	}
	if after != "" {
		q.Set("after", after)
	}
	var out []StreamEvent
	if err := c.get(ctx, "/events/", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EachEvent pages through every event matching f, starting after the cursor
// after ("" for the beginning), and calls fn for each one until fn returns
// false or the list ends.
func (c *Client) EachEvent(ctx context.Context, f EventFilter, pageSize int, after string, fn func(StreamEvent) bool) error {
	if pageSize <= 0 || pageSize > MaxMarketPage {
		pageSize = MaxMarketPage
	}
	for {
		page, err := c.ListEvents(ctx, f, pageSize, after)
		if err != nil {
			return err
		}
		for _, e := range page {
			if !fn(e) {
				return nil
			}
		}
		if len(page) < pageSize {
			return nil
		}
		after = EventCursor(page[len(page)-1])
	}
}

// OfferFilter selects the offers GET /v2/offers/ returns. Sport and at least
// one EventID are required.
type OfferFilter struct {
	Sport string
	// EventID names the events to price. ListOffers accepts at most
	// MaxOfferEvents; AllOffers splits a longer list across requests.
	EventID []string
	// BetType keeps only these markets, in the same form POST /v2/betslips/
	// takes.
	BetType []string
	// MarketType keeps only these market families, e.g. ah, ou, wdw.
	MarketType []string
	// MinLiquidity, in USDT, keeps a market only when its max stakes summed
	// across the price list reach it. Zero disables the filter.
	MinLiquidity float64
}

func (f OfferFilter) values() (url.Values, error) {
	if f.Sport == "" || len(f.EventID) == 0 {
		return nil, fmt.Errorf("sport and at least one event ID are required")
	}
	if len(f.EventID) > MaxOfferEvents {
		return nil, fmt.Errorf("at most %d events per offers request, got %d", MaxOfferEvents, len(f.EventID))
	}
	q := url.Values{}
	q.Set("sport", f.Sport)
	for _, v := range f.EventID {
		q.Add("event_id", v)
	}
	for _, v := range f.BetType {
		q.Add("bet_type", v)
	}
	for _, v := range f.MarketType {
		q.Add("market_type", v)
	}
	if f.MinLiquidity > 0 {
		q.Set("min_liquidity", strconv.FormatFloat(f.MinLiquidity, 'f', -1, 64))
	}
	return q, nil
}

// OfferCursor is the paging cursor GET /v2/offers/ expects in after: the
// previous page's last "{event_id},{bet_type}".
func OfferCursor(o Offer) string {
	return o.EventID + "," + o.BetType
}

// ListOffers returns one page of offers for up to MaxOfferEvents events, in
// (event_id, bet_type) order. Markets with no available stake are left out,
// as is any event the feed has no prices for.
//
// The REST view can trail the stream by about four seconds; register the
// event on the stream when live prices matter.
func (c *Client) ListOffers(ctx context.Context, f OfferFilter, limit int, after string) ([]Offer, error) {
	q, err := f.values()
	if err != nil {
		return nil, err
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(min(limit, MaxMarketPage)))
	}
	if after != "" {
		q.Set("after", after)
	}
	var out []Offer
	if err := c.get(ctx, "/offers/", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AllOffers returns every offer matching f, paging each request to the end
// and splitting EventID into batches of MaxOfferEvents.
func (c *Client) AllOffers(ctx context.Context, f OfferFilter) ([]Offer, error) {
	if f.Sport == "" || len(f.EventID) == 0 {
		return nil, fmt.Errorf("sport and at least one event ID are required")
	}
	var out []Offer
	ids := f.EventID
	for len(ids) > 0 {
		batch := f
		batch.EventID = ids[:min(len(ids), MaxOfferEvents)]
		ids = ids[len(batch.EventID):]

		after := ""
		for {
			page, err := c.ListOffers(ctx, batch, MaxMarketPage, after)
			if err != nil {
				return nil, err
			}
			out = append(out, page...)
			if len(page) < MaxMarketPage {
				break
			}
			after = OfferCursor(page[len(page)-1])
		}
	}
	return out, nil
}
