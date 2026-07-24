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
	"time"

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

// Watcher is the OPTIONAL push-watch seam a connector implements when its
// provider delivers change notifications through a subscription that must be
// renewed before it lapses (Gmail Pub/Sub's 7-day watch, Graph's ≤3-day
// subscription). It is separate from Connector because a provider without a
// renewable push subscription (the one-shot IMAP puller) does not implement it;
// the registry's watch-renewal scan type-asserts for it and skips a connector
// that is not a Watcher.
type Watcher interface {
	// Watch registers (or, on a repeat call, renews) the provider push
	// subscription against topic and returns the watermark to resume from plus
	// the new expiration deadline. It performs provider I/O like Sync; it never
	// touches the CRM or the connection row (the registry persists the result).
	Watch(ctx context.Context, auth Auth, topic string) (WatchResult, error)
}

// WatchResult is the outcome of registering/renewing a provider push watch:
// the historyId/delta anchor at watch time and when the watch expires. The
// registry stores ExpiresAt in capture_connection.watch_expires_at, which the
// renewal scan keys on (CAP-DDL-2, idx_capture_watch_renew).
type WatchResult struct {
	HistoryID string
	ExpiresAt time.Time
}

// AccountLabeler names the account an Auth bundle belongs to — the mailbox
// address, for display only. Optional and type-asserted, exactly like Watcher
// and Backfiller: the Connector interface stays frozen, and a connector that
// cannot name its account simply does not implement this.
//
// The label is never an identifier: nothing routes, authorizes or deduplicates
// on it. capture_connection is keyed (workspace_id, user_id, provider).
type AccountLabeler interface {
	AccountLabel(auth Auth) (string, error)
}

// Descriptor — declared capabilities; ⊆ the granting human's scopes.
type Descriptor struct {
	Name     string // stable id: "gmail", "gcal", "hubspot", "coldstart-scrape"
	Version  string
	Scopes   []principal.Scope
	RiskTier mcp.RiskTier // capture/read = auto_execute; any outbound = confirmation_required
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
	// Match carries the attributes the personal-mail exclusion gate (RC-2)
	// evaluates in the ONE Sink, BEFORE anything is written. Mail
	// connectors populate it; a record with a zero value (a lead, a
	// non-mail activity) can never match a rule, so the gate is a no-op
	// for it. Kept off Fields on purpose: exclusion is a pipeline concern,
	// not a domain column.
	Match ExclusionAttrs

	// Counterparty is the human on the other side of a captured message —
	// the auto-create pipeline's input (ADR-0063). Zero for records that
	// carry no counterparty (a lead import, a system activity); the
	// resolver never runs for those.
	Counterparty Counterparty

	// ThreadKey is the provider's conversation identity (Gmail threadId /
	// Graph conversationId / RFC822 References root) — the CAP-FORMULA-1
	// reply-detection join key and activity.thread_key's source. Empty when
	// the provider knows no thread.
	ThreadKey string
}

// Counterparty names the non-owner participant of one captured message.
// Email is authoritative; DisplayName is the header's human name (may be
// empty or hostile — consumers must treat it as untrusted text); Domain is
// the lowercased mail domain; Direction is the message's direction relative
// to the mailbox owner (inbound | outbound).
type Counterparty struct {
	Email       string
	DisplayName string
	Domain      string
	Direction   string
}

// ExclusionAttrs is the normalized, matchable face of a captured message
// the RC-2 exclusion gate reads: the sender's domain, every recipient's
// domain, and any provider mail labels. Producers should already lowercase
// these; the matcher compares case-insensitively regardless.
type ExclusionAttrs struct {
	SenderDomain     string
	RecipientDomains []string
	Labels           []string
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

// Backfiller is the OPTIONAL bounded-backfill seam (ADR-0063): a connector
// implements it when its provider can enumerate a mailbox backward from a
// date boundary. Like Watcher, it is separate from Connector so a provider
// without a date-bounded listing simply is not a Backfiller; the backfill
// engine type-asserts and refuses honestly. Backfill paging is disjoint from
// Sync's cursor by construction — incremental moves forward from the
// connect-time watermark while backfill pages backward on its own token, and
// the capture key makes any overlap a no-op.
type Backfiller interface {
	// EstimateBackfill returns the provider-side message count newer than
	// after — the scope shown before anything spends (the preview op's
	// number). An estimate, labeled as such; providers round.
	EstimateBackfill(ctx context.Context, auth Auth, after time.Time) (int, error)

	// BackfillPage pulls ONE bounded page of messages newer than after,
	// emitting each through the Sink. It performs provider I/O like Sync;
	// the engine persists cursor and counters from the returned result.
	BackfillPage(ctx context.Context, auth Auth, after time.Time, pageToken string, sink Sink) (BackfillPageResult, error)
}

// BackfillPageResult is one page's outcome: the token for the next page
// ("" = the window is exhausted) and the page's tally.
type BackfillPageResult struct {
	NextToken string
	Scanned   int
	Captured  int
	Skipped   int
}
