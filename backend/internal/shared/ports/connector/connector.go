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
	"strings"
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

	// ThreadKey is the RFC822 conversation identity: a MESSAGE id — the
	// References root, else In-Reply-To, else the message's own id — never a
	// provider's private conversation id, which lives in a different namespace
	// and joins nothing here (see SendReceipt). It is the CAP-FORMULA-1
	// reply-detection join key and activity.thread_key's source. A freshly
	// captured message with no reply headers is rooted at its OWN Message-ID,
	// not left empty, so a later reply that references it joins the thread
	// from the first message onward. Empty only when the record carries none
	// of the three sources — References, In-Reply-To, nor its own Message-ID.
	ThreadKey string
}

// Counterparty names the non-owner participant of one captured message.
// Email is authoritative; DisplayName is the header's human name (may be
// empty or hostile — consumers must treat it as untrusted text); Domain is
// the lowercased mail domain; Direction is the message's direction relative
// to the mailbox owner (DirectionInbound | DirectionOutbound).
type Counterparty struct {
	Email       string
	DisplayName string
	Domain      string
	Direction   string
	// ListUnsubscribe reports whether the message carried an RFC 2369
	// List-Unsubscribe header — the bulk-mail corroboration the transactional
	// suppression gate (CAP-PARAM-6, ADR-0072) requires before a subdomain
	// prefix rule may suppress record creation. Mail connectors populate it;
	// zero for records that carry no such signal.
	ListUnsubscribe bool
	// sentByOwner is the T1 correspondence-positive gate's only evidence
	// (ADR-0072 §1), and it is deliberately UNEXPORTED. The field is the one
	// thing on this struct a connector must not be able to state for itself:
	// whoever sets it can whitelist an arbitrary address past transactional
	// suppression. Unexported, the compiler refuses every route a convention
	// could not — a positional literal, a JSON unmarshal, reflection, a
	// conversion from a look-alike struct, a pointer handed to a decoder.
	// WithOwnerAttestation is the sole way in, and SentByOwner the sole way out.
	sentByOwner bool
}

// Message direction relative to the mailbox owner, as Counterparty.Direction
// reports it.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// WithOwnerAttestation returns a copy recording that the authenticated mailbox
// owner sent this message. providerFiled is the PROVIDER's own filing — Gmail's
// SENT label, an IMAP \Sent special-use mailbox, Microsoft's SentItems folder —
// and it is honored only where Direction independently names the owner as the
// message's author.
//
// The conjunction lives here, in the port that owns the field, because neither
// half is sufficient and no caller may choose to apply only one. Direction
// compares the forgeable From header against the owner's address, so a spoofed
// From:owner delivered to the inbox would otherwise pass as the owner's own
// correspondence. And placement is not authorship: a server-side rule can file
// a third party's message into the sent container, where the counterparty is
// that stranger's own address.
//
// Build the Counterparty first: this reads Direction as it stands at the call,
// so attesting before Direction is populated — or reassigning it afterwards —
// yields an answer that no longer matches the record. Both mistakes fail toward
// false, and the unexported field carries the same cost at any serialization
// boundary: a Counterparty that ever crosses one arrives un-attested rather
// than wrongly attested.
//
// A caller that attests nothing leaves the answer false, which suppresses
// rather than trusts.
func (c Counterparty) WithOwnerAttestation(providerFiled bool) Counterparty {
	c.sentByOwner = providerFiled && c.Direction == DirectionOutbound
	return c
}

// SentByOwner reports whether both halves of the attestation agreed.
func (c Counterparty) SentByOwner() bool { return c.sentByOwner }

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

// Sender is the OPTIONAL outbound seam a connector implements when its provider
// can transmit a message as the connected user. Type-asserted like Watcher and
// Backfiller, so the frozen Connector interface is unchanged and a capture-only
// provider simply does not implement it.
//
// Send MUST be idempotent on msg.MessageID. Job delivery is at-least-once, so a
// provider that retransmits on a retry mails the recipient twice; a connector
// whose provider can look up a prior send by RFC822 Message-ID must do so
// whenever msg.Attempt > 0 and return the existing receipt instead.
//
// That obligation has a precondition, and an implementation MUST refuse a
// message that fails it (OutboundMessage.Validate) before any provider I/O: an
// identity the prior-send lookup cannot search for makes the idempotency
// guarantee unkeepable, and transmitting anyway is the double-send this seam
// exists to prevent.
type Sender interface {
	Send(ctx context.Context, auth Auth, msg OutboundMessage) (SendReceipt, error)
}

