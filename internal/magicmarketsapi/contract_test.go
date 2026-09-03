package magicmarketsapi_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"magicmarkets-cli/internal/magicmarkets"
	"magicmarkets-cli/internal/magicmarketsapi"
)

// This test is what makes the generated package load-bearing.
//
// internal/magicmarkets keeps hand-written types because generated code cannot express
// stake tuples, the bet-status union, or non-pointer access to fields the API
// always sends (see doc.go). The risk in hand-written wire types is silent
// drift: the API grows a field, nobody notices, and the client quietly ignores
// it.
//
// So every hand-written type is compared field-for-field, by JSON name, against
// its generated counterpart. After `make update-spec && make generate`, an
// upstream field addition, removal or rename fails `go test` instead of being
// found in production.

// pair binds a hand-written type to the generated model it must mirror.
type pair struct {
	name string
	// hand is the hand-written type in internal/magicmarkets.
	hand any
	// generated is the model produced from the spec.
	generated any
	// handOnly lists JSON names the hand-written type may carry that the
	// generated model does not, each with a reason.
	handOnly map[string]string
	// specOnly lists JSON names present in the spec that the client
	// deliberately does not model, each with a reason.
	specOnly map[string]string
}

func pairs() []pair {
	return []pair{
		{name: "Order", hand: magicmarkets.Order{}, generated: magicmarketsapi.OrderResponse{}},
		{name: "Bet", hand: magicmarkets.Bet{}, generated: magicmarketsapi.BetResponse{}},
		{name: "Betslip", hand: magicmarkets.Betslip{}, generated: magicmarketsapi.BetslipResponse{}},
		{name: "ParlayLeg", hand: magicmarkets.ParlayLeg{}, generated: magicmarketsapi.ParlayLeg{}},
		{name: "PriceLevel", hand: magicmarkets.PriceLevel{}, generated: magicmarketsapi.PriceLevel{}},
		{name: "EventInfo", hand: magicmarkets.EventInfo{}, generated: magicmarketsapi.EventInfo{}},
		{name: "Balance", hand: magicmarkets.Balance{}, generated: magicmarketsapi.BalanceResponse{}},
		{name: "Heartbeat", hand: magicmarkets.Heartbeat{}, generated: magicmarketsapi.HeartbeatResponse{}},
		{name: "XRate", hand: magicmarkets.XRate{}, generated: magicmarketsapi.XRate{}},
		{name: "BetTypeInfo", hand: magicmarkets.BetTypeInfo{}, generated: magicmarketsapi.BetTypeInfoResponse{}},
		{name: "Position", hand: magicmarkets.Position{}, generated: magicmarketsapi.PositionResponse{}},
		{name: "PositionGrid", hand: magicmarkets.PositionGrid{}, generated: magicmarketsapi.PositionGrid{}},
		{name: "CashoutInfo", hand: magicmarkets.CashoutInfo{}, generated: magicmarketsapi.PositionCashoutInfo{}},
		{
			name:      "CreateBetslipRequest",
			hand:      magicmarkets.CreateBetslipRequest{},
			generated: magicmarketsapi.BetslipCreateRequest{},
		},
		{
			name:      "CreateOrderRequest",
			hand:      magicmarkets.CreateOrderRequest{},
			generated: magicmarketsapi.OrderCreateRequest{},
		},
	}
}

func TestHandWrittenTypesMatchTheSpec(t *testing.T) {
	for _, p := range pairs() {
		t.Run(p.name, func(t *testing.T) {
			handFields := jsonFields(reflect.TypeOf(p.hand))
			specFields := jsonFields(reflect.TypeOf(p.generated))

			for _, name := range sortedKeys(specFields) {
				if handFields[name] {
					continue
				}
				if reason, ok := p.specOnly[name]; ok {
					t.Logf("not modelled: %q (%s)", name, reason)
					continue
				}
				t.Errorf("magicmarkets.%s is missing field %q, which the spec defines.\n"+
					"The API grew a field the client ignores — add it, or record it in specOnly with a reason.",
					p.name, name)
			}

			for _, name := range sortedKeys(handFields) {
				if specFields[name] {
					continue
				}
				if reason, ok := p.handOnly[name]; ok {
					t.Logf("client-only: %q (%s)", name, reason)
					continue
				}
				t.Errorf("magicmarkets.%s declares field %q, which the spec does not define.\n"+
					"It may have been removed or renamed upstream — drop it, or record it in handOnly with a reason.",
					p.name, name)
			}
		})
	}
}

