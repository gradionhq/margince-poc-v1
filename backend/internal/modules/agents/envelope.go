// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The one envelope every tool result carries (BYO-RES-1).
//
// WHAT IT IS FOR. A tool used to answer with a bare payload, so an agent had no
// way to ask the two questions it needs answered about every answer: how do I
// know this, and how current is it? Worse, it could not tell "no records
// matched" from "records matched and you may not see them" — the one confusion
// that produces a WRONG answer rather than a thin one, because the agent then
// tells a person a record does not exist when it does.
//
// NONE OF THE SIX FIELDS IS NEW INFORMATION. Each reports something the call
// already computed and then discarded: the correlation id the HTTP layer mints
// per request, the freshness the datasource seam already populates, the trust
// label the seam already carries, the records the handler already read. That is
// what makes this half ratifiable now (A138) — it reports, it does not judge.
//
// WHAT IS DELIBERATELY ABSENT. `coverage` and `omitted_sections` (BYO-RES-3).
// Both are evaluative — judgements the system must MAKE rather than facts it
// holds — and both are one-way doors: the moment a client branches on
// `complete_exact`, that word is frozen for every surface that comes after. They
// ship with the surface that first produces them, and until then a result says
// what it found and what it warned about, and claims nothing about
// exhaustiveness. TestNoResultSchemaCarriesADeferredEnvelopeField holds that.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Envelope is what a tool answers with: the six descriptive fields, and the
// tool's own payload under `data`.
//
// The payload NESTS rather than merging into this object, so no tool's field
// name can ever collide with an envelope field — a merge would make the
// envelope's meaning depend on which tool answered, which is the opposite of
// what one envelope across every tool is for.
type Envelope struct {
	// SchemaVersion is the RESULT CONTRACT's version for this tool, not the
	// product's: it is what lets a client tell "the shape changed" from "the
	// data changed", which are indistinguishable without it.
	SchemaVersion string `json:"schema_version"`
	// TraceID is the request's correlation id — the field that makes one tool
	// call findable in the audit log, instead of a timestamp search.
	TraceID   string    `json:"trace_id"`
	Freshness Freshness `json:"freshness"`
	// Trust is the tier the material behind this answer arrived with. The
	// envelope CARRIES the label and never sets, raises or drops it: the
	// definitions are the threat model's and the propagation is trust
	// propagation's, cited here rather than re-decided.
	Trust string `json:"trust"`
	// Evidence and Warnings are never null on the wire. An agent reading
	// `null` has to decide whether it means "none" or "not computed", and only
	// one of those is true here.
	Evidence []EvidenceRef `json:"evidence"`
	Warnings []Warning     `json:"warnings"`
	// Data is the tool's own result, exactly as its handler marshalled it.
	Data json.RawMessage `json:"data"`
}

// Freshness is mirror staleness, as the datasource seam reports it.
type Freshness struct {
	// LastSyncedAt is the OLDEST stamp among the records behind the answer —
	// the worst case rather than the flattering one, because a caller asking
	// "how current is this" is asking about the stalest part of it. Absent when
	// no record contributed (a tool answering from product-generated
	// configuration has nothing to be stale).
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	// Authoritative is false when ANY contributing record was mirror-backed and
	// pending sync. In system-of-record mode it is always true.
	Authoritative bool `json:"authoritative"`
}

// EvidenceRef is one record an answer rests on. It is comparable, which is what
// lets the collector dedupe by value: a record read twice in one call is one
// piece of evidence.
type EvidenceRef struct {
	RecordType datasource.EntityType `json:"record_type"`
	RecordID   ids.UUID              `json:"record_id"`
}

// Warning is a non-fatal condition the caller must not have to infer.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// The trust tiers, as the threat model defines them: T0 is product-generated
// content, T1 is content an authenticated internal user typed, T2 is captured
// or external and is UNTRUSTED. This surface reads a tier off what the seam
// tells it and never invents one.
const (
	trustSystem   = "t0"
	trustInternal = "t1"
	trustExternal = "t2"
)

// The warning codes this surface raises. They are a closed set here because a
// caller branches on them; the message beside each is what a person reads.
const (
	// warningRowScopeFiltered is BYO-RES-2 on the wire. It says the QUERY was
	// bounded, never how many rows the bound removed — a count would be exactly
	// the side channel existence-hiding exists to close.
	warningRowScopeFiltered = "row_scope_filtered"
	// warningSweepTruncated marks an answer that stopped at its own cap, so a
	// model does not read a bounded list as an exhaustive one.
	warningSweepTruncated = "sweep_truncated"
)

