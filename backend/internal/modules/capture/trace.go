// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// What the pipeline decided about one message, written where the decision is
// made and deleted 24 hours later.
//
// It exists because every other record of these decisions is unattributable: an
// activity's audit row says a message WAS captured, and the decisions that
// captured nothing are `system_log` breadcrumbs carrying a natural key and no
// member. So the one question the people using this system actually ask — what
// happened to MY messages — had no answer short of psql.
//
// A trace is not a record. Nothing links to it, nothing derives from it, and it
// writes no audit row of its own: one per captured message would double the
// ledger to say what `audit_log` already says.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// TraceOutcome is what the pipeline did with one MESSAGE.
//
// One row per message PER OUTCOME, which is not quite a partition and the
// difference is worth knowing before reading a funnel: a message that landed
// and was later refused on a replay holds both `captured` and `fault`, so the
// counts sum to DECISIONS rather than to messages. That is the honest unit —
// both things happened — but it means a funnel total is not a message count.
//
// The verdict engine's answers are deliberately NOT here. They are facts about
// a sender's open question rather than about a message — the disposition ledger
// already holds them, with an owner, a status, a kind and its timestamps — and
// copying them in would file a sender's answer under one arbitrary message of
// the several it covers, then collide with itself the moment that sender were
// re-judged inside the window. The read joins the ledger on activity_id, which
// needs no address and so works with payloads off.
type TraceOutcome string

// The five, in the order a message meets them.
const (
	TraceCaptured   TraceOutcome = "captured"
	TraceInternal   TraceOutcome = "internal"
	TraceSuppressed TraceOutcome = "suppressed"
	TraceDeferred   TraceOutcome = "deferred"
	TraceFault      TraceOutcome = "fault"
)

// LadderDispositionOutcomes are the outcomes the tier ladder records a
// disposition ledger row for. They are the rows with a question of their own,
// which is what lets the trace read report a verdict to a message that raised
// one and to no other.
//
// Named here, beside the outcomes themselves, because two places have to agree
// about the list and they are in different files: the read's join
// (tracestore.go) and the ladder that writes them (sinkensure.go). A tier that
// starts recording a disposition joins this list, and the read follows.
var LadderDispositionOutcomes = []TraceOutcome{TraceDeferred, TraceSuppressed}

// The reasons that change what an outcome MEANS, rather than merely annotating
// it. Each of these exists because the outcome alone would give a user a
// confident wrong answer.
const (
	// TraceReasonDeferralCapped is a deferral the ceiling refused. The message
	// stands unjudged: nothing is pending and no verdict is coming, so a plain
	// `deferred` would tell somebody to wait for an answer that never arrives.
	TraceReasonDeferralCapped = "deferral_capped"
	// TraceReasonNoisePrior is mail from a sender a previous verdict judged
	// noise. The activity commits — so the naive trace is `captured` — and the
	// hide sweep then archives it. "Why did this not appear?" answered with "it
	// was captured" is worse than no answer at all.
	TraceReasonNoisePrior = "noise_prior"
	// TraceReasonDecidedPrior is mail from a sender already rejected by a human
	// or suppressed by the registry. Same shape as the above.
	TraceReasonDecidedPrior = "decided_prior"
	// TraceReasonNoGrantingHuman is the derivation fault that returns before the
	// guarded arm, so it reaches no other fault path.
	TraceReasonNoGrantingHuman = "no_granting_human"
	// gateFaultReason is the tier ladder failing on its own terms. It is a CLASS
	// and never the gate's error text: that text carries table and constraint
	// names, and this one is rendered on a member's screen.
	gateFaultReason = "derivation_failed"
	// traceReasonNoCounterparty is a message that landed and named nobody a
	// record could be created for -- an automated notice with no readable
	// sender, or a colleague-only thread whose external party left. Unexported:
	// no call site outside this module has occasion to state it.
	traceReasonNoCounterparty = "no_counterparty"
	// TraceReasonInvisibleIncumbent is a replayed message whose incumbent row
	// lies outside the reader's row scope. It is refused as an error from inside
	// the capture transaction, so its trace CANNOT be written there.
	TraceReasonInvisibleIncumbent = "invisible_incumbent"
)

// The bounds the payload columns are held to, matching the migration's CHECKs.
// A remote party does not choose how much a diagnostic table stores.
const (
	maxTraceAddressChars = 320
	maxTraceSubjectChars = MaxCapturedSubjectChars
)

