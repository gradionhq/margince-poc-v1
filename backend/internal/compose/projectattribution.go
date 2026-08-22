// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The project attribution ladder's uncertain rung, composed: which live
// projects a captured message reaches (a walk over activities' account reach
// and deals' project table, which capture may import neither of), the offer
// the approvals engine stages for it, and the two decisions.
//
// The offer is lopsided on purpose, like the counterparty review's. CONFIRM
// files the message under the project through the activities module's own
// relink path — the write a human typing the relink performs, with the same
// audit row and the same retention stamp. REJECT writes nothing: the message
// stays filed under nothing, the candidate records the refusal, and the engine
// remembers it so the same pairing is never offered again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// projectAttributionActor is the principal the confirm executes as, in the
// contract's declared grammar for captured_by.
const projectAttributionActor = "agent:" + approvals.KindProjectAttribution

// projectAttributionProposal is the staged offer's payload: the pairing, the
// words a reviewer recognizes it by, and how the ladder got there.
//
// The subject is here and NOT in the summary: the summary is copied onto the
// staging's audit row, which no erasure reaches, while the payload is blanked
// when the cited message is redacted (privacy's citation scrub).
type projectAttributionProposal struct {
	ActivityID      ids.UUID `json:"activity_id"`
	ProjectID       ids.UUID `json:"project_id"`
	ProjectName     string   `json:"project_name"`
	ProjectKey      string   `json:"project_key,omitempty"`
	ActivitySubject string   `json:"activity_subject"`
	Method          string   `json:"method"`
	Confidence      float64  `json:"confidence"`
}

// The two payload fields the identity is keyed on, spelled once so the
// identity and the payload's JSON tags cannot drift apart.
const (
	attributionFieldActivity = "activity_id"
	attributionFieldProject  = "project_id"
)

// projectAttributionIdentity is what a refusal is REMEMBERED by: the message
// and the project — never the payload, which carries a confidence and a
// subject that a later pass may compute or bound differently.
func projectAttributionIdentity(activityID, projectID ids.UUID) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]string{
		attributionFieldActivity: activityID.String(), attributionFieldProject: projectID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("compose: encoding the project attribution identity: %w", err)
	}
	return body, nil
}

// projectCandidateFinder answers capture.ProjectCandidateFinder over the pool:
// the live projects of every account the activity reaches, within the
// caller's project row scope.
type projectCandidateFinder struct{}

// LiveProjectsReached walks the same three arms every reader of an account's
// timeline walks (activities.OrgReachSet) and lists the live projects of the
// accounts at the end of them. Live is neither archived nor closed. The
// caller's project scope bounds it, as every rung above this one is bounded:
// a project the caller may not read is not one they may be asked about.
func (projectCandidateFinder) LiveProjectsReached(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID) ([]capture.LiveProject, error) {
	args := []any{activityID}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := auth.ScopeClauseFor(ctx, string(datasource.EntityProject), "p", arg)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		scope = " AND " + scope
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT p.id, p.name, coalesce(p.key, '')
		  FROM (`+activities.OrgReachSet()+`) ro
		  JOIN project p ON p.organization_id = ro.organization_id
		 WHERE ro.activity_id = $1
		   AND p.archived_at IS NULL AND p.phase <> 'closed'`+scope+`
		 ORDER BY p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var live []capture.LiveProject
	for rows.Next() {
		var project capture.LiveProject
		if err := rows.Scan(&project.ID, &project.Name, &project.Key); err != nil {
			return nil, err
		}
		live = append(live, project)
	}
	return live, rows.Err()
}

// projectCandidateProposer answers capture.ProjectCandidateProposer over one
// approvals service.
type projectCandidateProposer struct{ svc *approvals.Service }

// ProposeProjectCandidate stages the offer. StageUnlessDeclined, not Stage: a
// human's "no" on this pairing is durable, and the rung re-derives the same
// pairing on every capture that reaches the same account.
//
// The message is cited as evidence so that an erasure redacting it blanks
// this card too (privacy's citation scrub) — the citation is the tie that
// survives the redaction. No citation when the subject is empty: there is
// then nothing of the message on the card to blank.
func (p projectCandidateProposer) ProposeProjectCandidate(ctx context.Context, candidate capture.ProjectCandidate) (ids.UUID, bool, error) {
	identity, err := projectAttributionIdentity(candidate.ActivityID.UUID, candidate.Project.ID)
	if err != nil {
		return ids.Nil, false, err
	}
	body, err := json.Marshal(projectAttributionProposal{
		ActivityID:      candidate.ActivityID.UUID,
		ProjectID:       candidate.Project.ID,
		ProjectName:     candidate.Project.Name,
		ProjectKey:      candidate.Project.Key,
		ActivitySubject: candidate.Subject,
		Method:          candidate.Method,
		Confidence:      candidate.Confidence,
	})
	if err != nil {
		return ids.Nil, false, fmt.Errorf("compose: encoding the project attribution proposal: %w", err)
	}
	digest := sha256.Sum256(body)
	in := approvals.StageInput{
		Kind:           approvals.KindProjectAttribution,
		ProposedChange: body,
		DiffHash:       hex.EncodeToString(digest[:]),
		Identity:       identity,
		TargetType:     string(datasource.EntityActivity),
		TargetID:       candidate.ActivityID.UUID,
		Summary:        "File a captured message under " + projectLabel(candidate.Project) + "?",
		JoinPending:    true,
	}
	if candidate.Subject != "" {
		in.Evidence = []approvals.Evidence{{
			Snippet:    candidate.Subject,
			SourceType: string(datasource.EntityActivity),
			SourceID:   candidate.ActivityID.UUID,
		}}
	}
	id, staged, err := p.svc.StageUnlessDeclined(ctx, in)
	if err != nil {
		return ids.Nil, false, err
	}
	return id.UUID, staged, nil
}