const rowScopeFilteredMessage = "This answer covers only the records your access allows. " +
	"Others may exist that you cannot see, so report what you found rather than what exists."

// envelopeFacts collects, over ONE tool call, what the answer rests on.
//
// It lives on the context because the facts are produced deep inside a handler —
// at newWireRecord, which is the one place a datasource.Record becomes tool
// output — and consumed at Registry.Invoke, which is the one place a result
// leaves this surface. Threading a collector through every handler signature
// would put the same value in thirty argument lists to be forwarded unread.
//
// The mutex is not contention insurance: a handler is free to fan out its reads
// (the relationship tools do), and a collector written from two goroutines
// without one is a data race the race detector would find in the integration
// lane rather than a defect anyone reasoned about.
type envelopeFacts struct {
	mu            sync.Mutex
	evidence      []EvidenceRef
	seen          map[EvidenceRef]struct{}
	oldestSync    time.Time
	sawRecord     bool
	authoritative bool
	warnings      []Warning
	warned        map[string]struct{}
}

func newEnvelopeFacts() *envelopeFacts {
	return &envelopeFacts{
		seen:          map[EvidenceRef]struct{}{},
		warned:        map[string]struct{}{},
		authoritative: true,
	}
}

type envelopeFactsKey struct{}

// withEnvelopeFacts opens a collector for one call. Registry.Invoke opens
// exactly one; nothing else does, so a nested read cannot start a second and
// have its evidence disappear when it closes.
func withEnvelopeFacts(ctx context.Context) (context.Context, *envelopeFacts) {
	facts := newEnvelopeFacts()
	return context.WithValue(ctx, envelopeFactsKey{}, facts), facts
}

// factsOn returns the call's collector, or nil when there is none. Nil is a
// legal state rather than a defect: a handler exercised directly by a unit test
// has no envelope around it, and a note that goes nowhere is better than a
// panic in a test that is about something else.
func factsOn(ctx context.Context) *envelopeFacts {
	facts, _ := ctx.Value(envelopeFactsKey{}).(*envelopeFacts)
	return facts
}

// noteRecord records one served record: its ref becomes evidence, and its
// freshness folds into the answer's.
func noteRecord(ctx context.Context, rec datasource.Record) {
	facts := factsOn(ctx)
	if facts == nil {
		return
	}
	facts.mu.Lock()
	defer facts.mu.Unlock()
	facts.addRef(EvidenceRef{RecordType: rec.Ref.Type, RecordID: rec.Ref.ID})
	facts.sawRecord = true
	facts.authoritative = facts.authoritative && rec.Freshness.Authoritative
	if stamp := rec.Freshness.LastSyncedAt; !stamp.IsZero() &&
		(facts.oldestSync.IsZero() || stamp.Before(facts.oldestSync)) {
		facts.oldestSync = stamp
	}
}

// noteEvidence records a record an answer RESTS ON without serving it whole —
// the deals a slipping report names, the records a context assembly summarized.
// Those answers are about records too, and an evidence list that skipped them
// would make the tools that summarize the least sourced ones.
func noteEvidence(ctx context.Context, recordType datasource.EntityType, id ids.UUID) {
	facts := factsOn(ctx)
	if facts == nil || id.IsZero() {
		return
	}
	facts.mu.Lock()
	defer facts.mu.Unlock()
	facts.addRef(EvidenceRef{RecordType: recordType, RecordID: id})
}

// noteRecordDerived says the answer was BUILT from workspace records without
// naming any — an aggregate report's rows, a free/busy window computed from
// calendar entries.
//
// It exists because the trust label would otherwise be wrong in the dangerous
// direction. An answer that touched no record is t0, product-generated content,
// and t0 is the HIGHEST tier: labelling a report over live records as t0 would
// RAISE the trust of everything behind it, which is the one thing this envelope
// may never do. So a derived answer declares that records are underneath it, and
// lands at t1 with an empty evidence list — which is the honest pair, because
// there is no record reference for a caller to follow to a row that was summed.
func noteRecordDerived(ctx context.Context) {
	facts := factsOn(ctx)
	if facts == nil {
		return
	}
	facts.mu.Lock()
	defer facts.mu.Unlock()
	facts.sawRecord = true
}

// noteWarning raises one condition, once. A sweep that hits its cap on every
// page of a fan-out should say so once, not once per page.
func noteWarning(ctx context.Context, code, message string) {
	facts := factsOn(ctx)
	if facts == nil {
		return
	}
	facts.mu.Lock()
	defer facts.mu.Unlock()
	if _, dup := facts.warned[code]; dup {
		return
	}
	facts.warned[code] = struct{}{}
	facts.warnings = append(facts.warnings, Warning{Code: code, Message: message})
}

