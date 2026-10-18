// Package apitypes contains Go types generated from api/openapi.yaml.
// Handlers build responses from these types so the server cannot drift from
// the published contract. Regenerate with `go generate ./...`.
package apitypes

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../../api/openapi.yaml

// The table of operations that accept automation tokens, from the spec's
// x-omp-token-scopes extension (decision 58).
//go:generate go run ../../tools/tokenscopes ../../../api/openapi.yaml tokenscopes.gen.go
