// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package connector defines the capture/integration seam (interfaces.md
// §1): the uniform interface every integration implements — Gmail,
// calendar, telephony, the scrape/enrichment connector, and the deepest
// one, an incumbent SoR adapter. A connector normalizes provider records
// and hands them to the Sink; the capture module (never the connector) writes the
// row, the audit entry, and the domain event, so RBAC/RLS/audit stay in
// one place.
package connector

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Connector is the seam every integration implements, registered in the
// connector registry by Descriptor().Name.
type Connector interface {
	// Descriptor is static metadata, read at registration; it drives scope
	// enforcement, the 🟢/🟡 tier, crm gen, and the contract.
	Descriptor() Descriptor

	// Authenticate establishes or refreshes credentials for one
	// per-user, per-workspace connection and returns the opaque persisted
	// Auth the other methods reuse.
	Authenticate(ctx context.Context, req AuthRequest) (Auth, error)

	// Sync pulls INCREMENTALLY from cursor (history API / delta token /
	// updatedAt watermark), emits normalized records via the Sink, and
	// returns the advanced cursor. Idempotent: writes key on
	// (source_system, source_id) so the DB unique index dedupes replays.
	Sync(ctx context.Context, auth Auth, cursor Cursor, sink Sink) (Cursor, error)

	// Normalize maps ONE raw provider record to provenance-stamped domain
	// records. Pure — no I/O — so the mapping is the agent-edited,
	// test-guarded surface. Returns an ErrSkip-wrapped error for
	// deliberately excluded input (personal-mail rule etc.).
	Normalize(ctx context.Context, raw RawRecord) ([]NormalizedRecord, error)

	// HealthCheck feeds the ops surface; an outage degrades capture but
	// never blocks core CRM (capture is async on the job queue).
	HealthCheck(ctx context.Context, auth Auth) error
}

// Descriptor — declared capabilities; ⊆ the granting human's scopes.
type Descriptor struct {
	Name     string // stable id: "gmail", "gcal", "hubspot", "coldstart-scrape"
	Version  string
	Scopes   []principal.Scope
	RiskTier mcp.RiskTier // capture/read = green; any outbound = yellow
	Tools    []mcp.ToolSpec
	Produces []datasource.EntityType
}

// AuthRequest carries whatever the provider handshake needs (OAuth code,
// API key); shape is provider-specific and opaque to the registry.
type AuthRequest struct {
	WorkspaceConnection string
	Payload             []byte
}

// Sink is how a connector hands normalized records to the CRM for
// upsert + provenance + event emit.
type Sink interface {
	// Upsert writes one record idempotently by its NaturalKey, stamps
	// provenance, writes the audit row, and emits the domain event.
	Upsert(ctx context.Context, rec NormalizedRecord) (datasource.EntityRef, error)
}

// NormalizedRecord — a provider record mapped onto the clean relational
// core with provenance. Fields holds the typed domain struct for
// EntityType so a wrong mapping fails to compile, not at runtime.
type NormalizedRecord struct {
	EntityType datasource.EntityType
	NaturalKey NaturalKey
	Fields     any
	Links      []datasource.EntityRef
	Source     string // "<system>:<id>" — REQUIRED
	CapturedBy string // "connector:<name>" — REQUIRED
	Raw        []byte // re-parseable original → raw jsonb, off the hot path
}

// NaturalKey is the (source_system, source_id) idempotency key the DB
// unique indexes enforce (data-model §7/§8).
type NaturalKey struct {
	SourceSystem string
	SourceID     string
}

type (
	Cursor    []byte // opaque incremental-sync watermark
	Auth      []byte // opaque persisted credential bundle
	RawRecord []byte // one provider record as received
)

// ErrSkip marks a record a connector intentionally skipped (excluded or
// out of scope); the sync loop counts it, never surfaces it as a failure.
var ErrSkip = errors.New("connector: record intentionally skipped")
