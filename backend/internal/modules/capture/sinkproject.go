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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// StampProjectCorrespondence marks the activity just filed under a project as
// commercial correspondence, inside the transaction that wrote the link (D5).
// `activity` belongs to another module, so this is a seam for the same reason
// ProjectKeyMatcher is one.
type StampProjectCorrespondence func(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, projectID ids.UUID) error

// ProjectAttribution is the ladder's two halves, which are one seam because
// they are one obligation: deciding a message belongs to a project and
// classifying the correspondence that decision qualifies (D5).
//
// They are ONE struct rather than two parameters so a caller cannot supply the
// first without the second. The two failure modes are not comparable —
// attributing nothing is an ordinary configuration, attributing without
// classifying leaves a Handelsbrief the next erasure destroys — so the wiring
// that could get it wrong does not exist rather than being checked for.
type ProjectAttribution struct {
	Keys  ProjectKeyMatcher
	Stamp StampProjectCorrespondence
	// Candidates and Propose are the uncertain rung's two halves
	// (projectcandidate.go): which live projects the message reaches, and the
	// engine that asks a human. Both or neither — a finder with nobody to ask
	// would compute candidates nothing records, and a proposer with nothing to
	// propose is inert — so the rung runs only when both are wired, and the
	// zero value leaves the ladder at its three deterministic rungs.
	Candidates ProjectCandidateFinder
	Propose    ProjectCandidateProposer
}

// WithProjectAttribution returns a copy that files captured activities under a
// project and classifies what it files. The zero value leaves capture exactly
// as it is without one: messages land, and none of them is attributed — which
// is also what every rung concluding nothing looks like, so no caller has to
// special-case the absence.
func (s *Sink) WithProjectAttribution(attribution ProjectAttribution) *Sink {
	c := *s
	c.projectKeys = attribution.Keys
	c.stampProject = attribution.Stamp
	c.projectCandidates = attribution.Candidates
	c.proposeCandidate = attribution.Propose
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
	if s.projectKeys == nil || s.stampProject == nil {
		return
	}
	activityID := ids.From[ids.ActivityKind](ref.ID)
	var verdict ladderVerdict
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		verdict, err = s.decideProject(ctx, tx, rec, activityID)
		if err != nil || verdict.project.IsZero() {
			return err
		}
		return linkActivityToProject(ctx, tx, activityID, verdict.project, s.stampProject)
	})
	if err == nil && verdict.asks {
		// After the ladder's transaction, not inside it: staging takes the
		// approval row lock itself and has no in-transaction form
		// (StageUnlessDeclined), and the ladder wrote nothing a candidate
		// would need to be atomic with.
		err = s.offerCandidate(ctx, verdict.candidate)
	}
	if err != nil {
		s.logProjectAttributionFault(ctx, rec, err)
	}
}

// offerCandidate asks a human about the uncertain rung's conclusion and records
// that it asked. Staging first, then the ledger row naming the staged offer:
// the engine is the one that knows whether this pairing was already refused,
// and a candidate row for a question nobody is being asked would count as
// awaiting a decision forever.
//
// A crash between the two leaves an offer with no ledger row. The decision
// still applies — the effect carries everything it needs in the payload and
// resolves the ledger by proposal id, finding nothing — so the only loss is the
// coverage count, and the next capture in the thread opens a fresh question.
func (s *Sink) offerCandidate(ctx context.Context, candidate ProjectCandidate) error {
	proposalID, staged, err := s.proposeCandidate.ProposeProjectCandidate(ctx, candidate)
	if err != nil {
		return fmt.Errorf("capture: offering the project candidate: %w", err)
	}
	if !staged {
		return nil
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		return recordCandidate(ctx, tx, candidate, proposalID)
	})
}

