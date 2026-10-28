package magicmarkets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func stubAPI(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return New(ts.URL, "test-key", time.Second)
}

func writeData(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": data}); err != nil {
		t.Fatal(err)
	}
}

func TestListEventsSendsFilters(t *testing.T) {
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/" {
			t.Errorf("path = %q, want /events/", r.URL.Path)
		}
		q := r.URL.Query()
		checks := map[string][]string{
			"sport":           {"fb", "tn"},
			"competition_id":  {"1"},
			"event_id":        {"2026-06-15,1001,2002"},
			"ir_status":       {IRInRunning},
			"start_time_from": {"2026-06-15T00:00:00Z"},
			"start_time_to":   {"2026-06-16T00:00:00Z"},
			"lang":            {"ko"},
			"limit":           {"10"},
			"after":           {"fb,2026-06-14,1,2"},
		}
		for k, want := range checks {
			if got := q[k]; strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		writeData(t, w, []map[string]any{{
			"event_type": "normal", "sport": "fb", "event_id": "2026-06-15,1001,2002",
			"home": "Arsenal", "away": "Chelsea", "ir_status": "in_running", "start_time": nil,
		}})
	})

	from := time.Date(2026, 6, 15, 8, 0, 0, 0, time.FixedZone("HKT", 8*3600))
	got, err := c.ListEvents(context.Background(), EventFilter{
		Sport:         []string{"fb", "tn"},
		CompetitionID: []int{1},
		EventID:       []string{"2026-06-15,1001,2002"},
		IRStatus:      IRInRunning,
		StartFrom:     from,
		StartTo:       from.Add(24 * time.Hour),
		Lang:          "ko",
	}, 10, "fb,2026-06-14,1,2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Home != "Arsenal" || !got[0].StartTime.IsZero() {
		t.Fatalf("events = %+v", got)
	}
}

func TestListEventsIDFiltersNeedSport(t *testing.T) {
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent; want a local error")
	})
	for _, f := range []EventFilter{{EventID: []string{"2026-06-15,1,2"}}, {CompetitionID: []int{1}}} {
		if _, err := c.ListEvents(context.Background(), f, 0, ""); err == nil {
			t.Errorf("%+v: want error without sport", f)
		}
	}
}

func TestEachEventPagesWithCursor(t *testing.T) {
	var afters []string
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		afters = append(afters, after)
		n := 2 // a full page
		if after == "fb,e2" {
			n = 1 // short page ends the list
		}
		page := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			page = append(page, map[string]any{"sport": "fb", "event_id": fmt.Sprintf("e%d", len(afters)*2-1+i)})
		}
		writeData(t, w, page)
	})

	var seen []string
	err := c.EachEvent(context.Background(), EventFilter{}, 2, "", func(e StreamEvent) bool {
		seen = append(seen, e.EventID)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(afters, "|") != "|fb,e2" {
		t.Errorf("cursors = %q", afters)
	}
	if strings.Join(seen, ",") != "e1,e2,e3" {
		t.Errorf("seen = %v", seen)
	}
}

func TestEachEventStopsWhenFnReturnsFalse(t *testing.T) {
	calls := 0
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeData(t, w, []map[string]any{{"sport": "fb", "event_id": "a"}, {"sport": "fb", "event_id": "b"}})
	})
	_ = c.EachEvent(context.Background(), EventFilter{}, 2, "", func(StreamEvent) bool { return false })
	if calls != 1 {
		t.Errorf("requests = %d, want 1", calls)
	}
}

func TestListOffersSendsFilters(t *testing.T) {
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/offers/" {
			t.Errorf("path = %q, want /offers/", r.URL.Path)
		}
		q := r.URL.Query()
		// bet_type and event_id contain commas: each must arrive as one value.
		if got := q["bet_type"]; len(got) != 2 || got[0] != "for,ah,h,1" || got[1] != "for,h" {
			t.Errorf("bet_type = %q", got)
		}
		if got := q["event_id"]; len(got) != 1 || got[0] != "2026-06-15,1001,2002" {
			t.Errorf("event_id = %q", got)
		}
		if q.Get("sport") != "fb" || q.Get("min_liquidity") != "250.5" || strings.Join(q["market_type"], ",") != "ah,ou" {
			t.Errorf("query = %v", q)
		}
		writeData(t, w, []map[string]any{{
			"sport": "fb", "event_id": "2026-06-15,1001,2002", "bet_type": "for,ah,h,1", "market_type": "ah",
			"price_list": []any{map[string]any{"effective": map[string]any{"price": 2.0, "min": nil, "max": []any{"USDT", 150.0}}}},
		}})
	})
	got, err := c.ListOffers(context.Background(), OfferFilter{
		Sport:        "fb",
		EventID:      []string{"2026-06-15,1001,2002"},
		BetType:      []string{"for,ah,h,1", "for,h"},
		MarketType:   []string{"ah", "ou"},
		MinLiquidity: 250.5,
	}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PriceList[0].Effective.Max.Amount != 150 {
		t.Fatalf("offers = %+v", got)
	}
}

func TestListOffersRejectsTooManyEvents(t *testing.T) {
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request sent") })
	ids := make([]string, MaxOfferEvents+1)
	if _, err := c.ListOffers(context.Background(), OfferFilter{Sport: "fb", EventID: ids}, 0, ""); err == nil {
		t.Error("want error over the per-request event cap")
	}
	if _, err := c.AllOffers(context.Background(), OfferFilter{EventID: []string{"x"}}); err == nil {
		t.Error("want error without sport")
	}
}

func TestAllOffersBatchesEvents(t *testing.T) {
	var batches []int
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["event_id"]
		batches = append(batches, len(ids))
		out := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]any{"sport": "fb", "event_id": id, "bet_type": "for,h"})
		}
		writeData(t, w, out)
	})
	ids := make([]string, 45)
	for i := range ids {
		ids[i] = "2026-06-15,1," + strconv.Itoa(i)
	}
	got, err := c.AllOffers(context.Background(), OfferFilter{Sport: "fb", EventID: ids})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(batches) != "[20 20 5]" {
		t.Errorf("batches = %v, want [20 20 5]", batches)
	}
	if len(got) != 45 {
		t.Errorf("offers = %d, want 45", len(got))
	}
}

func TestCloseBetslip(t *testing.T) {
	c := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/betslips/bs-1/" {
			t.Errorf("%s %s, want DELETE /betslips/bs-1/", r.Method, r.URL.Path)
		}
		writeData(t, w, nil)
	})
	if err := c.CloseBetslip(context.Background(), "bs-1"); err != nil {
		t.Fatal(err)
	}
}
