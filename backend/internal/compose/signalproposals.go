// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Turning an observation into an offer (the account-intelligence proposals
// arc).
//
// A signal says what the correspondence stated. It changes nothing. This is
// where the consequence is proposed: an account whose own mail ends the
// contract is OFFERED the move to former_customer, and a human accepts, edits
// or dismisses it. Nothing structural is written before that (GATE-AI-2) —
// the reconciler stages, and the effect writes only once someone has said yes.
//
// The model never stages. This whole file is deterministic: it reads open
// signals and open approvals, and it proposes exactly one thing per open
// contract_ended signal. Whatever the extraction site got wrong is a card
// somebody clears, and a card is where its influence stops.
//
// The precedent is org_name_promotion, not deal_follow_up: StageUnlessDeclined
// (durable rejection memory — StageOrJoinPendingInTx only dedupes against live
// pending rows and forgets rejections) and RedeemAndApply (redemption and the
// write in one transaction).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// lifecycleProposalKind names the staged offer in the review queue.
	lifecycleProposalKind = "lifecycle_change"
	// lifecycleProposalActor is the provenance the accepted write carries: the
	// stage came from the account's own correspondence, not from someone
	// typing it, and a later human edit must still win over it.
	lifecycleProposalActor = "agent:" + lifecycleProposalKind
	// lifecycleEnded is what a contract_ended signal proposes from every live
	// stage. The founder's own example is a record reading "Prospect" whose
	// mail ends a contract: the mail is the fact, and former_customer is what
	// it says, whether or not the record ever said customer.
	lifecycleEnded = "former_customer"
)

// lifecycleProposal is the staged change: which account, from what stage to
// what stage, and the signal that asked for it.
//
// The current stage is part of the payload because the card must show both
// sides — a reader deciding "is this right?" needs to see what the record says
// now, not only what it would say next.
type lifecycleProposal struct {
	OrganizationID ids.OrganizationID `json:"organization_id"`
	CurrentStage   string             `json:"current_lifecycle"`
	ProposedStage  string             `json:"proposed_lifecycle"`
	SignalID       ids.UUID           `json:"signal_id"`
	// Because is the signal's own summary, so the card can say why in the
	// words the conversation used rather than in a rule's words.
	Because string `json:"because"`
}

// lifecycleIdentity is what a refusal is REMEMBERED by: the account and the
// stage it was offered. Not the payload — the payload carries the signal's
// summary and the record's current stage, both of which move on their own, and
// a refusal keyed on the whole payload would be forgotten the first time
// either did, re-offering the same move on every pass until someone accepted.
func lifecycleIdentity(orgID ids.OrganizationID, stage string) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]string{
		paramOrganizationID: orgID.String(), "proposed_lifecycle": stage,
	})
	if err != nil {
		return nil, fmt.Errorf("compose: encoding the lifecycle proposal identity: %w", err)
	}
	return body, nil
}

// SignalProposer offers the consequence of a signal to a human.
type SignalProposer struct {
	pool      *pgxpool.Pool
	approvals *approvals.Service
	log       *slog.Logger
}

// NewSignalProposer builds the reconciler over the pool and one approvals
// service — the same instance that decides, so a released effect can redeem
// what it decides on.
func NewSignalProposer(pool *pgxpool.Pool, svc *approvals.Service, log *slog.Logger) *SignalProposer {
	return &SignalProposer{pool: pool, approvals: svc, log: log}
}

// contradiction is one account whose stage its own mail contradicts.
type contradiction struct {
	OrganizationID ids.OrganizationID
	Stage          string
	SignalID       ids.UUID
	Summary        string
}

// RunWorkspace offers a stage change for every account whose open
// contract_ended signal contradicts the stage it is filed under, and reports
// how many of those offers are now STANDING in the inbox.
//
// Standing, not newly created: an hourly pass over an unchanged account joins
// the offer it made last hour rather than stacking a second copy, and both
// count. The number a reader wants from this is "how many accounts are waiting
// on a decision", which is the same on every pass until someone decides.
//
// Staging happens OUTSIDE the signal-write transaction: StageUnlessDeclined
// has no in-tx variant, because it takes the approval row lock itself. A crash
// between the signal and the offer self-heals — the signal is still open on
// the next hourly pass, and this reads open signals rather than new ones.
func (p *SignalProposer) RunWorkspace(ctx context.Context) (int, error) {
	var found []contradiction
	if err := database.WithWorkspaceTx(ctx, p.pool, func(tx pgx.Tx) error {
		var err error
		found, err = readContradictions(ctx, tx)
		return err
	}); err != nil {
		return 0, fmt.Errorf("compose: reading the accounts their mail contradicts: %w", err)
	}
	standing := 0
	for _, account := range found {
		live, err := p.offerStageChange(ctx, account)
		if err != nil {
			return standing, err
		}
		if live {
			standing++
		}
	}
	return standing, nil
}

