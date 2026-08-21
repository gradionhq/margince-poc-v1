// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Filing a captured message under a project (PROJ-FORM-1..3): a deterministic
// ladder, run AFTER the capture transaction committed, first match wins, no
// model call anywhere in it.
//
// Most mail belongs to no project and that is the correct answer. The ladder is
// therefore allowed to conclude nothing — it never guesses, and it never fails
// a capture.
//
// The first two rungs read the timeline rows capture itself writes — the
// thread's siblings and the activity's own links — and the third touches no
// project SQL at all: which project a subject's tokens name is asked through
// the ProjectKeyMatcher seam, which compose implements from the module that
// owns the project table. A module never imports a sibling.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// ProjectKeyMatcher resolves the subject-line rung of the ladder: which LIVE
// project one of these tokens is the key of. It is a seam because `project`
// belongs to another module and capture must not import a sibling; compose
// injects the implementation.
//
// Ambiguity is the matcher's to report, not the caller's to reconstruct: two
// distinct projects named in one subject means the message says nothing
// reliable, so the matcher answers with no project rather than picking one.
type ProjectKeyMatcher interface {
	MatchProjectKey(ctx context.Context, tokens []string) (ids.UUID, error)
}

// WithProjectAttribution returns a copy that files captured activities under a
// project. A nil matcher leaves capture exactly as it is without one: messages
// land, and none of them is attributed — which is also what every rung
// concluding nothing looks like, so no caller has to special-case the absence.
func (s *Sink) WithProjectAttribution(matcher ProjectKeyMatcher) *Sink {
	c := *s
	c.projectKeys = matcher
	return &c
}

// attributeProject runs the ladder for one freshly captured activity and writes
// at most one project link.
//
// Post-commit, in its own transaction, for the reason ensureCounterparty is:
// the timeline row must never be lost to an attribution fault, and the capture
// budget must not wait on this. A fault is recorded for the nightly reconcile
// and never returned — the message is already on the timeline and stays there.
func (s *Sink) attributeProject(ctx context.Context, rec connector.NormalizedRecord, ref datasource.EntityRef) {
	if s.projectKeys == nil {
		return
	}
	activityID := ids.From[ids.ActivityKind](ref.ID)
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		projectID, err := s.decideProject(ctx, tx, rec, activityID)
		if err != nil || projectID.IsZero() {
			return err
		}
		return linkActivityToProject(ctx, tx, activityID, projectID)
	})
	if err != nil {
		s.logProjectAttributionFault(ctx, rec, err)
	}
}

// logProjectAttributionFault records a failed attribution in system_log, on its
// own transaction — the one this fault came out of is already rolled back. The
// activity stands, filed under nothing, and the nightly reconcile re-runs the
// ladder over it; there is no partial state to repair, because a failed
// transaction wrote no link.
//
// It logs rather than returns for the reason every post-commit step does: the
// message is on the timeline and nothing here may take it off.
func (s *Sink) logProjectAttributionFault(ctx context.Context, rec connector.NormalizedRecord, cause error) {
	detail := map[string]any{
		fieldReason:       "project_attribution_failed",
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldError:        cause.Error(),
	}
	// The natural key of a channel message embeds the customer's account id,
	// and this fault can land after an erasure committed — logEnsureFault
	// withholds it for that reason and so does this.
	if rec.Counterparty.ChannelIdentity.Provider == "" {
		detail[fieldSourceID] = rec.NaturalKey.SourceID
	}
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		_, logErr := storekit.LogSystem(ctx, tx, "capture_project_attribution_fault", detail)
		return logErr
	})
	if err != nil {
		slog.ErrorContext(ctx, "capture: recording the project-attribution fault", "err", err, "cause", cause)
	}
}

// decideProject is the ladder itself: thread stickiness, then the deal the
// activity is filed under, then the subject's tokens. Zero means no rung
// matched, which is the ordinary answer for most mail.
//
// Nothing here checks for an existing project link, and nothing needs to. This
// runs only for an activity the capture transaction just INSERTED, so there is
// no earlier link to find, and the link it writes is the only one there will
// ever be: uq_activity_link_project admits one per activity, and
// linkActivityToProject writes ON CONFLICT DO NOTHING. Replacing a filing is a
// human's relink alone.
func (s *Sink) decideProject(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, activityID ids.ActivityID) (ids.UUID, error) {
	for _, rung := range []func() (ids.UUID, error){
		func() (ids.UUID, error) { return threadProject(ctx, tx, rec.ThreadKey, activityID) },
		func() (ids.UUID, error) { return dealProject(ctx, tx, activityID) },
		func() (ids.UUID, error) { return s.subjectProject(ctx, rec) },
	} {
		projectID, err := rung()
		if err != nil || !projectID.IsZero() {
			return projectID, err
		}
	}
	return ids.Nil, nil
}

