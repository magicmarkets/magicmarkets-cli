// Command prepspec rewrites the vendored OpenAPI spec into a form oapi-codegen
// can consume, without modifying the vendored file itself.
//
// Two transformations are needed:
//
//  1. Every `{"type": "number"}` gains `"format": "double"`. oapi-codegen maps a
//     formatless `number` to float32, which carries only ~7 significant decimal
//     digits — not enough for prices and stakes. `double` maps to float64.
//
//  2. `StakeTuple` is replaced with an untyped array. It is an OpenAPI 3.1
//     tuple (`prefixItems` with `"type": ["string", "number"]`) and oapi-codegen
//     cannot express that; left alone it fails outright. The generated package
//     is a contract reference, so an untyped array is sufficient there — the
//     typed form lives in magicmarkets.Stake, which marshals the tuple properly.
//
// Run via `make generate`; the output is a temporary file that is not checked in.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	in := flag.String("in", "internal/spec/openapi.json", "path to the vendored OpenAPI spec")
	out := flag.String("out", "", "path to write the prepared spec (required)")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "prepspec: -out is required")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepspec: read %s: %v\n", *in, err)
		os.Exit(1)
	}

	// A generic tree keeps every field we do not care about untouched.
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "prepspec: parse %s: %v\n", *in, err)
		os.Exit(1)
	}

	stats := &counters{}
	doc = widenNumbers(doc, stats)

	if err := replaceStakeTuple(doc, stats); err != nil {
		fmt.Fprintf(os.Stderr, "prepspec: %v\n", err)
		os.Exit(1)
	}

	prepared, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepspec: encode: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, prepared, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "prepspec: write %s: %v\n", *out, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "prepspec: %s -> %s (%d number fields widened to double, StakeTuple replaced: %v)\n",
		*in, *out, stats.numbersWidened, stats.stakeTupleReplaced)
}

type counters struct {
	numbersWidened     int
	stakeTupleReplaced bool
}

// isNumericSchema reports whether a schema's "type" denotes a number.
//
// Nullable fields in this spec use the OpenAPI 3.1 union form
// `"type": ["number", "null"]`, which matters because the fields written that
// way are the achieved-price ones — exactly where float32 would hurt most.
func isNumericSchema(typ any) bool {
	switch t := typ.(type) {
	case string:
		return t == "number"
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s == "number" {
				return true
			}
		}
	}
	return false
}

// widenNumbers walks the tree and gives every formatless numeric schema an
// explicit `"format": "double"`.
//
// It keys off the presence of a "type" sibling so it only touches schema
// objects, not arbitrary maps that happen to contain the string "number".
func widenNumbers(node any, stats *counters) any {
	switch n := node.(type) {
	case map[string]any:
		if isNumericSchema(n["type"]) {
			if _, hasFormat := n["format"]; !hasFormat {
				n["format"] = "double"
				stats.numbersWidened++
			}
		}
		for k, v := range n {
			n[k] = widenNumbers(v, stats)
		}
		return n
	case []any:
		for i, v := range n {
			n[i] = widenNumbers(v, stats)
		}
		return n
	default:
		return node
	}
}

// replaceStakeTuple swaps the StakeTuple schema for an untyped array.
//
// It fails loudly rather than silently skipping: if the schema is renamed
// upstream, codegen would break in a far more confusing way.
func replaceStakeTuple(doc any, stats *counters) error {
	root, ok := doc.(map[string]any)
	if !ok {
		return fmt.Errorf("spec root is not an object")
	}
	components, ok := root["components"].(map[string]any)
	if !ok {
		return fmt.Errorf("spec has no components object")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return fmt.Errorf("spec has no components.schemas object")
	}
	if _, ok := schemas["StakeTuple"]; !ok {
		return fmt.Errorf("StakeTuple schema not found — it may have been renamed upstream; " +
			"update tools/prepspec to match")
	}

	schemas["StakeTuple"] = map[string]any{
		"type":  "array",
		"items": map[string]any{},
		"description": "[currency, amount] tuple, e.g. [\"USDT\", 115.38]. Generated as an " +
			"untyped array because oapi-codegen cannot express an OpenAPI 3.1 tuple; " +
			"magicmarkets.Stake is the typed equivalent.",
	}
	stats.stakeTupleReplaced = true
	return nil
}
