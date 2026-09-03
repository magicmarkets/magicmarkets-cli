package magicmarkets

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Stake is a [currency, amount] tuple. Stakes in responses are always USDT.
//
// On the wire it is a two-element JSON array — ["USDT", 115.38] — so it needs
// custom marshalling to and from a Go struct.
type Stake struct {
	Currency string
	Amount   float64
}

// USDT builds a stake in USDT, the currency all API stakes use.
func USDT(amount float64) Stake {
	return Stake{Currency: "USDT", Amount: amount}
}

func (s Stake) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{s.Currency, s.Amount})
}

func (s *Stake) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("stake: expected [currency, amount] array: %w", err)
	}
	if len(raw) != 2 {
		return fmt.Errorf("stake: expected 2 elements, got %d", len(raw))
	}
	if err := json.Unmarshal(raw[0], &s.Currency); err != nil {
		return fmt.Errorf("stake currency: %w", err)
	}
	if err := json.Unmarshal(raw[1], &s.Amount); err != nil {
		return fmt.Errorf("stake amount: %w", err)
	}
	return nil
}

// String renders the stake for display, e.g. "115.38 USDT".
func (s Stake) String() string {
	return strconv.FormatFloat(s.Amount, 'f', 2, 64) + " " + s.Currency
}

// StakeString renders a possibly-absent stake, using "-" for nil.
func StakeString(s *Stake) string {
	if s == nil {
		return "-"
	}
	return s.String()
}

// PriceLevel is one entry of a price list: the stake available at one price.
type PriceLevel struct {
	Effective struct {
		// Price is the decimal price, always already on the tick schedule.
		Price float64 `json:"price"`
		// Min is the minimum stake accepted here, or nil for no minimum.
		Min *Stake `json:"min"`
		// Max is the total stake available at this price.
		Max *Stake `json:"max"`
	} `json:"effective"`
}

// ParlayLeg is one selection within an accumulator.
type ParlayLeg struct {
	ID                 int      `json:"id,omitempty"`
	Sport              string   `json:"sport"`
	EventID            string   `json:"event_id"`
	BetType            string   `json:"bet_type"`
	BetTypeDescription string   `json:"bet_type_description,omitempty"`
	Price              *float64 `json:"price,omitempty"`
	// Outcome is won, lost, void, push, or empty while undecided.
	Outcome string `json:"outcome,omitempty"`
}

// EventResult holds a match or race result. Its shape depends on the sport —
// the API models it as a oneOf across five sport-specific schemas
// (EventResultMatch, EventResultTennis, EventResultHockey,
// EventResultTableTennis, EventResultMultirunner) — so EventResult flattens
// their union rather than making callers branch on sport before reading a
// field. Only the fields relevant to the event's sport are populated.
type EventResult struct {
	// Football and other two-half sports. HT is omitted for single-period sports.
	HTHome *int `json:"ht_home"`
	HTAway *int `json:"ht_away"`
	FTHome *int `json:"ft_home"`
	FTAway *int `json:"ft_away"`

	// Tennis. SetNPM is player M's game count in set N.
	Set1P1     *int `json:"set1_p1"`
	Set1P2     *int `json:"set1_p2"`
	Set2P1     *int `json:"set2_p1"`
	Set2P2     *int `json:"set2_p2"`
	Set3P1     *int `json:"set3_p1"`
	Set3P2     *int `json:"set3_p2"`
	Set4P1     *int `json:"set4_p1"`
	Set4P2     *int `json:"set4_p2"`
	Set5P1     *int `json:"set5_p1"`
	Set5P2     *int `json:"set5_p2"`
	WhoRetired *int `json:"who_retired"`

	// Ice hockey. TpN is period N's score, Tall the regulation total, Pen the
	// penalty-shootout score.
	Tp1Home  *int `json:"tp1_home"`
	Tp1Away  *int `json:"tp1_away"`
	Tp2Home  *int `json:"tp2_home"`
	Tp2Away  *int `json:"tp2_away"`
	Tp3Home  *int `json:"tp3_home"`
	Tp3Away  *int `json:"tp3_away"`
	TallHome *int `json:"tall_home"`
	TallAway *int `json:"tall_away"`
	PenHome  *int `json:"pen_home"`
	PenAway  *int `json:"pen_away"`

	// Table tennis. GameN is the point score in game N (up to 7 games).
	Game1Home *int `json:"game1_home"`
	Game1Away *int `json:"game1_away"`
	Game2Home *int `json:"game2_home"`
	Game2Away *int `json:"game2_away"`
	Game3Home *int `json:"game3_home"`
	Game3Away *int `json:"game3_away"`
	Game4Home *int `json:"game4_home"`
	Game4Away *int `json:"game4_away"`
	Game5Home *int `json:"game5_home"`
	Game5Away *int `json:"game5_away"`
	Game6Home *int `json:"game6_home"`
	Game6Away *int `json:"game6_away"`
	Game7Home *int `json:"game7_home"`
	Game7Away *int `json:"game7_away"`

	// Multirunner (outright) events. RunnerResults holds finishing positions:
	// 1=first, 2=second, 0=unknown, -1=void, -2=non-runner, -3=eliminated.
	RunnerResults []struct {
		TeamID   int `json:"team_id"`
		Position int `json:"position"`
	} `json:"runner_results"`
	NonRunnerCount *int `json:"non_runner_count"`
}

