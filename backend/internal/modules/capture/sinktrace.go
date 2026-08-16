// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The Sink's side of the 24-hour trace: how a normalized record becomes a
// TraceEntry, and the two outcomes that cannot be written on the capture
// transaction at all.
//
// It lives beside the Sink rather than inside it because sink.go is already at
// the length where a reader stops holding it at once, and because every call
// site there wants to be one line — a trace is an aside, and it should read
// like one.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// WithTracePayloads returns a copy that keeps each traced message's sender and
// subject for the trace's 24 hours. It is the deployment's
// capture.trace_payloads posture and nothing else decides it: there is no API
// and no per-workspace switch, because a member must not be able to turn on
// retention of their colleagues' subjects.
func (s *Sink) WithTracePayloads(on bool) *Sink {
	c := *s
	c.tracePayloads = on
	return &c
}

// traceTx records one pipeline decision on the CALLER's transaction.
//
// Every Sink call site passes the transaction it is already inside, so the
// trace commits with the decision or rolls back with it. The two paths where
// that is impossible — a decision whose transaction is doomed — go through
// traceAfterRollback below instead.
func (s *Sink) traceTx(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord,
	stage pipelinetrace.Stage, outcome TraceOutcome, reason string,
) error {
	return Trace(ctx, tx, s.traceEntry(ctx, rec, stage, outcome, reason), s.tracePayloads)
}

// traceInvisibleIncumbent records the one decision whose own transaction did
// not survive to carry it.
//
// skipInvisibleIncumbent is returned as an ERROR from inside the capture
// transaction, so a trace written there rolls back with it — and from the
// member's side that outcome is a message sitting in their own mailbox that
// simply never arrives, which is exactly what this surface exists to explain.
// It therefore gets its own transaction, as logEnsureFault already does for the
// same reason.
//
// Best effort, and it says so by logging rather than returning: the message did
// not land either way, and failing a capture in order to record why it failed
// would be the tail wagging the dog.
func (s *Sink) traceInvisibleIncumbent(ctx context.Context, rec connector.NormalizedRecord, cause error) {
	if !errors.Is(cause, errInvisibleIncumbent) {
		return
	}
	// The ACTIVITY WRITE stage, not the ladder: this is the capture transaction
	// refusing a replay whose incumbent row sits outside the reader's scope, and
	// the ladder never got to decide anything about it.
	entry := s.traceEntry(ctx, rec, pipelinetrace.StageActivityWrite, TraceFault, TraceReasonInvisibleIncumbent)
	if err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return Trace(ctx, tx, entry, s.tracePayloads)
	}); err != nil {
		slog.ErrorContext(ctx, "capture: recording the invisible-incumbent trace", "err", err, "cause", cause)
	}
}

// traceEntry builds the entry one decision records.
//
// The MEMBER comes from capturePrincipal rather than from actor.OnBehalfOf
// directly, so that a principal which sets only UserID cannot silently demote a
// personal row to the workspace view — where a manager would then read one
// member's mailbox traffic. The difference is one fallback and the whole
// access-control axis of the feature.
func (s *Sink) traceEntry(ctx context.Context, rec connector.NormalizedRecord,
	stage pipelinetrace.Stage, outcome TraceOutcome, reason string,
) TraceEntry {
	_, owner := capturePrincipal(ctx)
	// Read from the COUNTERPARTY on purpose, unlike traceConnector below, and
	// the two are not inconsistent: this flag does not ask whether the message
	// arrived on a channel, it asks whether SourceID is a provider ACCOUNT id
	// that has to be hashed. Which is a question about how the record names its
	// human. Do not "correct" it to match its neighbour.
	//
	// It is imperfect — a connector whose natural key is a message id on BOTH
	// branches has one branch hashed and one not (issue #1465) — but the error
	// is toward hashing, and the fix belongs with the natural key rather than
	// here.
	channel := rec.Counterparty.ChannelIdentity.Provider != ""
	return TraceEntry{
		Stage:           stage,
		UserID:          owner,
		Connector:       traceConnector(rec),
		SourceSystem:    rec.NaturalKey.SourceSystem,
		SourceID:        rec.NaturalKey.SourceID,
		Outcome:         outcome,
		Reason:          reason,
		ChannelIdentity: channel,
		Counterparty:    rec.Counterparty.Email,
		Subject:         traceSubject(rec),
	}
}

// traceConnector names which transport carried this message, as an ID.
//
// A channel record answers with its provider (`telegram`), the same spelling
// activity.channel_provider carries and the key /v1/channel-providers resolves
// to a label. Everything else answers with the natural key's SOURCE SYSTEM
// (`gmail`, `imap`, `ext:<unit>:<system>`).
//
// The provider is read from the RECORD's own fields — the value that becomes
// activity.channel_provider — and not from the counterparty's channel identity.
// They are different questions: how the message travelled, versus how it names
// its human. A connector may answer the second with an address alone (a
// mention, where the address IS the identity) while carrying every message on
// the same transport, so a counterparty-derived answer gives one connector two
// ids, splits it into two groups in a list that groups by connector, and leaves
// one of them unresolvable to a label.
//
// Not captureSource: that is the provenance CHANNEL, and a connector may set it
// to `<system>:<id>` — several do — so it identifies one message rather than the
// transport that carried it, and would put a message id in a column meant to
// group by connector.
//
// A DISPLAY label is deliberately not stored either: it is derived from the id
// or compiled into the composition root, so it is a property of the running
// binary, and two deploys' traces would disagree about the same transport with
// no row having changed.
func traceConnector(rec connector.NormalizedRecord) string {
	if fields, ok := rec.Fields.(ActivityFields); ok && fields.ChannelProvider != "" {
		return fields.ChannelProvider
	}
	return rec.NaturalKey.SourceSystem
}

// traceSubject is the message's subject when the record carries one. A lead has
// no subject and is not traced at all (the trace covers messages); a channel
// message usually has none, and an absent subject is left absent rather than
// filled with a placeholder a reader would take for the provider's own text.
func traceSubject(rec connector.NormalizedRecord) string {
	fields, ok := rec.Fields.(ActivityFields)
	if !ok {
		return ""
	}
	return fields.Subject
}

// traceActivity records a landed message together with the row it became and
// what the ladder concluded about its sender.
//
// ONE row per message, not one per gate: a suppressed message landed AND was
// suppressed, and writing both would count it twice in a funnel a member reads
// as "what happened to my mail". The ladder's outcome is the more specific
// answer, so it wins; an empty one means the ordinary case.
func (s *Sink) traceActivity(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord,
	activityID ids.UUID, decision counterpartyDecision,
) error {
	outcome := decision.traceOutcome
	if outcome == "" {
		outcome = TraceCaptured
	}
	entry := s.traceEntry(ctx, rec, pipelinetrace.StageTierLadder, outcome, decision.traceReason)
	entry.ActivityID = activityID
	return Trace(ctx, tx, entry, s.tracePayloads)
}