// addRef appends one ref unless the call already carries it. The caller holds
// the mutex.
func (f *envelopeFacts) addRef(ref EvidenceRef) {
	if _, dup := f.seen[ref]; dup {
		return
	}
	f.seen[ref] = struct{}{}
	f.evidence = append(f.evidence, ref)
}

// sealEnvelope renders one handler's bytes as the result a client reads.
//
// It is called from Registry.Invoke and nowhere else, which is what makes the
// envelope true of the whole surface: a handler does not build one, so no
// handler can forget one, and a tool added tomorrow carries it without its
// author having read this file.
func sealEnvelope(spec mcp.ToolSpec, trace string, facts *envelopeFacts, data json.RawMessage) (json.RawMessage, error) {
	snapshot := facts.snapshot()
	sealed, err := json.Marshal(Envelope{
		SchemaVersion: spec.Version,
		TraceID:       trace,
		Freshness:     snapshot.Freshness,
		Trust:         snapshot.Trust,
		Evidence:      snapshot.Evidence,
		Warnings:      snapshot.Warnings,
		Data:          data,
	})
	if err != nil {
		return nil, fmt.Errorf("crmagents: cannot encode the result envelope for %s: %w", spec.Name, err)
	}
	return sealed, nil
}

// noteRowScope raises BYO-RES-2's warning when the caller's reads are bounded
// by their own row scope.
//
// It is a statement about the QUERY, never about the data: no count, no hint
// that anything was actually removed. That is the whole point — a count would be
// precisely the side channel existence-hiding closes, while saying nothing at
// all leaves "no records matched" and "records matched and you may not see them"
// rendering identically, which is how an agent ends up telling a person a record
// does not exist when it does.
//
// Only READ tools raise it. A write answers with the record the caller just
// acted on, so there is no withheld half for a warning to be about, and a
// warning on every write would be noise a model learns to skip.
func noteRowScope(ctx context.Context, spec mcp.ToolSpec) {
	if !spec.ReadOnly() {
		return
	}
	actor, ok := principal.Actor(ctx)
	if !ok || auth.Unbounded(actor) {
		return
	}
	noteWarning(ctx, warningRowScopeFiltered, rowScopeFilteredMessage)
}

// withTrace binds a correlation id when the caller opened no operation scope,
// and answers with the id either way.
//
// Binding rather than reporting an empty string is the useful half: the id is
// what makes a tool call findable in the audit log, and a write inside the
// handler reads the same key when it stamps its audit row and its outbox event.
// A call that arrived without one would otherwise put an unfindable trace_id on
// the wire AND write rows that share no trace with it. Every HTTP-borne call
// already carries the chassis's id, and this never replaces one.
func withTrace(ctx context.Context) (context.Context, string) {
	if bound, ok := principal.CorrelationID(ctx); ok {
		return ctx, bound.String()
	}
	minted := ids.NewV7()
	return principal.WithCorrelationID(ctx, minted), minted.String()
}

// envelopeSnapshot is the collected facts, read out as one consistent picture.
type envelopeSnapshot struct {
	Evidence  []EvidenceRef
	Warnings  []Warning
	Freshness Freshness
	Trust     string
}

// snapshot reads the collected facts out under one lock, with the slices copied
// so a handler that keeps noting after its result was rendered cannot move an
// answer already on the wire.
//
// The trust label can only ever report the LOWEST material behind the answer:
//
//   - no record at all → t0, product-generated content (the pipeline
//     configuration, a free/busy window computed from a calendar);
//   - a record from the native store → t1, what an internal user typed;
//   - any record the seam reported as non-authoritative → t2, mirror-backed
//     content from an incumbent system, which is external and untrusted.
//
// It never RAISES a tier, which is the never-launder rule this envelope is
// required to keep. Per-field tiers inside a record are a different question and
// stay where they are — this label is about the answer as a whole.
func (f *envelopeFacts) snapshot() envelopeSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := envelopeSnapshot{
		Evidence:  append(make([]EvidenceRef, 0, len(f.evidence)), f.evidence...),
		Warnings:  append(make([]Warning, 0, len(f.warnings)), f.warnings...),
		Freshness: Freshness{Authoritative: f.authoritative},
		Trust:     trustInternal,
	}
	switch {
	case !f.sawRecord:
		out.Trust = trustSystem
	case !f.authoritative:
		out.Trust = trustExternal
	}
	if !f.oldestSync.IsZero() {
		stamp := f.oldestSync
		out.Freshness.LastSyncedAt = &stamp
	}
	return out
}
