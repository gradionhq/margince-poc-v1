// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The coldstart ACCEPT executor (features/07 §1): a human approval of a
// staged read-back now WRITES the accepted fields onto the organization
// the source URL names — the follow-on effect that closes the
// stage→approve loop. Redeem-then-execute like every 🟡 executor: the
// single-use redemption is the exactly-once claim, so a replayed or
// re-driven decision applies nothing twice.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// approvalsHandlersWithEffects wires the approvals HTTP surface with
// every registered follow-on effect — the decision path and the effects
// share one service so a released effect can redeem what it decides on.
func approvalsHandlersWithEffects(pool *pgxpool.Pool) approvals.Handlers {
	svc := approvals.NewService(pool)
	store := people.NewStore(pool)
	svc.WithEffect("coldstart", coldstartAcceptEffect(svc, store))
	svc.WithEffect("enrich", scrapeAcceptEffect(svc, store))
	return approvals.NewHandlers(svc)
}

// coldstartAcceptEffect builds the approvals.ApprovedEffect compose
// injects for kind "coldstart".
func coldstartAcceptEffect(svc *approvals.Service, store *people.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.UUID, proposedChange json.RawMessage, diffHash string) error {
		// The single-use redemption IS the idempotency claim: whoever
		// consumes the approval executes; anyone else finds it consumed.
		if err := svc.Redeem(ctx, approvalID, "coldstart", diffHash); err != nil {
			return err
		}
		sourceURL, fields, err := people.UnmarshalColdStartFields(proposedChange)
		if err != nil {
			return err
		}
		// The write executes as the coldstart executor: captured_by =
		// agent:coldstart / source = coldstart (features/07 §1 AC), on
		// behalf of the human whose approval released it — that human is
		// on the decision's own audit row, this one carries the machine
		// provenance the 360 renders as "read from your site".
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: coldstart effect without a deciding principal")
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         "agent:coldstart",
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		_, err = store.ApplyColdStartProfile(execCtx, people.ApplyColdStartProfileInput{
			SourceURL: sourceURL,
			Fields:    fields,
		})
		return err
	}
}
