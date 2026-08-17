// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What the pipeline trace needs to know about one activity, answered HERE
// rather than by the reader.
//
// The trace explains why a stage did or did not run. For the attention
// classifier that answer is a predicate this module owns, and a reader that
// re-spelled it would be a second copy: correct on the day it was written and
// silently wrong the first time the backlog's WHERE clause moved. That is the
// exact defect class the trace exists to expose, so reproducing it inside the
// trace would be the worst possible bug.
//
// ClassifyBacklogPredicate is therefore ONE string, used verbatim by the backlog
// query and by the single-row question below. They cannot disagree, because
// there is nothing to disagree with.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ClassifyBacklogPredicate is what makes an activity eligible for the batched
// attention-label pass (ADR-0063, §2.8).
//
// The unqualified `activity` references are deliberate: both callers query the
// table under its own name, so the fragment drops in verbatim. Aliasing it in
// one caller would silently break the correlated subquery.
//
// Mail from a sender whose disposition is still open is excluded (ADR-0072 §5):
// labelling routes attention, and a message from someone the workspace has not
// yet decided is a counterparty at all should not compete for it — least of all
// when the pending question may resolve to `noise` and hide it.
const ClassifyBacklogPredicate = `capture_label IS NULL
	  AND captured_by LIKE 'connector:%' AND kind = 'email'
	  AND archived_at IS NULL
	  AND NOT EXISTS (
	    SELECT 1 FROM capture_pending_counterparty p
	     WHERE p.email = activity.counterparty_email
	       AND p.status IN ('pending', 'unsure'))`

// PipelineFacts is one activity's contribution to its own pipeline ladder.
//
// It is a bundle rather than four reads because the ladder needs all of it at
// once and every field comes off the same row — four round trips to assemble one
// drawer would be four times the cost for no separation anybody benefits from.
type PipelineFacts struct {
	// HasPersonLink answers the person-creation rung. The link is the durable
	// signal: it is also how the nightly reconcile finds its work, so a
	// link-less connector activity is precisely what "not linked yet" means.
	HasPersonLink bool

	// CaptureLabel is the attention label, empty when unlabelled.
	CaptureLabel string

	// ClassifyEligible is the shared predicate's own answer for this row.
	ClassifyEligible bool

	// ClassifyReason names WHY it is ineligible, and is empty when it is
	// eligible or already labelled. Four honest answers, not one: telling a
	// member "the classifier reads email only" about an archived email would be
	// a wrong why, which is worse than no why at all.
	ClassifyReason pipelinetrace.Reason
}

// ReadPipelineFacts answers the derived rungs for one activity.
//
// It takes BOTH gates readActivity takes — the object grant and the row scope —
// rather than trusting the caller to have taken them. Anything that returns a
// record is a read, and this returns facts ABOUT a message: whether a contact
// was made from it, what the classifier concluded. The object grant alone would
// answer for an activity the caller's row scope excludes.
//
// The compose assembler gates first as well, and that is still not sufficient:
// a guard the caller supplies is a guard the next caller can forget, and this
// read is reachable from two doors.
func (s *Store) ReadPipelineFacts(ctx context.Context, id ids.UUID) (PipelineFacts, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return PipelineFacts{}, err
	}
	var out PipelineFacts
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureActivityVisible(ctx, tx, id); err != nil {
			return err
		}
		var label *string
		var kind, capturedBy string
		var archived, senderUndecided, eligible bool
		row := tx.QueryRow(ctx, `
			SELECT
			  EXISTS (SELECT 1 FROM activity_link l
			           WHERE l.activity_id = activity.id AND l.person_id IS NOT NULL),
			  capture_label,
			  kind,
			  captured_by,
			  archived_at IS NOT NULL,
			  EXISTS (SELECT 1 FROM capture_pending_counterparty p
			           WHERE p.email = activity.counterparty_email
			             AND p.status = ANY($2)),
			  (`+ClassifyBacklogPredicate+`)
			FROM activity
			WHERE id = $1`, id, pipelinetrace.OpenDispositionStatuses())
		if err := row.Scan(&out.HasPersonLink, &label, &kind, &capturedBy,
			&archived, &senderUndecided, &eligible); err != nil {
			return err
		}
		if label != nil {
			out.CaptureLabel = *label
		}
		out.ClassifyEligible = eligible
		out.ClassifyReason = classifyReason(classifySubject{
			label: out.CaptureLabel, kind: kind, capturedBy: capturedBy,
			archived: archived, senderUndecided: senderUndecided,
		})
		return nil
	})
	if err != nil {
		return PipelineFacts{}, fmt.Errorf("activities: reading pipeline facts: %w", err)
	}
	return out, nil
}

// kindEmail is the ONE transport the attention classifier reads. Spelled once
// because the predicate, the exclusion rule and the test all name it, and three
// literals drift the day a second transport is admitted.
const kindEmail = "email"

// classifySubject is the row as the exclusion rules see it. A struct rather than
// five parameters because two of them are booleans, and a call site reading
// `classifyReason(label, kind, by, true, false)` cannot be checked by eye.
type classifySubject struct {
	label           string
	kind            string
	capturedBy      string
	archived        bool
	senderUndecided bool
}

// classifyReason names the FIRST exclusion that applies, in the order the
// predicate itself tests them, so the reason a member reads is the one the
// backlog query actually acted on rather than whichever happened to be checked
// last.
//
// A labelled row is not excluded at all — it already ran — so it reports no
// reason and the rung reads as done.
func classifyReason(in classifySubject) pipelinetrace.Reason {
	switch {
	case in.label != "":
		return ""
	case !isConnectorCaptured(in.capturedBy):
		return pipelinetrace.ReasonNotConnectorCaptured
	case in.kind != kindEmail:
		return pipelinetrace.ReasonTransportNotRead
	case in.archived:
		return pipelinetrace.ReasonArchived
	case in.senderUndecided:
		return pipelinetrace.ReasonSenderUndecided
	default:
		// Eligible and unlabelled: the batch has simply not reached it.
		return pipelinetrace.ReasonAwaitingBatch
	}
}

// isConnectorCaptured mirrors the predicate's `LIKE 'connector:%'` exactly.
//
// HasPrefix rather than a length test: `%` matches the empty string, so a bare
// "connector:" is eligible to the backlog. A stricter check here would call
// that row "not captured by a connector" while the classifier queued it — the
// reader and the rule disagreeing about one row, which is the one thing this
// pairing exists to prevent.
func isConnectorCaptured(capturedBy string) bool {
	return strings.HasPrefix(capturedBy, "connector:")
}
