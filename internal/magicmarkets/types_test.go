package magicmarkets

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReconciledUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want any
	}{
		{name: "live API bool true", in: `true`, want: true},
		{name: "live API bool false", in: `false`, want: false},
		{name: "spec string", in: `"settled"`, want: "settled"},
		{name: "empty string", in: `""`, want: ""},
		{name: "null", in: `null`, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Reconciled
			if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got.value, tt.want) {
				t.Errorf("value = %#v, want %#v", got.value, tt.want)
			}
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(out) != tt.in {
				t.Errorf("Marshal = %s, want %s", out, tt.in)
			}
		})
	}
}

func TestReconciledRejectsNumber(t *testing.T) {
	var got Reconciled
	if err := json.Unmarshal([]byte(`1`), &got); err == nil {
		t.Fatal("expected error for numeric reconciled")
	}
}

func TestBetStatusUnmarshalCapturesReason(t *testing.T) {
	var s BetStatus
	raw := []byte(`{"code": "failed", "reason": "insufficient liquidity"}`)
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.Code != "failed" || s.Reason != "insufficient liquidity" {
		t.Errorf("got Code=%q Reason=%q, want Code=%q Reason=%q", s.Code, s.Reason, "failed", "insufficient liquidity")
	}
}

func TestBetStatusUnmarshalBareString(t *testing.T) {
	var s BetStatus
	if err := json.Unmarshal([]byte(`"success"`), &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.Code != "success" || s.Reason != "" {
		t.Errorf("got Code=%q Reason=%q, want Code=%q Reason=%q", s.Code, s.Reason, "success", "")
	}
}

// TestEventResultUnmarshalsBySport checks that EventResult, which flattens
// the union of five sport-specific spec schemas, correctly picks up each
// sport's own fields regardless of which shape is on the wire.
func TestEventResultUnmarshalsBySport(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want EventResult
	}{
		{
			name: "match (football)",
			in:   `{"ht_home": 1, "ht_away": 0, "ft_home": 2, "ft_away": 1}`,
			want: EventResult{HTHome: intp(1), HTAway: intp(0), FTHome: intp(2), FTAway: intp(1)},
		},
		{
			name: "tennis",
			in:   `{"set1_p1": 6, "set1_p2": 4, "who_retired": null}`,
			want: EventResult{Set1P1: intp(6), Set1P2: intp(4)},
		},
		{
			name: "hockey",
			in:   `{"tp1_home": 1, "tp1_away": 2, "tall_home": 3, "tall_away": 4}`,
			want: EventResult{Tp1Home: intp(1), Tp1Away: intp(2), TallHome: intp(3), TallAway: intp(4)},
		},
		{
			name: "table tennis",
			in:   `{"game1_home": 11, "game1_away": 9}`,
			want: EventResult{Game1Home: intp(11), Game1Away: intp(9)},
		},
		{
			name: "multirunner",
			in:   `{"runner_results": [{"team_id": 5, "position": 1}], "non_runner_count": 2}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got EventResult
			if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if tt.name == "multirunner" {
				if len(got.RunnerResults) != 1 || got.RunnerResults[0].TeamID != 5 || got.RunnerResults[0].Position != 1 {
					t.Errorf("RunnerResults = %#v, want one entry {TeamID:5 Position:1}", got.RunnerResults)
				}
				if got.NonRunnerCount == nil || *got.NonRunnerCount != 2 {
					t.Errorf("NonRunnerCount = %v, want 2", got.NonRunnerCount)
				}
				return
			}
			got.RunnerResults = nil // not exercised by the non-multirunner cases
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func intp(i int) *int { return &i }

func TestBetUnmarshalsBoolReconciled(t *testing.T) {
	// Reproduction of `magicmarkets orders` failing on the live wire shape.
	raw := []byte(`{
		"bet_id": 1,
		"order_id": 5001,
		"order_ccy_rate": 1,
		"status": "success",
		"sport": "fb",
		"bet_type": "for,h",
		"ccy_rate": 1,
		"want_price": 2.0,
		"reconciled": true
	}`)
	var bet Bet
	if err := json.Unmarshal(raw, &bet); err != nil {
		t.Fatalf("Unmarshal bet: %v", err)
	}
	if got, ok := bet.Reconciled.value.(bool); !ok || !got {
		t.Errorf("Reconciled.value = %#v, want true", bet.Reconciled.value)
	}
}