// OutboundMessage is one message to transmit, in provider-NEUTRAL form. The
// connector owns the wire encoding — Gmail takes base64url RFC822, Graph takes
// JSON — so no caller ever builds MIME. It is the mirror of Normalize, which
// owns decoding on the way in.
type OutboundMessage struct {
	To      []string
	Cc      []string
	Subject string
	Body    string // text/plain; the only body shape sent today

	// MessageID is the RFC822 message identity WITHOUT angle brackets —
	// "abc@host", never "<abc@host>". Stored and compared in this form because
	// that is how mail parsing yields it, so the copy the provider files back
	// into the mailbox carries a key that matches the one recorded at send.
	// The connector adds the brackets when it renders the header.
	MessageID string

	// InReplyTo threads onto an existing conversation, also unbracketed. Empty
	// starts a new thread.
	InReplyTo string

	// References is the unbracketed ancestry chain, oldest first.
	References []string

	// ListUnsubscribe and ListUnsubscribePost carry the RFC 8058 header pair for
	// a marketing send; both empty for a transactional purpose, which has nothing
	// to unsubscribe from.
	ListUnsubscribe     string
	ListUnsubscribePost string

	// Attempt is 0 on the first transmission and increments on every retry. It is
	// how a connector knows to run the prior-send lookup the contract requires.
	Attempt int
}

// ErrInvalidMessageID marks an outbound message carrying no usable RFC822
// identity. It is the idempotency contract failing its precondition: Send is
// required to be idempotent on MessageID, and an identity the provider's
// prior-send lookup cannot search for makes that guarantee unkeepable. A
// message sent under one would mail its recipient again on every retry, and
// the copy the provider files back would key onto no activity.
var ErrInvalidMessageID = errors.New("connector: outbound message carries no usable RFC822 message identity")

// maxMessageIDLen bounds a message identity at a length a header can actually
// carry. RFC 5322 caps a header line at 998 octets, and this system renders the
// identity into Message-ID, In-Reply-To and a References chain that holds
// several of them at once, so the usable ceiling is far below that line limit
// — 512 is already an order of magnitude above what any provider mints (a
// Gmail identity is around forty characters). The bound matters because an
// identity is not only rendered: it is READ BACK out of a provider response of
// up to 96 MiB and adopted as a natural key, a thread key and a log field. An
// unbounded "valid" identity is a remote party choosing how many bytes this
// installation stores per sent message.
const maxMessageIDLen = 512

// ValidMessageID reports whether id is a usable RFC822 message identity in the
// UNBRACKETED form this system stores and compares: an addr-spec with exactly
// one '@', both sides non-empty, no whitespace, angle brackets, or ASCII
// control character (the connector adds the brackets at the wire), and no
// longer than a header line can carry. Control characters are rejected
// wholesale, not just the tab/CR/LF an editor is likely to type: any of them
// would render a malformed Message-ID header on the wire, and a provider that
// mangles or strips one on receipt breaks the retry path's rfc822msgid: lookup
// — the search that stops an at-least-once redelivery from mailing the
// recipient twice.
//
// It is the ONE spelling of that question, so the identity a send transmits
// under, the identity a threading header is derived from, and the identity a
// provider reports back cannot disagree about what counts.
func ValidMessageID(id string) bool {
	if len(id) > maxMessageIDLen {
		return false
	}
	local, domain, found := strings.Cut(id, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") {
		return false
	}
	for _, r := range id {
		switch {
		case r == ' ' || r == '<' || r == '>':
			return false
		case r <= 0x1F || r == 0x7F: // the full ASCII control range (C0 + DEL)
			return false
		}
	}
	return true
}

// Validate refuses a message no provider should be handed. It is the sender
// boundary's own precondition — checked before any provider I/O, so a message
// that cannot be retried safely is never transmitted a first time.
func (m OutboundMessage) Validate() error {
	if !ValidMessageID(m.MessageID) {
		return ErrInvalidMessageID
	}
	return nil
}

// SendReceipt is what the provider confirmed: its own message identity, and
// the RFC822 identity the transmitted copy actually carries.
//
// The provider's CONVERSATION id is deliberately absent. This system threads on
// the RFC822 message identity — comms_outbound.thread_key and activity.thread_key
// both hold a Message-ID derived from References/In-Reply-To, which is what
// capture keys reply detection on. A provider's own conversation id (Gmail's
// threadId) lives in a different namespace, joins nothing here, and carrying it
// would invite a reader to key on a value no query reads.
//
// The RFC822 identity is the opposite case, and the distinction is worth
// holding onto: it joins everything here. A Message-ID is a REQUEST, not a
// guarantee — Gmail discards the client's and mints its own — so the identity
// this system records has to be the one the wire carries, not the one it asked
// for.
type SendReceipt struct {
	ProviderMessageID string
	// RFC822MessageID is the unbracketed Message-ID on the transmitted copy.
	//
	// EMPTY means "no re-key needed": the provider honoured the identity it was
	// given, does not report one, or could not be asked. All three degrade to
	// the same correct no-op, so a provider that never sets this field is
	// wrong about nothing.
	RFC822MessageID string
}
