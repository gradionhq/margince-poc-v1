// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package crm is the CONTRACT's own shapes, published to the extension tier:
// the records a unit reads and the requests it writes through
// extension.Tx.Core(). Every type here is generated from a subset of
// backend/api/crm.yaml, so a unit writes the shape the product's own HTTP
// surface documents, and the two cannot drift into two vocabularies for one
// record.
//
// It is a SUBSET, and deliberately a small one. A published type is one an
// installation's units compile against, so publishing the whole contract would
// freeze thousands of schemas nobody asked for; the set here grows one entity
// at a time, when the port grows a verb that takes or returns it.
//
// Two shapes the contract carries are NOT here, and their absence is the
// design rather than a gap:
//
//   - Custom fields. A record's `additional_properties` needs the field
//     catalog, and reading the catalog opens a second transaction — the very
//     acquire the port's tx-borrowing seams exist to avoid. The port refuses a
//     write that carries them rather than dropping them silently.
//   - `format: uuid` and `format: email` as their own Go types. They generate
//     openapi_types.UUID, which this package may not import (`pkg-purity` is a
//     strict depguard over backend/pkg), and a unit handles both as strings
//     anyway — extension.Caller.UserID is a string for the same reason.
//
//margince:extension-surface
package crm

// The generated file is drift-gated (`make drift`), so all three steps run from
// `make gen` and none of their output is edited by hand. The middle step is the
// same 3.1-to-3.0 downgrade the main contract pipeline runs, and for the same
// reason: the generator cannot read a 3.1 nullable. Pruning is OFF on purpose:
// the subset has no operations, so every schema in it has zero incoming refs
// and a pruning pass would empty the package while the manifest still looked
// right.

//go:generate go run github.com/gradionhq/margince/backend/tools/contract-subset -in ../../../api/crm.yaml -out .build/subset31.yaml -schemas Activity,CreateActivityRequest
//go:generate go run github.com/gradionhq/margince/backend/tools/contract-overlay -in .build/subset31.yaml -out .build/subset30.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.1 --config oapi.yaml .build/subset30.yaml
