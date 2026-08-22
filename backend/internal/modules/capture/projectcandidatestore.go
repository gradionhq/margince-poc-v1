// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The project_link_candidate ledger's SQL, and the two reads the uncertain rung
// makes on the way to a candidate. Like the disposition ledger in pending.go,
// this file owns the table's statements and nothing else: the link a confirmed
// candidate becomes is the activities module's write, never this one's.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// withoutThreadRefusals drops every project a human already REFUSED for a
// sibling of this message in the same conversation.
//
// StageUnlessDeclined remembers a refusal per (activity, project), which is
// exactly right for the same message asked twice and useless for a reply: the
// reply is a new activity, so the engine has nothing to join and would ask the
// same question about the same conversation again. A conversation is about one
// body of work (the thread rung's premise), so a "no" on one message is a "no"
// on its siblings. Matched within one medium, under the same predicate the
// thread rung uses and for the same reason: a forged References root must not
// let a stranger's mail read a decision made about somebody else's chat. A
// held or archived sibling settles nothing here either, as it settles nothing
// in the thread rung — a restricted message is out of every read about the
// business, refusals included.
func withoutThreadRefusals(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, fields ActivityFields, activityID ids.ActivityID, live []LiveProject) ([]LiveProject, error) {
	if rec.ThreadKey == "" || len(live) == 0 {
		return live, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT c.project_id
		  FROM project_link_candidate c
		  JOIN activity a ON a.id = c.activity_id
		 WHERE a.thread_key = $1 AND a.id <> $2
		   AND a.kind = $3
		   AND a.channel_provider IS NOT DISTINCT FROM NULLIF($4, '')
		   AND a.restricted_at IS NULL AND a.archived_at IS NULL
		   AND c.status = 'rejected'`, rec.ThreadKey, activityID, fields.Kind, fields.ChannelProvider)
	if err != nil {
		return nil, fmt.Errorf("capture: reading the thread's refused projects: %w", err)
	}
	refused, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, fmt.Errorf("capture: reading the thread's refused projects: %w", err)
	}
	if len(refused) == 0 {
		return live, nil
	}
	refusedSet := make(map[ids.UUID]bool, len(refused))
	for _, id := range refused {
		refusedSet[id] = true
	}
	kept := live[:0]
	for _, project := range live {
		if !refusedSet[project.ID] {
			kept = append(kept, project)
		}
	}
	return kept, nil
}

// nearestProject ranks several live projects against the message by cosine
// similarity of stored embeddings and answers the winner — or nothing, when the
// message has no embedding yet, no project has one under the same model, the
// best is below the floor, or the best and the runner-up cannot be told apart.
//
// One query over the embedding table's primary key, no model call: the rung
// runs inside a capture, and a capture must not wait on a provider. A message
// embedded later is not re-asked; the nightly reconcile is where that belongs.
//
// Same model on both sides, or the distance operator compares vectors of
// different dimensions and the statement fails; chunks are folded by the best
// pair, so a long message matched by one passage still counts.
func nearestProject(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, live []LiveProject) (LiveProject, float64, bool, error) {
	projectIDs := make([]ids.UUID, 0, len(live))
	byID := make(map[ids.UUID]LiveProject, len(live))
	for _, project := range live {
		projectIDs = append(projectIDs, project.ID)
		byID[project.ID] = project
	}
	rows, err := tx.Query(ctx, `
		SELECT p.entity_id, max(1 - (p.embedding <=> a.embedding))::float8 AS similarity
		  FROM embedding a
		  JOIN embedding p ON p.entity_type = 'project' AND p.model = a.model
		 WHERE a.entity_type = 'activity' AND a.entity_id = $1
		   AND p.entity_id = ANY($2)
		 GROUP BY p.entity_id
		 ORDER BY similarity DESC, p.entity_id
		 LIMIT 2`, activityID, projectIDs)
	if err != nil {
		return LiveProject{}, 0, false, fmt.Errorf("capture: ranking the projects by similarity: %w", err)
	}
	type scored struct {
		ID         ids.UUID
		Similarity float64
	}
	ranked, err := pgx.CollectRows(rows, pgx.RowToStructByPos[scored])
	if err != nil {
		return LiveProject{}, 0, false, fmt.Errorf("capture: ranking the projects by similarity: %w", err)
	}
	if len(ranked) == 0 || ranked[0].Similarity < similarityFloor {
		return LiveProject{}, 0, false, nil
	}
	if len(ranked) == 2 && ranked[1].Similarity >= ranked[0].Similarity {
		return LiveProject{}, 0, false, nil
	}
	return byID[ranked[0].ID], ranked[0].Similarity, true, nil
}

// recordCandidate writes the ledger row for a candidate whose approval is
// already staged. ON CONFLICT on the live-row index: a replayed capture that
// joined the existing offer must not open a second question for the message.
func recordCandidate(ctx context.Context, tx pgx.Tx, candidate ProjectCandidate, proposalID ids.UUID) error {
	var field *string
	var start, end *int
	if span := candidate.Evidence; span != nil {
		field, start, end = &span.Field, &span.Start, &span.End
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO project_link_candidate
		       (activity_id, project_id, method, confidence, evidence_field, evidence_start, evidence_end, proposal_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (activity_id) WHERE status = 'pending' DO NOTHING`,
		candidate.ActivityID, candidate.Project.ID, candidate.Method, candidate.Confidence,
		field, start, end, proposalID)
	if err != nil {
		return fmt.Errorf("capture: recording the project candidate: %w", err)
	}
	return nil
}

// ResolveProjectCandidateTx closes the pending candidate that rode on one approval,
// on the caller's transaction — the decision's own, or the expiry sweep's, so
// the ledger can never disagree with the approval row about what was decided. The CAS on `pending`
// makes a replayed effect a no-op; an approval no candidate points at (the
// capture crashed between staging and recording) resolves nothing, and that is
// not an error: the approval itself is the record that the question was asked.
func ResolveProjectCandidateTx(ctx context.Context, tx pgx.Tx, proposalID ids.UUID, status string) error {
	if status != CandidateStatusConfirmed && status != CandidateStatusRejected && status != CandidateStatusExpired {
		return fmt.Errorf("capture: %q is not a decision a project candidate can take", status)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE project_link_candidate
		   SET status = $2, decided_at = now()
		 WHERE proposal_id = $1 AND status = 'pending'`, proposalID, status); err != nil {
		return fmt.Errorf("capture: resolving the project candidate for approval %s: %w", proposalID, err)
	}
	return nil
}