// TraceEntry is one decision, as its call site knows it.
type TraceEntry struct {
	// Stage is which step of the pipeline produced this decision. It is what
	// lets a member read the row as a rung on a path rather than as a bare
	// outcome, and it decides whether the row counts in the funnel — a stage
	// that has not opted in does not touch the metric.
	Stage pipelinetrace.Stage

	// UserID is the member whose credential produced the record, and zero for a
	// workspace-owned connection. That difference IS the access-control axis:
	// a zero here makes the row readable by a manager, so a call site that
	// cannot name the member must not guess.
	UserID ids.UUID

	// Connector is the provider id (`gmail`, `telegram`, `ext:<unit>:<system>`),
	// never a display label — a label is a property of the running binary, so
	// two deploys' traces would disagree about the same transport.
	Connector    string
	SourceSystem string
	SourceID     string

	Outcome TraceOutcome
	Reason  string

	ActivityID ids.UUID

	// ChannelIdentity reports that SourceID is a provider ACCOUNT id rather than
	// a message id — which is personal data, and is hashed on write.
	ChannelIdentity bool

	// Counterparty and Subject are written only when the deployment turned
	// payload capture on.
	Counterparty string
	Subject      string
}

// traceOutcomeTotals counts what this PROCESS has traced since it started, one
// counter per outcome.
//
// A counter rather than a gauge over the table, and in memory rather than in
// SQL, for two reasons. /metrics is process-global and binds no workspace, so a
// windowed query would have to read every tenant's rows on every scrape — of a
// table whose whole lifecycle is insert-then-delete, competing with the writes
// and the sweep. And "how many messages were dropped as internal" is a rate an
// operator alerts on, which is what a monotonic counter is for; the 24-hour
// window belongs to the member's screen, not to a scrape.
var traceOutcomeTotals sync.Map // TraceOutcome -> *atomic.Uint64

// TraceOutcomeTotals reports this process's counts, for the metrics endpoint.
func TraceOutcomeTotals() map[string]uint64 {
	out := map[string]uint64{}
	traceOutcomeTotals.Range(func(key, value any) bool {
		outcome, isOutcome := key.(TraceOutcome)
		counter, isCounter := value.(*atomic.Uint64)
		if isOutcome && isCounter {
			out[string(outcome)] = counter.Load()
		}
		return true
	})
	return out
}

func countTraced(outcome TraceOutcome) {
	stored, _ := traceOutcomeTotals.LoadOrStore(outcome, &atomic.Uint64{})
	if counter, ok := stored.(*atomic.Uint64); ok {
		counter.Add(1)
	}
}

