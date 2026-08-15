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
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TraceOutcome is what the pipeline did. Two vocabularies live in one column
// because they share a row shape, but they do NOT share a unit and must never
// be summed together: the ingress values are per MESSAGE, the resolution values
// are per SENDER. One stranger who writes five times is five deferred messages
// and one resolution.
type TraceOutcome string

// The ingress outcomes — one per message, written by the Sink.
//
// TraceCaptured and TraceFault can BOTH apply to one message: a derivation
// fault is contained by a savepoint and the message still commits. That is why
// these are not a partition and the read reports them as counts rather than as
// shares of a total.
const (
	TraceCaptured   TraceOutcome = "captured"
	TraceInternal   TraceOutcome = "internal"
	TraceSuppressed TraceOutcome = "suppressed"
	TraceDeferred   TraceOutcome = "deferred"
	TraceFault      TraceOutcome = "fault"
)

// The resolution outcomes — one per sender, written by the verdict engine and
// by the human decisions that close what it could not settle.
const (
	TraceVerdictReal  TraceOutcome = "verdict_real"
	TraceVerdictNoise TraceOutcome = "verdict_noise"
	TraceUnsure       TraceOutcome = "unsure"
	TraceAccepted     TraceOutcome = "accepted"
	TraceDeclined     TraceOutcome = "declined"
	TraceAgedOut      TraceOutcome = "aged_out"
)

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

	ActivityID    ids.UUID
	DispositionID ids.UUID

	// ChannelIdentity reports that SourceID is a provider ACCOUNT id rather than
	// a message id — which is personal data, and is hashed on write.
	ChannelIdentity bool

	// Counterparty and Subject are written only when the deployment turned
	// payload capture on.
	Counterparty string
	Subject      string
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
	var counterparty, subject *string
	if payloads {
		counterparty = nonEmpty(strings.ToLower(strings.TrimSpace(clampRunes(in.Counterparty, maxTraceAddressChars))))
		subject = nonEmpty(clampRunes(in.Subject, maxTraceSubjectChars))
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO capture_trace (workspace_id, user_id, connector, source_system, source_id,
		                           outcome, reason, activity_id, disposition_id,
		                           counterparty, subject)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10)
		-- The conflict target SPELLS the index's expression, COALESCE and all: a
		-- bare column list does not match an expression index, and Postgres
		-- answers that with an error on every insert -- which, on the capture
		-- transaction, would fail every capture in the deployment.
		ON CONFLICT (workspace_id, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		             source_system, source_id, outcome) DO NOTHING`,
		nullableID(in.UserID), in.Connector, in.SourceSystem,
		traceSourceID(in.SourceID, in.ChannelIdentity),
		string(in.Outcome), in.Reason, nullableID(in.ActivityID), nullableID(in.DispositionID),
		counterparty, subject)
	if err != nil {
		return fmt.Errorf("capture: recording the pipeline trace: %w", err)
	}
	return nil
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
func clampRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	return string([]rune(text)[:max])
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
