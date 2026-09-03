// Package magicmarketsapi holds Go models generated from the vendored Magic Markets
// OpenAPI specification.
//
// # Why this package exists alongside internal/magicmarkets
//
// These types are a machine-checked record of the API contract, not the types
// the CLI actually uses. The hand-written client in internal/magicmarkets keeps its own
// curated types because generated code cannot express three things this API
// needs:
//
//   - Stakes are OpenAPI 3.1 tuples (["USDT", 115.38]). oapi-codegen cannot
//     generate them at all, so tools/prepspec flattens StakeTuple to an untyped
//     array here. magicmarkets.Stake is the real, typed equivalent.
//   - A bet's status is either a bare string or an object. magicmarkets.BetStatus
//     unmarshals both; a generated union type would push that branch onto every
//     caller.
//   - The spec marks almost nothing `required`, so every generated field is a
//     pointer. Threading nil checks through the CLI for fields the API always
//     sends would be noise.
//
// # What keeps the two in sync
//
// contract_test.go compares the JSON field names of each generated model against
// its hand-written counterpart in internal/magicmarkets and fails on any difference. A
// field added, removed or renamed upstream therefore breaks `go test` after
// `make generate`, instead of being discovered at runtime.
//
// Regenerate with `make generate` after `make update-spec`. Do not edit
// types.gen.go by hand.
package magicmarketsapi

// Paths in oapi-codegen.yaml are relative to the repository root, so generation
// is driven from there. `go generate ./...` delegates to the same make target.
//
//go:generate sh -c "cd ../.. && make generate"
