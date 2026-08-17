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
// on it. capture_connection is keyed (user_id, provider).
type AccountLabeler interface {
	AccountLabel(auth Auth) (string, error)
}

// GrantedScoper reports the PROVIDER scopes a connection actually holds — the
// provider's own vocabulary ("Mail.Read"), read back from the Auth bundle the
// consent sealed. Distinct from Descriptor.Scopes, which is this system's
// internal permission vocabulary; the two never share storage.
//
// Optional and type-asserted like AccountLabeler: a connector that cannot know
// its granted scopes (a direct-credential one, with no consent step) simply
// does not implement it, and the connection records no claim rather than a
// false one.
type GrantedScoper interface {
	GrantedScopes(auth Auth) ([]string, error)
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

	// Counterparty is the human on the other side of a captured message —
	// the auto-create pipeline's input (ADR-0063). Zero for records that
	// carry no counterparty (a lead import, a system activity); the
	// resolver never runs for those.
	Counterparty Counterparty

	// ThreadKey is the conversation identity, and what counts as "the
	// conversation" differs by shape. Mail roots on a MESSAGE id — the
	// References root, else In-Reply-To, else the message's own id — never a
	// provider's private conversation id, a different namespace joining
	// nothing here (see SendReceipt); a fresh message with no reply headers
	// roots at its OWN Message-ID rather than empty, so a later reply joins
	// from the first message onward. A channel (Telegram) has no message-id
	// chain to root on — the chat itself IS the conversation, so ThreadKey is
	// the provider's chat id ("telegram:<bot_id>:<chat_id>"), the one case
	// where a provider conversation id is the right join key. Both feed the
	// same CAP-FORMULA-1 reply join and activity.thread_key. Empty only for a
	// mail record with none of its three sources; a channel record always has one.
	ThreadKey string

	// Participants are the FURTHER parties to this message beyond the mailbox
	// owner and Counterparty — the CCs on a thread, the attendees and organizer
	// of a meeting. A connector that reports none behaves exactly as before,
	// which is why this is additive rather than a replacement for Counterparty:
	// the two ends of the exchange are what direction is defined against, and
	// everyone else is present without being either end.
	//
	// They are addresses, not records. Resolving one to a colleague or a known
	// contact is capture's job at stamping time, and an address that resolves to
	// neither is still kept — an attendee nobody has a record for is a fact
	// about the meeting.
	Participants []MessageParticipant

	// Addresses is EVERY address this record names — for mail the union of
	// From, To, Cc and whatever Bcc survived; for calendar the organizer and
	// attendees — including the connected owner's own. It is what the
	// internal-vs-external decision is taken over (ADR-0082/A127, formulas
	// §20), which is why it overlaps Counterparty and Participants rather than
	// complementing them: those two are the derived ENDS of the exchange, and
	// a message is internal only when every party to it is.
	//
	// A connector that reports none is saying "I cannot enumerate the parties",
	// not "there are none" — and an unenumerable message is never treated as
	// internal, so it is captured.
	Addresses []string

	// Parts are the files this record carried, already bounded, renamed safely
	// and typed by their bytes. A connector never enforces those rules itself:
	// they belong to the one parser every mail adapter shares, so a new adapter
	// cannot arrive without them.
	Parts []Part

	// PartDrops names the files the bounds refused. It is carried rather than
	// discarded so a message whose attachments were too many or too large is
	// distinguishable from a message that had none — silence would report the
	// two identically, and only one of them means something is missing.
	PartDrops []PartDrop
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

// BackfillProgress carries a page's tally WHILE the page runs, so the engine
// can show progress that moves per message instead of once per committed
// page. A page is a hundred messages and minutes of provider I/O; without
// this the activation view sits at zero for the whole first page and reads
// as a dead import.
//
// Optional on both sides. The engine installs a reporter with
// WithBackfillProgress; a connector that never calls it reports only the
// BackfillPageResult it already returned, and behaves exactly as before.
// What a reporter records is advisory and transient — the page's own commit
// remains the one authority on a run's counters.
type BackfillProgress interface {
	// Observed reports THIS page's tally so far — the same three counts the
	// page's result carries, so a caller reading them mid-page still finds
	// scanned - captured = skipped. The numbers are absolute since the page
	// began, never deltas: a reporter that misses a call is corrected by the
	// next one instead of drifting, and a retried page restates rather than
	// double-counts.
	Observed(ctx context.Context, scanned, captured, skipped int)
}

// backfillProgressKey is the private context key — unexported and typed, so
// the reporter is reachable only through the two helpers below, never by
// another package reaching into the context for it directly.
type backfillProgressKey struct{}

// WithBackfillProgress installs the reporter a running page reports into.
// The engine calls this for the page it is about to run; nothing else should.
func WithBackfillProgress(ctx context.Context, p BackfillProgress) context.Context {
	return context.WithValue(ctx, backfillProgressKey{}, p)
}

// BackfillReporter is the value a connector reports through. It wraps the
// installed reporter, if any, so an unreported page costs a branch instead of
// a nil check at every call site.
type BackfillReporter struct{ to BackfillProgress }

// Observed forwards the page's tally, or discards it when nothing is
// listening.
func (r BackfillReporter) Observed(ctx context.Context, scanned, captured, skipped int) {
	if r.to != nil {
		r.to.Observed(ctx, scanned, captured, skipped)
	}
}

// BackfillProgressFrom returns the reporter for the running page — usable
// whether or not one was installed. Absence is ordinary: incremental sync
// installs no reporter, and neither do a connector's own tests.
func BackfillProgressFrom(ctx context.Context) BackfillReporter {
	p, _ := ctx.Value(backfillProgressKey{}).(BackfillProgress)
	return BackfillReporter{to: p}
}
