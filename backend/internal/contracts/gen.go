package crmcontracts

// The contract pipeline (P3, B-EP01.9): the authoritative 3.1 contract is
// api/crm.yaml; the 3.0 overlay is a build artifact; api_gen.go is
// the committed, drift-gated output. `make gen` runs both steps;
// `make drift` fails the merge on any divergence.

// The generator is pinned to an exact version: its output is drift-gated, so an
// unpinned `go run` would float to the latest oapi-codegen and rewrite api_gen.go
// (v2.8.0 renames the enum constants), turning any backend PR red on drift the
// author never touched. Bump this deliberately, regenerating in the same change.

//go:generate go run github.com/gradionhq/margince/backend/tools/contract-overlay -in ../../api/crm.yaml -out .build/openapi30.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.1 --config oapi.yaml .build/openapi30.yaml