// Runner is one competitor in a multirunner (outright) event.
type Runner struct {
	TeamID int    `json:"team_id"`
	Name   string `json:"name"`
}

// EventInfo describes the event an order or position sits on.
type EventInfo struct {
	// EventType is normal, multirunner, or parlay.
	EventType          string       `json:"event_type"`
	EventID            *string      `json:"event_id"`
	EventName          string       `json:"event_name"`
	HomeID             *int         `json:"home_id"`
	HomeTeam           *string      `json:"home_team"`
	AwayID             *int         `json:"away_id"`
	AwayTeam           *string      `json:"away_team"`
	CompetitionID      int          `json:"competition_id"`
	CompetitionName    string       `json:"competition_name"`
	CompetitionCountry string       `json:"competition_country"`
	StartTime          time.Time    `json:"start_time"`
	Date               string       `json:"date"`
	Result             *EventResult `json:"result"`
	// Teams is the runner list, multirunner events only.
	Teams []Runner `json:"teams"`
	// EndTime is the race end time, multirunner events only.
	EndTime *time.Time `json:"end_time"`
	// LegEventInfos holds sub-event info per leg, parlay orders only.
	LegEventInfos []EventInfo `json:"leg_event_infos"`
}

// Betslip registers interest in one selection and carries its live quote.
//
// The POST response has no prices; poll GET or watch the stream for the quote.
type Betslip struct {
	BetslipID          string `json:"betslip_id"`
	Sport              string `json:"sport"`
	EventID            string `json:"event_id"`
	BetType            string `json:"bet_type"`
	BetTypeDescription string `json:"bet_type_description"`
	// ExpiryTS is a Unix timestamp; betslips are short-lived.
	ExpiryTS         float64     `json:"expiry_ts"`
	IsOpen           bool        `json:"is_open"`
	CloseReason      *string     `json:"close_reason"`
	EquivalentBets   bool        `json:"equivalent_bets"`
	CustomerUsername string      `json:"customer_username"`
	CustomerCcy      string      `json:"customer_ccy"`
	BetslipType      string      `json:"betslip_type"`
	Legs             []ParlayLeg `json:"legs"`
	UserData         *string     `json:"user_data"`

	// PriceList is sorted best price first. Empty until quotes arrive, or
	// when no source is currently quoting. Absent on the create response.
	PriceList []PriceLevel `json:"price_list"`
	// Total is the sum of max stakes across all price levels.
	Total *Stake `json:"total"`
}