// subjectProject asks the key matcher about the subject's tokens. A subject
// with no token that could BE a key never reaches the seam: the shape rules out
// most words, and asking anyway would be one query per captured message to
// learn what the tokenizer already knows.
func (s *Sink) subjectProject(ctx context.Context, rec connector.NormalizedRecord) (ids.UUID, error) {
	fields, ok := rec.Fields.(ActivityFields)
	if !ok {
		return ids.Nil, nil
	}
	tokens := projectKeyCandidates(fields.Subject)
	if len(tokens) == 0 {
		return ids.Nil, nil
	}
	return s.projectKeys.MatchProjectKey(ctx, tokens)
}

// threadProject is the stickiness rung: a conversation is about one body of
// work, so a sibling message already filed under a project settles where this
// one belongs.
//
// Siblings CAN disagree, because a human may relink any one of them, so the
// most recent one wins: the latest filing is the freshest statement about where
// the conversation stands.
func threadProject(ctx context.Context, tx pgx.Tx, threadKey string, activityID ids.ActivityID) (ids.UUID, error) {
	if threadKey == "" {
		return ids.Nil, nil
	}
	var projectID ids.UUID
	// A held or archived sibling settles nothing. A legal hold takes a message
	// out of every read that answers a question about the business, and this is
	// one of them: letting a restricted message decide where later mail is
	// filed would put its content back into circulation by proxy.
	err := tx.QueryRow(ctx, `
		SELECT al.project_id
		  FROM activity a
		  JOIN activity_link al ON al.activity_id = a.id AND al.entity_type = 'project'
		 WHERE a.thread_key = $1 AND a.id <> $2
		   AND a.restricted_at IS NULL AND a.archived_at IS NULL
		 ORDER BY a.occurred_at DESC
		 LIMIT 1`, threadKey, activityID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, nil
	}
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: reading the thread's project: %w", err)
	}
	return projectID, nil
}

// dealProject is the inheritance rung: a message filed under a deal that
// belongs to a project belongs to that project too. The deal's own rollup is
// the claim; this rung only follows it.
func dealProject(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID) (ids.UUID, error) {
	var projectID ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT d.project_id
		  FROM activity_link al
		  JOIN deal d ON d.id = al.deal_id
		 WHERE al.activity_id = $1 AND al.entity_type = 'deal'
		   AND d.project_id IS NOT NULL AND d.archived_at IS NULL
		 LIMIT 1`, activityID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, nil
	}
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: reading the deal's project: %w", err)
	}
	return projectID, nil
}

// linkActivityToProject writes the one link the ladder concluded, with its own
// audit row: this write commits in its own transaction, so it cannot ride the
// capture's. Why no outbox row rides with it is auditProjectAttribution's own
// comment.
//
// The row-scope check first, exactly as every other link writer does: a
// connector must not plant a link to a row its granting human could not see. A
// project outside that scope is not an error here — the message stands, filed
// under nothing — because refusing it would turn one member's narrower scope
// into a capture fault the operator has to read.
//
// ON CONFLICT DO NOTHING because uq_activity_link_project admits exactly one
// project link per activity: a concurrent pass that got there first is the
// system working, not a collision to report. Nothing is audited when nothing
// landed — a no-op writes no audit noise.
func linkActivityToProject(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, projectID ids.UUID) error {
	if err := auth.EnsureLinkTarget(ctx, tx, string(datasource.EntityProject), projectID); err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("capture: project link target: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO activity_link (activity_id, entity_type, project_id)
		VALUES ($1, 'project', $2)
		ON CONFLICT DO NOTHING`, activityID, projectID)
	if err != nil {
		return fmt.Errorf("capture: filing the activity under its project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return auditProjectAttribution(ctx, tx, activityID, projectID)
}

// auditProjectAttribution records the link the ladder just wrote, under the
// same action the human-driven relink uses: a reader asking "how did this
// message end up on this project?" must find one answer whether a person or the
// ladder filed it, and the audit row's principal already says which.
//
// No public event rides with it, and that is deliberate rather than an
// omission. This link is part of landing ONE captured message, which
// activity.captured already announced in the transaction just before; a second
// event saying the same message changed would have subscribers reacting twice
// to one arrival. activity.updated is the activities module's type to mean
// what it means, and capture does not get to redefine it as "a message
// arrived, again".
func auditProjectAttribution(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, projectID ids.UUID) error {
	_, err := storekit.Audit(ctx, tx, "activity_relink", "activity", activityID.UUID, nil,
		map[string]any{"entity_type": string(datasource.EntityProject), "entity_id": projectID})
	return err
}