// Trace records one decision on the CALLER's transaction, so a trace can
// neither outlive nor precede the thing it describes: a rolled-back capture
// leaves no explanation of a message that does not exist.
//
// payloads is the deployment's capture.trace_payloads posture. With it off —
// the default — the content columns are left NULL rather than written and
// masked later, because a column that is never populated cannot leak.
func Trace(ctx context.Context, tx pgx.Tx, in TraceEntry, payloads bool) error {
	if err := in.validate(); err != nil {
		return err
	}
	counterparty, subject, payloadErr := tracePayload(ctx, tx, in, payloads)
	if payloadErr != nil {
		return payloadErr
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO capture_trace (user_id, connector, source_system, source_id,
		                           stage, outcome, reason, activity_id, counterparty, subject)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10)
		-- The conflict target SPELLS the index's expression, COALESCE and all: a
		-- bare column list does not match an expression index, and Postgres
		-- answers that with an error on every insert -- which, on the capture
		-- transaction, would fail every capture in the deployment.
		ON CONFLICT (COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		             source_system, source_id, stage, outcome) DO NOTHING`,
		nullableID(in.UserID), in.Connector, in.SourceSystem,
		traceSourceID(in.SourceID, in.ChannelIdentity),
		string(in.Stage), string(in.Outcome), in.Reason, nullableID(in.ActivityID),
		counterparty, subject)
	if err != nil {
		return fmt.Errorf("capture: recording the pipeline trace: %w", err)
	}
	// Only what the statement actually inserted. ON CONFLICT DO NOTHING swallows
	// a replayed decision, and the internal gate fires before the dedupe
	// watermark — so counting the call rather than the row would inflate
	// `internal` by one per re-walked poll and disagree with the funnel beside
	// it, which counts rows.
	//
	// It still counts a decision this transaction later abandons: a counter
	// cannot un-count, which is the honest limit of a process counter and part
	// of why the member's screen reads the table instead.
	//
	// And only FUNNEL stages count. The gate is structural rather than a
	// convention because the alternative is undetectable: every stage writing
	// through here is a funnel stage today, so an unconditional count is
	// correct today and silently wrong the first time a stage that is not part
	// of the member's five outcomes writes a row — inflating
	// margince_capture_outcomes_total with no diff to any metric code and no
	// test failing. Opt-in in the registry is what an operator's alert rests on.
	if tag.RowsAffected() > 0 && pipelinetrace.CountsInFunnel(in.Stage) {
		countTraced(in.Outcome)
	}
	return nil
}

// tracePayload decides what content this row may carry, and is the only place
// that decides it.
//
// DELETION STICKS AT THE WRITE. recordDisposition already refuses to write an
// erased subject's address into the ledger, for the reason its own comment
// gives: a fresh row would restore that address and their header display name
// in a new table. A diagnostic trace is exactly such a table, and payload mode
// is exactly when it would happen — so it asks the same list, rather than
// leaving the invariant to hold in one module and not the one beside it.
//
// The check costs a query per traced message, and only in payload mode, which
// is an operator's opt-in diagnostic posture rather than the steady state.
func tracePayload(ctx context.Context, tx pgx.Tx, in TraceEntry, payloads bool) (*string, *string, error) {
	address := strings.ToLower(strings.TrimSpace(in.Counterparty))
	// An unattributable row carries no content, whatever the posture.
	//
	// A zero UserID means one of two things: a workspace-owned channel binding,
	// which genuinely has no member — or a personal connector whose principal
	// arrived without one, which is the `no_granting_human` fault. The row is
	// written either way, because a member is owed the answer that their message
	// was handled; but the second case is ALSO the class a manager can read, and
	// a fault must not be the thing that carries somebody's mail across that
	// boundary. ChannelIdentity is what tells the two apart.
	if in.UserID.IsZero() && !in.ChannelIdentity {
		return nil, nil, nil
	}
	if !payloads || address == "" {
		// No payload posture, or nothing to say: the columns stay NULL rather
		// than being written and masked. A column never populated cannot leak.
		return nil, nil, nil
	}
	suppressed, err := storekit.EmailSuppressed(ctx, tx, address)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: checking the suppression list for a trace payload: %w", err)
	}
	if suppressed {
		// The decision is still traced — a member is owed the answer that their
		// message was handled — but with no trace of WHO, which is the part the
		// erasure removed.
		return nil, nil, nil
	}
	return nonEmpty(clampRunes(address, maxTraceAddressChars)),
		nonEmpty(clampRunes(in.Subject, maxTraceSubjectChars)), nil
}

// validate refuses an entry that would record a decision nobody can read back.
// It is a programming error rather than a user one, so it names the field.
func (in TraceEntry) validate() error {
	switch {
	case in.Connector == "":
		return fmt.Errorf("capture: a trace entry names no connector (outcome %q)", in.Outcome)
	case in.SourceSystem == "" || in.SourceID == "":
		return fmt.Errorf("capture: a trace entry carries no natural key (outcome %q)", in.Outcome)
	case in.Outcome == "":
		return fmt.Errorf("capture: a trace entry names no outcome")
	case in.Stage == "":
		return fmt.Errorf("capture: a trace entry names no pipeline stage (outcome %q)", in.Outcome)
	case !pipelinetrace.CanStore(in.Stage):
		// A stage the registry does not list as stored would violate the
		// column's CHECK at the database and fail the whole capture. Naming it
		// here says which stage was wrong; the constraint violation would not.
		return fmt.Errorf("capture: %q is not a stage this pipeline stores", in.Stage)
	}
	return nil
}

// traceSourceID is the natural key half this table stores.
//
// A CHANNEL record's source id embeds the customer's provider account id, which
// this module already treats as personal data — logEnsureFault omits it and
// refuseErasedChannelAccount will not name it. So it is hashed here: dedupe is
// equality and a hash equals itself, so the unique index is unaffected, while an
// erasure landing inside the 24-hour window has nothing here left to reach.
//
// Mail keeps its message id. ADR-0082 §1 permits a drop to record the external
// id, and that permission was written about mail, where the id identifies a
// message rather than a person — and where it is what makes a support question
// answerable at all.
func traceSourceID(sourceID string, channelIdentity bool) string {
	if !channelIdentity {
		return sourceID
	}
	sum := sha256.Sum256([]byte(sourceID))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// clampRunes bounds text by RUNES rather than bytes, so a multi-byte subject is
// cut at a character boundary and the column's CHECK sees what this function
// counted.
func clampRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

func nonEmpty(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

// nullableID renders a zero id as SQL NULL. A zero user id is not a member with
// no name — it is a workspace-owned connection, which the read distinguishes by
// exactly this NULL.
func nullableID(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}
