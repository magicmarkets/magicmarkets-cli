package magicmarkets

import (
	"encoding/json"
	"testing"
)

func validRequest() CreateOrderRequest {
	return CreateOrderRequest{
		BetslipID: "bs-1",
		Price:     2.0,
		Stake:     USDT(10),
		Duration:  15,
	}
}

func TestValidateExchangeMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "unset", mode: "", wantErr: false},
		{name: "make_and_take", mode: ExchangeMakeAndTake, wantErr: false},
		{name: "take_only", mode: ExchangeTakeOnly, wantErr: false},
		{name: "dark", mode: ExchangeDark, wantErr: false},
		// The API dropped the old post-only "make" mode and renamed "take" to
		// "take_only" — both former values must now be rejected client-side
		// rather than sent to an API that no longer accepts them.
		{name: "old make mode rejected", mode: "make", wantErr: true},
		{name: "old take mode rejected", mode: "take", wantErr: true},
		{name: "garbage rejected", mode: "yolo", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			req.ExchangeMode = tt.mode
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCurrentScore(t *testing.T) {
	tests := []struct {
		name    string
		score   *[2]int
		wantErr bool
	}{
		{name: "omitted", score: nil, wantErr: false},
		{name: "zero-zero", score: &[2]int{0, 0}, wantErr: false},
		{name: "in range", score: &[2]int{3, 2}, wantErr: false},
		{name: "negative home rejected", score: &[2]int{-1, 0}, wantErr: true},
		{name: "over max rejected", score: &[2]int{0, 32768}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			req.CurrentScore = tt.score
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCurrentScoreMarshalsAsTuple pins the wire shape: the API now expects
// current_score as a [home, away] integer array, not the string it used to
// accept.
func TestCurrentScoreMarshalsAsTuple(t *testing.T) {
	req := validRequest()
	req.CurrentScore = &[2]int{1, 2}

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, ok := decoded["current_score"].([]any)
	if !ok || len(got) != 2 || got[0] != float64(1) || got[1] != float64(2) {
		t.Errorf("current_score = %#v, want [1, 2]", decoded["current_score"])
	}
}

func TestCurrentScoreOmittedWhenNil(t *testing.T) {
	req := validRequest()

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, present := decoded["current_score"]; present {
		t.Errorf("current_score should be omitted when nil, got %#v", decoded["current_score"])
	}
}