// ladderVerdict is what one run of the ladder concluded: a project to file
// under, a candidate to ask about (asks), or neither. Never both — a candidate
// is only computed when every deterministic rung found nothing.
type ladderVerdict struct {
	project   ids.UUID
	candidate ProjectCandidate
	asks      bool
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
// A rung that finds a project the caller cannot use — archived, or outside
// their object grant and row scope — counts as NO MATCH and the ladder carries
// on to the next rung. Stopping there instead would let an unusable T0 answer
// silently suppress a perfectly good T1 subject key, and the member would see
// a message filed under nothing with no way to tell why.
//
// Nothing here checks for an existing project link, and nothing needs to. This
// runs only for an activity the capture transaction just INSERTED, so there is
// no earlier link to find, and the link it writes is the only one there will
// ever be: uq_activity_link_project admits one per activity, and
// linkActivityToProject writes ON CONFLICT DO NOTHING. Replacing a filing is a
// human's relink alone.
//
// Below the three deterministic rungs sits the uncertain one
// (candidateProject), which reaches the verdict as a CANDIDATE rather than a
// project: it is asked only when the rungs above found nothing, and what it
// finds is offered to a human, never filed.
func (s *Sink) decideProject(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, activityID ids.ActivityID) (ladderVerdict, error) {
	fields, isActivity := rec.Fields.(ActivityFields)
	if !isActivity {
		return ladderVerdict{}, nil
	}
	grant, err := readableProjectScope(ctx)
	if err != nil || grant.deniedOutright {
		// No project grant at all: this principal may not learn a project id,
		// let alone have one copied onto their timeline. Not a fault — a role
		// that never sees projects is a legitimate role.
		return ladderVerdict{}, err
	}
	for _, rung := range []func() (ids.UUID, error){
		func() (ids.UUID, error) { return threadProject(ctx, tx, rec, fields, activityID) },
		func() (ids.UUID, error) { return dealProject(ctx, tx, activityID) },
		func() (ids.UUID, error) { return s.subjectProject(ctx, fields) },
	} {
		projectID, err := rung()
		if err != nil || !projectID.IsZero() {
			return ladderVerdict{project: projectID}, err
		}
	}
	candidate, asks, err := s.candidateProject(ctx, tx, rec, fields, activityID)
	return ladderVerdict{candidate: candidate, asks: asks}, err
}

// subjectProject asks the key matcher about the subject's bracketed keys. A
// subject carrying none never reaches the seam: the bracket rule rules out
// almost every message, and asking anyway would be one query per captured
// message to learn what the tokenizer already knows.
func (s *Sink) subjectProject(ctx context.Context, fields ActivityFields) (ids.UUID, error) {
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
// Matched within ONE medium, and that is a security control rather than a
// convenience — the same one sinkreply.go's reply detection applies, for the
// same reason and with the same predicate. thread_key is a single flat
// namespace holding both a mail thread root and a channel's
// `<provider>:<bot>:<chat>` key, and the mail half is attacker-supplied: it is
// the message's own References root, chosen verbatim by the sender. Without the
// medium match, a forged References header naming a Telegram conversation — a
// bot id being public and a private chat's id being the user's own — files the
// forger's mail onto the project of a conversation they were never in.
//
// kind alone stopped discriminating at ADR-0107/A158, when every channel row
// took kind='message' whatever carried it, so the match is on the PAIR:
// IS NOT DISTINCT FROM lets one statement serve both, mail comparing NULL to
// NULL and a channel comparing provider to provider.
//
// Siblings CAN disagree, because a human may relink any one of them, so the
// most recent one wins: the latest filing is the freshest statement about where
// the conversation stands. id breaks a timestamp tie so two siblings stamped
// the same second cannot return either project run to run.
func threadProject(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields, activityID ids.ActivityID) (ids.UUID, error) {
	if rec.ThreadKey == "" {
		return ids.Nil, nil
	}
	args := []any{rec.ThreadKey, activityID, fields.Kind, fields.ChannelProvider}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scoped, err := projectScopeClause(ctx, "p", arg)
	if err != nil {
		return ids.Nil, err
	}
	var projectID ids.UUID
	// A held or archived sibling settles nothing. A legal hold takes a message
	// out of every read that answers a question about the business, and this is
	// one of them: letting a restricted message decide where later mail is
	// filed would put its content back into circulation by proxy.
	err = tx.QueryRow(ctx, `
		SELECT p.id
		  FROM activity a
		  JOIN activity_link al ON al.activity_id = a.id AND al.entity_type = 'project'
		  JOIN project p ON p.id = al.project_id
		 WHERE a.thread_key = $1 AND a.id <> $2
		   AND a.kind = $3
		   AND a.channel_provider IS NOT DISTINCT FROM NULLIF($4, '')
		   AND a.restricted_at IS NULL AND a.archived_at IS NULL
		   AND p.archived_at IS NULL`+scoped+`
		 ORDER BY a.occurred_at DESC, a.id DESC
		 LIMIT 1`, args...).Scan(&projectID)
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
//
// An activity can carry several deal links, so this reads the DISTINCT projects
// they roll up to rather than picking one: two deals naming two different
// projects is ambiguity, and the ladder answers ambiguity the same way here as
// it does for a subject naming two keys — with no project at all. LIMIT 2 is
// what makes that a bounded question; the ORDER BY makes the one-match case
// reproducible rather than dependent on scan order.
func dealProject(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID) (ids.UUID, error) {
	args := []any{activityID}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scoped, err := projectScopeClause(ctx, "p", arg)
	if err != nil {
		return ids.Nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT p.id
		  FROM activity_link al
		  JOIN deal d ON d.id = al.deal_id
		  JOIN project p ON p.id = d.project_id
		 WHERE al.activity_id = $1 AND al.entity_type = 'deal'
		   AND d.archived_at IS NULL
		   AND p.archived_at IS NULL`+scoped+`
		 ORDER BY p.id
		 LIMIT 2`, args...)
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: reading the deal's project: %w", err)
	}
	matched, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: reading the deal's project: %w", err)
	}
	if len(matched) != 1 {
		return ids.Nil, nil
	}
	return matched[0], nil
}

// linkActivityToProject writes the one link the ladder concluded.
//
// It REQUIRES activity.update, and that is not belt-and-braces over the
// activity.create the capture already checked. Filing an activity under a
// project changes who can reach it and bumps activity.version, which is the pin
// a staged approval re-checks before it redeems — so this is an update of the
// activity by every test that matters, and the audit row says so
// (auditProjectAttribution). A principal that may create captured mail but not
// change it attributes nothing, which is the honest outcome rather than a
// silent widening of what create means.
//
// Denial is not a fault: a connector role without activity.update is an
// ordinary configuration, so its mail lands filed under nothing.
//
// Then the row-scope check on the target, exactly as every other link writer
// does: a connector must not plant a link to a row its granting human could not
// see. That check is narrower than the ladder's own, which already refused an
// unreadable project — it stays because this function is the write, and a write
// re-checks its own target rather than trusting the caller to have done it.
//
// ON CONFLICT DO NOTHING because uq_activity_link_project admits exactly one
// project link per activity: a concurrent pass that got there first is the
// system working, not a collision to report. Nothing is audited or bumped when
// nothing landed — a no-op writes no audit noise and moves no version.
func linkActivityToProject(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, projectID ids.UUID, stamp StampProjectCorrespondence) error {
	if err := auth.Require(ctx, "activity", principal.ActionUpdate); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return nil
		}
		return err
	}
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
		// A link was already there. Stamp anyway before returning: the early
		// exit used to skip it on the reasoning that whoever filed the link
		// also stamped it, and that is exactly the guarantee nothing enforces
		// — the migration's own backfill exists because pre-stamp project
		// links do. A missed stamp leaves a Handelsbrief an erasure destroys;
		// a repeated one costs a no-op, because the stamp is idempotent. The
		// echo path made the same call for the same reason
		// (activities/messageidentity.go).
		//
		// Nothing else runs: no version bump and no audit row, because nothing
		// changed about where the activity is filed.
		return stamp(ctx, tx, activityID, projectID)
	}
	// Touch the activity ROW, not just its link table, for the reason the
	// human relink path does it (activities/lifecycle.go): a staged approval
	// pins activity.version, and that pin is what stands between an approved
	// "send this on this conversation" and the conversation being repointed
	// before the approval redeems. Filing changes who the activity reaches, so
	// it must move the version the pin re-checks. The trigger
	// (set_updated_at_bump_version) does the bump; this only has to be a
	// genuine UPDATE of the row.
	if _, err := tx.Exec(ctx, `UPDATE activity SET updated_at = now() WHERE id = $1`, activityID); err != nil {
		return fmt.Errorf("capture: bumping the filed activity's version: %w", err)
	}
	// The link is what qualifies the correspondence, so the stamp commits with
	// it (D5).
	if err := stamp(ctx, tx, activityID, projectID); err != nil {
		return fmt.Errorf("capture: classifying the filed activity's correspondence: %w", err)
	}
	return auditProjectAttribution(ctx, tx, activityID, projectID)
}

// auditProjectAttribution records the link the ladder just wrote, under the
// same action the human-driven relink uses: a reader asking "how did this
// message end up on this project?" must find one answer whether a person or the
// ladder filed it, and the audit row's principal already says which.
//
// activity_relink maps to activity.update in auditActionGrant, and the caller
// really does require that grant (linkActivityToProject) — so the
// authorization_rule this row renders names the rule that actually admitted the
// write. audit_log is append-only, so a verb whose write path never checked the
// grant it claims would be an uncorrectable lie about who was allowed to do
// what.
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