// ExpiresAt returns the betslip expiry as a time.
func (b *Betslip) ExpiresAt() time.Time {
	sec, frac := int64(b.ExpiryTS), b.ExpiryTS-float64(int64(b.ExpiryTS))
	return time.Unix(sec, int64(frac*1e9))
}

// BestPrice returns the best available price, or 0 when unquoted.
func (b *Betslip) BestPrice() float64 {
	if len(b.PriceList) == 0 {
		return 0
	}
	return b.PriceList[0].Effective.Price
}

// BetStatus is a bet's status, which the API sends either as a bare string or
// as an object carrying a code and the quote that was responded to.
type BetStatus struct {
	Code string
	// Reason is a human-readable failure explanation, present only when Code
	// is "failed".
	Reason string
	// ResponsePMM is the private quote the bet was matched against, when the
	// API sends the object form.
	ResponsePMM *PriceLevel
}

func (s *BetStatus) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		s.Code = str
		return nil
	}
	var obj struct {
		Code        string      `json:"code"`
		Reason      string      `json:"reason"`
		ResponsePMM *PriceLevel `json:"response_pmm"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("bet status: %w", err)
	}
	s.Code = obj.Code
	s.Reason = obj.Reason
	s.ResponsePMM = obj.ResponsePMM
	return nil
}

func (s BetStatus) String() string { return s.Code }

// Reconciled is a bet's reconciliation flag.
//
// The spec types this as bool|null. It used to document string|null while the
// API actually sent a boolean; that mismatch is fixed upstream now, but
// accepting either keeps this client tolerant of the same kind of drift in
// the future.
type Reconciled struct {
	value any // bool, string, or nil
}

func (r *Reconciled) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*r = Reconciled{}
		return nil
	}
	var flag bool
	if err := json.Unmarshal(b, &flag); err == nil {
		r.value = flag
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		r.value = s
		return nil
	}
	return fmt.Errorf("reconciled: expected bool, string, or null")
}

func (r Reconciled) MarshalJSON() ([]byte, error) {
	if r.value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(r.value)
}

// Bet is an individual bet within an order.
type Bet struct {
	BetID        int64      `json:"bet_id"`
	OrderID      int64      `json:"order_id"`
	OrderCcyRate float64    `json:"order_ccy_rate"`
	Status       BetStatus  `json:"status"`
	Sport        string     `json:"sport"`
	EventID      *string    `json:"event_id"`
	BetType      string     `json:"bet_type"`
	CcyRate      float64    `json:"ccy_rate"`
	WantPrice    float64    `json:"want_price"`
	GotPrice     *float64   `json:"got_price"`
	WantStake    *Stake     `json:"want_stake"`
	GotStake     *Stake     `json:"got_stake"`
	ProfitLoss   *Stake     `json:"profit_loss"`
	Reconciled   Reconciled `json:"reconciled"`
	// ExchangeRole is maker, taker, or nil.
	ExchangeRole *string `json:"exchange_role"`
}

// Order commits a stake against a betslip's quote.
type Order struct {
	OrderID int64 `json:"order_id"`
	// OrderType is normal, lay, or parlay.
	OrderType          string    `json:"order_type"`
	BetType            string    `json:"bet_type"`
	BetTypeDescription string    `json:"bet_type_description"`
	Sport              string    `json:"sport"`
	WantPrice          float64   `json:"want_price"`
	WantStake          *Stake    `json:"want_stake"`
	CcyRate            float64   `json:"ccy_rate"`
	PlacementTime      time.Time `json:"placement_time"`
	ExpiryTime         time.Time `json:"expiry_time"`
	Closed             bool      `json:"closed"`
	// CloseReason is e.g. filled, expired, cancelled.
	CloseReason *string    `json:"close_reason"`
	EventInfo   *EventInfo `json:"event_info"`
	Bets        []Bet      `json:"bets"`
	UserData    *string    `json:"user_data"`
	// Status is open, pending, done, or failed.
	Status       string  `json:"status"`
	KeepOpenIR   bool    `json:"keep_open_ir"`
	ExchangeMode *string `json:"exchange_mode"`
	// Price is the achieved price, nil while the order is open.
	Price *float64 `json:"price"`
	// Stake is the aggregate matched stake across bets.
	Stake      *Stake      `json:"stake"`
	ProfitLoss *Stake      `json:"profit_loss"`
	Legs       []ParlayLeg `json:"legs"`
	// BetBarValues is an opaque display payload. The spec gives it no shape, so
	// it is carried through untyped rather than dropped, keeping --json output
	// faithful to the API response.
	BetBarValues map[string]any `json:"bet_bar_values,omitempty"`
}

// Balance is the account's money position.
type Balance struct {
	Balance   Stake `json:"balance"`
	OpenStake Stake `json:"open_stake"`
	// SmartCredit is nil when the account has none.
	SmartCredit *Stake `json:"smart_credit"`
}

// Heartbeat is a dead-man's switch: if it expires, all open orders are closed.
type Heartbeat struct {
	HeartbeatID string    `json:"heartbeat_id"`
	ExpiryTime  time.Time `json:"expiry_time"`
}

// XRate is one currency's exchange rate to USDT.
type XRate struct {
	Ccy  string  `json:"ccy"`
	Rate float64 `json:"rate"`
}

// BetTypeInfo is the parsed form of a bet_type string, used to validate it.
type BetTypeInfo struct {
	// Sport is the display name, e.g. "Football".
	Sport              string `json:"sport"`
	BetTypeDescription string `json:"bet_type_description"`
	// WinLossGrid is a 20x20 grid of w / l / p (push) / v (void) outcomes
	// indexed by [home_score][away_score].
	WinLossGrid [][]string `json:"winloss_grid"`
}

// PositionGrid is profit or loss per scoreline in USDT, indexed as
// Values[home_score][away_score].
type PositionGrid struct {
	CcyCode string      `json:"ccy_code"`
	Values  [][]float64 `json:"values"`
}

// PositionTotal is the position for one bet type. Standard bet types carry
// prices; custom bet types carry their own payoff grid instead.
type PositionTotal struct {
	BetTypeDescription string        `json:"bet_type_description"`
	GotPrice           *float64      `json:"got_price"`
	GotStake           *Stake        `json:"got_stake"`
	UnknownPrice       *float64      `json:"unknown_price"`
	UnknownStake       *Stake        `json:"unknown_stake"`
	PayoffGrid         *PositionGrid `json:"payoff_grid"`
}

// CashoutInfo is a cashout valuation, offered on football only. Every value is
// nil when Allowed is false.
type CashoutInfo struct {
	Allowed bool `json:"allowed"`
	// Reason is position_already_flat or insufficient_credit when shareable.
	Reason           *string       `json:"reason"`
	Valuation        *Stake        `json:"valuation"`
	Stake            *Stake        `json:"stake"`
	SmartCreditDelta *Stake        `json:"smart_credit_delta"`
	Position         *PositionGrid `json:"position"`
}

// Position is the aggregate P&L across the orders matched by the filters.
type Position struct {
	// PayoffGrid is nil when every cell is zero.
	PayoffGrid *PositionGrid `json:"payoff_grid"`
	// Totals is keyed by the unflipped bet type string.
	Totals map[string]PositionTotal `json:"totals"`
	// UnknownBetsNum counts bets that could not be projected onto the grid.
	UnknownBetsNum int           `json:"unknown_bets_num"`
	UnknownGrid    *PositionGrid `json:"unknown_grid"`
	Sport          string        `json:"sport"`
	EventID        string        `json:"event_id"`
	EventInfo      *EventInfo    `json:"event_info"`
	CashoutInfo    *CashoutInfo  `json:"cashout_info"`
}
