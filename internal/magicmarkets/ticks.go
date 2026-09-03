package magicmarkets

import (
	"fmt"
	"math"
	"strings"
)

// Direction is the side of a bet, encoded as the first token of a bet_type.
type Direction string

const (
	// Back wins if the outcome happens ("for").
	Back Direction = "for"
	// Lay wins if the outcome does not happen ("against").
	Lay Direction = "against"
)

// MinPrice and MaxPrice bound the tick schedule.
const (
	MinPrice = 1.01
	MaxPrice = 1000.0
)

// tickBand is one row of the price tick schedule: prices in [lo, hi) step by
// tick. Boundaries are exact in decimal price.
type tickBand struct {
	lo, hi, tick float64
}

// tickSchedule is the fixed schedule all prices lie on. The tick widens as the
// decimal price grows.
var tickSchedule = []tickBand{
	{1.01, 2, 0.01},
	{2, 3, 0.02},
	{3, 4, 0.05},
	{4, 6, 0.10},
	{6, 10, 0.20},
	{10, 20, 0.50},
	{20, 30, 1},
	{30, 50, 2},
	{50, 100, 5},
	{100, 1000, 10},
}

// TickAt returns the tick size that applies at the given decimal price.
func TickAt(price float64) float64 {
	return bandFor(price).tick
}

// bandFor returns the schedule band containing price, clamping out-of-range
// values to the first or last band.
func bandFor(price float64) tickBand {
	if price <= MinPrice {
		return tickSchedule[0]
	}
	for _, b := range tickSchedule {
		if price < b.hi {
			return b
		}
	}
	return tickSchedule[len(tickSchedule)-1]
}

// SnapPrice rounds price onto the tick schedule in the direction that does not
// tighten the bettor's limit: down for back ("for") orders, up for lay
// ("against") orders.
//
// This mirrors what the server does to an off-tick order price, so the CLI can
// show the price the order will actually run with before it is submitted. A
// price already on the schedule is returned unchanged.
func SnapPrice(price float64, dir Direction) float64 {
	if price <= MinPrice {
		return MinPrice
	}
	if price >= MaxPrice {
		return MaxPrice
	}

	b := bandFor(price)
	steps := (price - b.lo) / b.tick

	// Treat a price within float noise of a tick as already on-schedule,
	// so 2.50 is never nudged to 2.48 by binary representation error.
	if nearest := math.Round(steps); math.Abs(steps-nearest) < 1e-9 {
		steps = nearest
	} else if dir == Lay {
		steps = math.Ceil(steps)
	} else {
		steps = math.Floor(steps)
	}

	snapped := roundTo(b.lo+steps*b.tick, decimalsFor(b.tick))

	// Rounding up out of a band lands on the next band's lower bound, which
	// is itself a valid price. Clamp so we never exceed the schedule.
	if snapped > MaxPrice {
		return MaxPrice
	}
	if snapped < MinPrice {
		return MinPrice
	}
	return snapped
}

// IsOnTick reports whether price sits exactly on the tick schedule.
func IsOnTick(price float64) bool {
	if price < MinPrice || price > MaxPrice {
		return false
	}
	b := bandFor(price)
	steps := (price - b.lo) / b.tick
	return math.Abs(steps-math.Round(steps)) < 1e-9
}

// decimalsFor returns how many decimal places a tick size needs, so snapped
// prices print as 2.48 rather than 2.4800000000000004.
func decimalsFor(tick float64) int {
	switch {
	case tick >= 1:
		return 0
	case tick >= 0.1:
		return 1
	default:
		return 2
	}
}

func roundTo(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

// ImpliedCents converts a decimal price to its implied probability in cents.
// The band boundaries are exact in decimal price, so match prices to bands by
// decimal value, not by cents.
func ImpliedCents(price float64) float64 {
	if price <= 0 {
		return 0
	}
	return 100 / price
}

// DirectionOf reads the direction from a bet_type string. Bet types always
// begin with "for" or "against".
func DirectionOf(betType string) Direction {
	if strings.HasPrefix(betType, string(Lay)+",") || betType == string(Lay) {
		return Lay
	}
	return Back
}

// AsianLineToWire converts a real Asian handicap line to its wire integer.
// Wire lines are integers equal to 4× the actual line, which keeps 0.25-step
// lines integer-only on the wire (0.5 → 2, 1.75 → 7, -1.0 → -4).
func AsianLineToWire(line float64) (int, error) {
	wire := line * 4
	if math.Abs(wire-math.Round(wire)) > 1e-9 {
		return 0, fmt.Errorf("asian handicap line %g is not a multiple of 0.25", line)
	}
	return int(math.Round(wire)), nil
}

// AsianLineFromWire converts a wire Asian handicap integer to the real line.
func AsianLineFromWire(wire int) float64 {
	return float64(wire) / 4
}
