package magicmarkets

import (
	"math"
	"testing"
)

func TestTickAt(t *testing.T) {
	cases := []struct {
		price float64
		want  float64
	}{
		{1.01, 0.01},
		{1.50, 0.01},
		{1.99, 0.01},
		{2.00, 0.02}, // band boundaries belong to the upper band
		{2.98, 0.02},
		{3.00, 0.05},
		{4.00, 0.10},
		{6.00, 0.20},
		{10.0, 0.50},
		{20.0, 1},
		{30.0, 2},
		{50.0, 5},
		{100.0, 10},
		{999.0, 10},
	}
	for _, c := range cases {
		if got := TickAt(c.price); got != c.want {
			t.Errorf("TickAt(%g) = %g, want %g", c.price, got, c.want)
		}
	}
}

func TestSnapPriceAlreadyOnTick(t *testing.T) {
	// Prices on the schedule must survive untouched, including ones that are
	// inexact in binary floating point.
	onTick := []float64{1.01, 1.50, 1.99, 2.00, 2.02, 2.50, 3.05, 4.10, 6.20, 10.5, 21, 32, 55, 110, 1000}
	for _, p := range onTick {
		if !IsOnTick(p) {
			t.Errorf("IsOnTick(%g) = false, want true", p)
		}
		for _, dir := range []Direction{Back, Lay} {
			if got := SnapPrice(p, dir); math.Abs(got-p) > 1e-9 {
				t.Errorf("SnapPrice(%g, %s) = %g, want %g unchanged", p, dir, got, p)
			}
		}
	}
}

func TestSnapPriceDirection(t *testing.T) {
	// Measured on production 2026-10-09: on a normal betslip an off-tick
	// limit moves up (7.15 to 7.20, and 1.985 to 1.99 on an against bet type);
	// on a lay betslip it moves down (2.31 to 2.30, 2.01 to 2.00).
	cases := []struct {
		price    float64
		wantBack float64
		wantLay  float64
	}{
		{1.234, 1.24, 1.23},   // 0.01 tick
		{1.985, 1.99, 1.98},   // measured: normal against,h ran at 1.99
		{2.01, 2.02, 2.00},    // measured: lay against,h ran at 2.00
		{2.31, 2.32, 2.30},    // measured: lay for,h ran at 2.30
		{2.345, 2.36, 2.34},   // 0.02 tick
		{3.33, 3.35, 3.30},    // 0.05 tick
		{4.55, 4.60, 4.50},    // 0.10 tick
		{6.55, 6.60, 6.40},    // 0.20 tick
		{7.15, 7.20, 7.00},    // the documented back example
		{10.7, 11.0, 10.5},    // 0.50 tick
		{20.5, 21.0, 20.0},    // 1 tick
		{31.5, 32.0, 30.0},    // 2 tick
		{52.5, 55.0, 50.0},    // 5 tick
		{105.0, 110.0, 100.0}, // 10 tick
	}
	for _, c := range cases {
		if got := SnapPrice(c.price, Back); math.Abs(got-c.wantBack) > 1e-9 {
			t.Errorf("SnapPrice(%g, back) = %g, want %g", c.price, got, c.wantBack)
		}
		if got := SnapPrice(c.price, Lay); math.Abs(got-c.wantLay) > 1e-9 {
			t.Errorf("SnapPrice(%g, lay) = %g, want %g", c.price, got, c.wantLay)
		}
	}
}

func TestSnapPriceCrossesBandUpward(t *testing.T) {
	// Rounding up out of a band lands on the next band's lower bound, which
	// is itself a valid price.
	if got := SnapPrice(1.995, Back); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("SnapPrice(1.995, back) = %g, want 2", got)
	}
	if got := SnapPrice(2.99, Back); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("SnapPrice(2.99, back) = %g, want 3", got)
	}
	if got := SnapPrice(1.995, Lay); math.Abs(got-1.99) > 1e-9 {
		t.Errorf("SnapPrice(1.995, lay) = %g, want 1.99", got)
	}
}