// projectLabel is how the summary names a project: the name, with the key
// beside it when there is one.
func projectLabel(project capture.LiveProject) string {
	if project.Key == "" {
		return project.Name
	}
	return project.Name + " (" + project.Key + ")"
}

// projectAttributionConfirmEffect builds the approvals.ApprovedEffect for kind
// "project_attribution": the human agreed, so the message is filed under the
// project through the relink path and the candidate closes as confirmed, in
// the redemption's own transaction.
//
// A message a human filed elsewhere while the offer waited is NOT re-filed:
// the human's filing is the fresher statement, the approval is spent, the
// candidate closes as rejected, and nothing is written. Filed under the SAME
// project already, the relink is a no-op and the candidate still confirms.
func projectAttributionConfirmEffect(svc *approvals.Service) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		var proposal projectAttributionProposal
		if err := json.Unmarshal(proposedChange, &proposal); err != nil {
			return fmt.Errorf("compose: decoding the project attribution proposal: %w", err)
		}
		decider, ok := principal.Actor(ctx)
		if !ok {
			return errors.New("compose: project attribution confirm without a deciding principal")
		}
		// The link is written by the confirm executor on behalf of the human
		// who released it: their approval is on the decision's own audit row,
		// and the relink's audit row carries the machine provenance that says
		// the filing came out of the ladder rather than a typed relink.
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         projectAttributionActor,
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		return svc.RedeemAndApply(ctx, approvalID, approvals.KindProjectAttribution, diffHash, func(tx pgx.Tx) error {
			return applyProjectAttribution(execCtx, tx, approvalID.UUID, proposal)
		})
	}
}

// applyProjectAttribution is the confirm's write, on the redemption's
// transaction.
func applyProjectAttribution(ctx context.Context, tx pgx.Tx, approvalID ids.UUID, proposal projectAttributionProposal) error {
	filedElsewhere, err := filedUnderAnotherProject(ctx, tx, proposal.ActivityID, proposal.ProjectID)
	if err != nil {
		return err
	}
	if filedElsewhere {
		return capture.ResolveProjectCandidateTx(ctx, tx, approvalID, capture.CandidateStatusRejected)
	}
	if _, err := activities.RelinkActivityInTx(ctx, tx, ids.From[ids.ActivityKind](proposal.ActivityID),
		activities.RelinkActivityInput{
			EntityType: string(datasource.EntityProject),
			EntityID:   proposal.ProjectID,
		}); err != nil {
		return fmt.Errorf("compose: filing the message under the confirmed project: %w", err)
	}
	return capture.ResolveProjectCandidateTx(ctx, tx, approvalID, capture.CandidateStatusConfirmed)
}

// filedUnderAnotherProject answers whether the activity already carries a
// project link to a DIFFERENT project — a yes/no, never the project itself:
// the confirm has no business learning where a human filed the message, only
// whether its own filing would contradict them.
func filedUnderAnotherProject(ctx context.Context, tx pgx.Tx, activityID, projectID ids.UUID) (bool, error) {
	var elsewhere bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM activity_link
		                WHERE activity_id = $1 AND entity_type = 'project' AND project_id <> $2)`,
		activityID, projectID).Scan(&elsewhere)
	if err != nil {
		return false, fmt.Errorf("compose: reading whether the message is filed elsewhere: %w", err)
	}
	return elsewhere, nil
}

// projectAttributionDeclineEffect records a human's "no" on the candidate, in
// the decision's own transaction. Nothing else happens: the message stays
// filed under nothing, and StageUnlessDeclined keeps the pairing from being
// offered again.
func projectAttributionDeclineEffect() approvals.DeclinedEffect {
	return func(ctx context.Context, tx pgx.Tx, approvalID ids.ApprovalID, _ json.RawMessage) error {
		return capture.ResolveProjectCandidateTx(ctx, tx, approvalID.UUID, capture.CandidateStatusRejected)
	}
}

// withCandidateSeams adds the uncertain rung's two seams to a ladder wiring,
// over a staging-only approvals service: the sink never decides, so it needs
// no effect registered.
func withCandidateSeams(attribution capture.ProjectAttribution, pool *pgxpool.Pool) capture.ProjectAttribution {
	attribution.Candidates = projectCandidateFinder{}
	attribution.Propose = projectCandidateProposer{svc: approvals.NewService(InstallationDB(pool))}
	return attribution
}