// readContradictions lists the accounts holding an open contract_ended signal
// while still filed under a stage that reads as live.
//
// It is the same comparison the page's lifecycle_conflict card makes, and it
// is deliberately a second spelling in a second place: the card states the
// conflict to whoever is looking, this offers the fix to whoever decides. If
// they ever disagree the card is what a reader sees, so the card is the one
// with the test that pins the rule.
func readContradictions(ctx context.Context, tx pgx.Tx) ([]contradiction, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.id, o.lifecycle, s.id, s.summary
		  FROM signal s
		  JOIN organization o ON o.id = s.resolved_org_id AND o.archived_at IS NULL
		 WHERE s.kind = 'contract_ended' AND s.status = 'open' AND s.archived_at IS NULL
		   AND o.lifecycle IN ('prospect','opportunity','customer')
		 ORDER BY s.detected_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contradiction
	for rows.Next() {
		var found contradiction
		if err := rows.Scan(&found.OrganizationID, &found.Stage,
			&found.SignalID, &found.Summary); err != nil {
			return nil, err
		}
		out = append(out, found)
	}
	return out, rows.Err()
}

// offerStageChange puts one offer in the inbox, reporting whether an offer now
// stands there. A false means a human already refused this exact move on this
// account, and the answer is not to ask again.
//
// JoinPending keeps an hourly re-run from stacking the same question in the
// inbox, and StageUnlessDeclined keeps a human's "no" from being asked again
// next hour — the signal that produced it stays open, so without the durable
// memory this offer would come back every pass forever.
func (p *SignalProposer) offerStageChange(ctx context.Context, account contradiction) (bool, error) {
	identity, err := lifecycleIdentity(account.OrganizationID, lifecycleEnded)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(lifecycleProposal{
		OrganizationID: account.OrganizationID,
		CurrentStage:   account.Stage,
		ProposedStage:  lifecycleEnded,
		SignalID:       account.SignalID,
		Because:        account.Summary,
	})
	if err != nil {
		return false, fmt.Errorf("compose: encoding the lifecycle proposal: %w", err)
	}
	digest := sha256.Sum256(body)
	_, live, err := p.approvals.StageUnlessDeclined(ctx, approvals.StageInput{
		Kind:           lifecycleProposalKind,
		ProposedChange: body,
		DiffHash:       hex.EncodeToString(digest[:]),
		Identity:       identity,
		TargetType:     string(recordTypeOrganization),
		TargetID:       account.OrganizationID.UUID,
		Summary: fmt.Sprintf("Their mail says the contract ended. Move this account from %s to %s?",
			account.Stage, lifecycleEnded),
		JoinPending: true,
	})
	if err != nil {
		return false, err
	}
	return live, nil
}

// lifecycleAcceptEffect builds the approvals.ApprovedEffect for kind
// "lifecycle_change": the human agreed with what the correspondence said, so
// the stage moves and the signal that asked is acknowledged — in ONE
// transaction, because a moved account still shouting the signal that moved it
// is a page that contradicts itself.
//
// There is no reject effect. Rejecting means the reader judged the stage
// right and the reading wrong; the record is already what they want, and the
// signal stays open because they did not say the mail was wrong, only that
// the record was not.
func lifecycleAcceptEffect(svc *approvals.Service, store *people.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		var proposal lifecycleProposal
		if err := json.Unmarshal(proposedChange, &proposal); err != nil {
			return fmt.Errorf("compose: decoding the lifecycle proposal: %w", err)
		}
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: lifecycle accept without a deciding principal")
		}
		// The write carries the machine provenance — the stage came from the
		// account's correspondence, not from someone typing it — while the
		// human's approval is on the decision's own audit row.
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         lifecycleProposalActor,
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		return svc.RedeemAndApply(ctx, approvalID, lifecycleProposalKind, diffHash, func(tx pgx.Tx) error {
			// A false here is the stage having been corrected by a human while
			// the offer waited: the approval is spent, nothing is written, and
			// their edit stands.
			moved, err := store.SetOrganizationLifecycleTx(execCtx, tx,
				proposal.OrganizationID, proposal.CurrentStage, proposal.ProposedStage)
			if err != nil || !moved {
				return err
			}
			// The false is a signal a human already triaged; their judgement
			// stands and the stage change is still correct.
			_, err = signals.AcknowledgeTx(execCtx, tx, proposal.SignalID)
			return err
		})
	}
}