func TestSnapDirection(t *testing.T) {
	if SnapDirection("lay") != Lay {
		t.Error(`SnapDirection("lay") should be Lay`)
	}
	for _, bt := range []string{"normal", "parlay", ""} {
		if SnapDirection(bt) != Back {
			t.Errorf("SnapDirection(%q) should be Back", bt)
		}
	}
}

func TestSnapPriceClamps(t *testing.T) {
	if got := SnapPrice(1.0, Back); got != MinPrice {
		t.Errorf("SnapPrice(1.0, back) = %g, want %g", got, MinPrice)
	}
	if got := SnapPrice(5000, Lay); got != MaxPrice {
		t.Errorf("SnapPrice(5000, lay) = %g, want %g", got, MaxPrice)
	}
}

func TestSnapPriceNeverWorseThanRequest(t *testing.T) {
	// Property check: a snapped limit is never worse than the request. Higher
	// is better on a normal betslip, lower is better on a lay betslip.
	for p := 1.01; p <= 999; p += 0.013 {
		back := SnapPrice(p, Back)
		if back < p-1e-9 || !IsOnTick(back) {
			t.Fatalf("SnapPrice(%g, back) = %g: below the request or off the schedule", p, back)
		}
		lay := SnapPrice(p, Lay)
		if lay > p+1e-9 || !IsOnTick(lay) {
			t.Fatalf("SnapPrice(%g, lay) = %g: above the request or off the schedule", p, lay)
		}
	}
}

func TestSnapPriceOutputIsOnTick(t *testing.T) {
	for p := 1.01; p <= 999; p += 0.017 {
		for _, dir := range []Direction{Back, Lay} {
			got := SnapPrice(p, dir)
			if !IsOnTick(got) {
				t.Fatalf("SnapPrice(%g, %s) = %g, which is not on the tick schedule", p, dir, got)
			}
		}
	}
}

func TestDirectionOf(t *testing.T) {
	cases := map[string]Direction{
		"for,h":            Back,
		"for,ah,h,-4":      Back,
		"against,h":        Lay,
		"against,over,2.5": Lay,
	}
	for betType, want := range cases {
		if got := DirectionOf(betType); got != want {
			t.Errorf("DirectionOf(%q) = %s, want %s", betType, got, want)
		}
	}
}

func TestAsianLineWireRoundTrip(t *testing.T) {
	// Asian handicap lines are integers equal to 4x the actual line.
	cases := []struct {
		line float64
		wire int
	}{
		{0.0, 0},
		{0.5, 2},
		{1.75, 7},
		{2.0, 8},
		{-1.0, -4},
		{-5.25, -21},
	}
	for _, c := range cases {
		got, err := AsianLineToWire(c.line)
		if err != nil {
			t.Fatalf("AsianLineToWire(%g): %v", c.line, err)
		}
		if got != c.wire {
			t.Errorf("AsianLineToWire(%g) = %d, want %d", c.line, got, c.wire)
		}
		if back := AsianLineFromWire(c.wire); math.Abs(back-c.line) > 1e-9 {
			t.Errorf("AsianLineFromWire(%d) = %g, want %g", c.wire, back, c.line)
		}
	}

	if _, err := AsianLineToWire(0.3); err == nil {
		t.Error("AsianLineToWire(0.3) should fail: not a multiple of 0.25")
	}
}

func TestImpliedCents(t *testing.T) {
	if got := ImpliedCents(2.0); math.Abs(got-50) > 1e-9 {
		t.Errorf("ImpliedCents(2.0) = %g, want 50", got)
	}
	if got := ImpliedCents(4.0); math.Abs(got-25) > 1e-9 {
		t.Errorf("ImpliedCents(4.0) = %g, want 25", got)
	}
}