// TestPositionTotalCoversBothVariants checks the one hand-written type that
// deliberately merges two spec schemas.
//
// The spec models a position total as oneOf{PositionComponentTotal,
// PositionCustomBetTotal}. magicmarkets.PositionTotal flattens both so callers do not
// have to branch, so it must carry the union of their fields.
func TestPositionTotalCoversBothVariants(t *testing.T) {
	handFields := jsonFields(reflect.TypeOf(magicmarkets.PositionTotal{}))

	union := map[string]bool{}
	for _, variant := range []any{
		magicmarketsapi.PositionComponentTotal{},
		magicmarketsapi.PositionCustomBetTotal{},
	} {
		for name := range jsonFields(reflect.TypeOf(variant)) {
			union[name] = true
		}
	}

	for _, name := range sortedKeys(union) {
		if !handFields[name] {
			t.Errorf("magicmarkets.PositionTotal is missing field %q, present in one of the spec's "+
				"position-total variants", name)
		}
	}
	for _, name := range sortedKeys(handFields) {
		if !union[name] {
			t.Errorf("magicmarkets.PositionTotal declares field %q, absent from both spec variants", name)
		}
	}
}

// TestEventResultCoversAllSportVariants checks the one hand-written type that
// deliberately merges five spec schemas.
//
// The spec models a match/race result as oneOf{EventResultMatch,
// EventResultTennis, EventResultHockey, EventResultTableTennis,
// EventResultMultirunner} — the API picks one shape per sport. magicmarkets.EventResult
// flattens all five so callers do not have to branch on sport before reading a
// result, so it must carry the union of their fields.
func TestEventResultCoversAllSportVariants(t *testing.T) {
	handFields := jsonFields(reflect.TypeOf(magicmarkets.EventResult{}))

	union := map[string]bool{}
	for _, variant := range []any{
		magicmarketsapi.EventResultMatch{},
		magicmarketsapi.EventResultTennis{},
		magicmarketsapi.EventResultHockey{},
		magicmarketsapi.EventResultTableTennis{},
		magicmarketsapi.EventResultMultirunner{},
	} {
		for name := range jsonFields(reflect.TypeOf(variant)) {
			union[name] = true
		}
	}

	for _, name := range sortedKeys(union) {
		if !handFields[name] {
			t.Errorf("magicmarkets.EventResult is missing field %q, present in one of the spec's "+
				"result variants", name)
		}
	}
	for _, name := range sortedKeys(handFields) {
		if !union[name] {
			t.Errorf("magicmarkets.EventResult declares field %q, absent from every spec variant", name)
		}
	}
}

// TestGeneratedPackageIsPresent guards against the generated file being deleted
// or emptied, which would make every comparison above trivially pass.
func TestGeneratedPackageIsPresent(t *testing.T) {
	if n := len(jsonFields(reflect.TypeOf(magicmarketsapi.OrderResponse{}))); n < 20 {
		t.Fatalf("magicmarketsapi.OrderResponse has only %d fields; the generated code looks stale or "+
			"truncated — run `make generate`", n)
	}
}

// TestMoneyAndPricesAreFloat64 pins the reason tools/prepspec exists.
//
// oapi-codegen maps a formatless OpenAPI `number` to float32, which holds only
// ~7 significant decimal digits. prepspec rewrites those to `format: double` so
// prices and stakes generate as float64. If that step regresses, tick snapping
// and stake arithmetic start losing precision, so fail loudly here.
func TestMoneyAndPricesAreFloat64(t *testing.T) {
	numericFields := 0

	for _, p := range pairs() {
		typ := reflect.TypeOf(p.generated)
		for i := range typ.NumField() {
			field := typ.Field(i)
			ft := field.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Float32 {
				t.Errorf("%s.%s generated as float32; prices and stakes need float64 — "+
					"check tools/prepspec", typ.Name(), field.Name)
			}
			if ft.Kind() == reflect.Float64 {
				numericFields++
			}
		}
	}

	// A guard against the loop above passing because it found nothing at all.
	if numericFields == 0 {
		t.Fatal("no float64 fields found across the generated models; codegen looks broken")
	}
}

// jsonFields returns the set of JSON field names a struct marshals, following
// embedded structs so a flattened type compares correctly against a generated
// one.
func jsonFields(typ reflect.Type) map[string]bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	out := map[string]bool{}
	if typ.Kind() != reflect.Struct {
		return out
	}

	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}

		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")

		// An embedded struct with no JSON name is inlined by encoding/json.
		if field.Anonymous && name == "" {
			for n := range jsonFields(field.Type) {
				out[n] = true
			}
			continue
		}
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		out[name] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
