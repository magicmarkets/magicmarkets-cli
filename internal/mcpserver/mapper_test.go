package mcpserver

import (
	"reflect"
	"testing"
	"time"

	"magicmarkets-cli/internal/magicmarkets"
)

func TestRatesFromAPI(t *testing.T) {
	got := ratesFromAPI([]magicmarkets.XRate{{Ccy: "EUR", Rate: 1.08}})
	want := []rate{{Ccy: "EUR", Rate: 1.08}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ratesFromAPI = %#v, want %#v", got, want)
	}
}

func TestHeartbeatFromAPI(t *testing.T) {
	ts := time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC)
	got := heartbeatFromAPI(&magicmarkets.Heartbeat{HeartbeatID: "hb1", ExpiryTime: ts})
	want := heartbeat{HeartbeatID: "hb1", ExpiryTime: "2026-09-10T02:00:00Z"}
	if got != want {
		t.Errorf("heartbeatFromAPI = %#v, want %#v", got, want)
	}
}

func TestOrderFromAPIOmitsAPIOnlyFields(t *testing.T) {
	id := "2026-06-15,1,2"
	home := "Home"
	o := &magicmarkets.Order{
		OrderID:            9,
		OrderType:          "normal",
		BetType:            "for,h",
		BetTypeDescription: "Home",
		Sport:              "fb",
		WantPrice:          1.9,
		Status:             "open",
		EventInfo: &magicmarkets.EventInfo{
			EventType: "normal",
			EventID:   &id,
			EventName: "Home vs Away",
			HomeTeam:  &home,
			LegEventInfos: []magicmarkets.EventInfo{{
				EventType: "normal",
				EventName: "leg",
			}},
		},
		BetBarValues: map[string]any{"opaque": true},
	}
	got := orderFromAPI(o)
	if got.OrderID != 9 || got.BetType != "for,h" {
		t.Fatalf("orderFromAPI identity = %+v", got)
	}
	if got.Event == nil || got.Event.EventID != id || got.Event.HomeTeam != home {
		t.Fatalf("event mapping = %+v", got.Event)
	}
	if len(got.Event.Legs) != 1 || got.Event.Legs[0].EventName != "leg" {
		t.Fatalf("parlay legs = %+v", got.Event.Legs)
	}
}

func TestCreateBetslipInputMapsToAPIRequest(t *testing.T) {
	in := createBetslipInput{Sport: "fb", EventID: "e1", BetType: "for,h"}
	got := in.request()
	want := magicmarkets.CreateBetslipRequest{
		Sport:       "fb",
		EventID:     "e1",
		BetType:     "for,h",
		BetslipType: magicmarkets.BetslipNormal,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request = %#v, want %#v", got, want)
	}
}

func TestListOrdersInputMapsToOrderFilter(t *testing.T) {
	in := listOrdersInput{Status: "open,done", Sport: "fb"}
	got := in.orderFilter()
	want := magicmarkets.OrderFilter{Status: []string{"open", "done"}, Sport: []string{"fb"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orderFilter = %#v, want %#v", got, want)
	}
}

func TestPlaceOrderInputMapsStakeAndDurationDefault(t *testing.T) {
	in := placeOrderInput{BetslipID: "b1", Stake: 10}
	got := in.request(2.1)
	if got.BetslipID != "b1" || got.Price != 2.1 || got.Duration != 15 {
		t.Fatalf("request = %+v", got)
	}
	if got.Stake != magicmarkets.USDT(10) {
		t.Errorf("stake = %+v, want USDT 10", got.Stake)
	}
}
