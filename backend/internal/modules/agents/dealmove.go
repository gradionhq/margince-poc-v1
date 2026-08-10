// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the tier gate is shown for a deal move, and what it decides from it.
//
// This is its own file because it is its own concept and it has more than one
// door: the two dynamic MCP tools reach it through their ResolverInput, and the
// REST admission path (ADR-0055 — a passport is a REST Bearer credential,
// governed exactly like MCP) reaches it from compose. Living in tools.go beside
// the CRUD verbs is how it came to be read as "advance_deal's helper" and fixed
// on one door only.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// stageSemanticOpen is the one stage semantic that does not close a deal. Named
// because the tier gate is the only place it decides anything, and a typo there
// would silently open the gate rather than close it.
const stageSemanticOpen = "open"

// advanceDealTier is the invocation-time exception (A34/ADR-0026): a move
// between OPEN stages is 🟢, and a move with a terminal stage at EITHER end is
// 🟡 — money and irreversibility.
//
// Either end, because the risk is not a property of the destination. Closing a
// deal is the obvious half; reopening one is the other, and it was ungated. A
// won deal moved back to Proposal clears its close date, its lost reason and
// the FX rate frozen at close, and takes revenue out of a quarter that has
// already been reported — the same money and the same irreversibility, reached
// from the far side.
//
// The resolver may only ever RAISE: it auto-executes exactly when it can prove
// BOTH endpoints open, so an unknown, absent or malformed semantic at either
// end fails toward the approval gate rather than away from it.
func advanceDealTier(in mcp.TierResolverInput) mcp.RiskTier {
	if in.SourceStageSemantic == stageSemanticOpen && in.TargetStageSemantic == stageSemanticOpen {
		return mcp.TierAutoExecute
	}
	return mcp.TierConfirmationRequired
}

// DealMoveTierInput is what the tier gate is shown for a deal move: both
// endpoints of the move, resolved to their semantics, plus the pipeline the
// target belongs to.
//
// One builder for every door, because they all share one resolver. The two
// dynamic tools reach it from the MCP registry; the REST admission path
// (ADR-0055 — a passport is a REST Bearer credential, governed exactly like
// MCP) reaches it from compose. Each copy of this would be a place the gate
// could be fed differently, and the difference that matters is the one that
// stops reading the SOURCE: that is the reopen hole, and it was open on
// whichever door had not been edited.
func DealMoveTierInput(
	ctx context.Context, p datasource.SystemOfRecordProvider, stages StageResolver,
	dealID, toStageID ids.UUID, args json.RawMessage,
) (mcp.TierResolverInput, error) {
	target, pipelineID, err := stages.StageSemantic(ctx, toStageID)
	if err != nil {
		return mcp.TierResolverInput{}, err
	}
	source, observed, err := dealStageSemantic(ctx, p, stages, dealID)
	if err != nil {
		return mcp.TierResolverInput{}, err
	}
	in := mcp.TierResolverInput{
		Args:                args,
		SourceStageSemantic: source,
		TargetStageSemantic: target,
		PipelineID:          pipelineID.String(),
	}
	// The version of the deal the SOURCE semantic was read from, which is the
	// whole of what this gate proved about the record. It travels so the write
	// the gate admits can be conditioned on it: the read commits first, and an
	// agent can close the deal in between with its own call. One read, one
	// moment — the rule dealMoveSummary states for the approval sentence,
	// applied to the auto-executed move.
	//
	// A record with NO version is left unreported rather than pinned at zero. A
	// mirror row answers zero because the mirror keeps no version of its own,
	// and zero is not a version any write can be conditioned on — the REST door
	// would send `If-Match: 0`, which the contract's own parser rejects as a
	// malformed header the caller never sent. Unreported means the gate raises
	// to confirm-first, which is the honest answer: this server could not
	// establish the record's state, so a human decides.
	if observed > 0 {
		in.ObservedVersion = &observed
	}
	return in, nil
}

// dealStageSemantic reads the semantic of the stage a deal is currently in, and
// the version of the deal it read it from.
//
// It goes through the same StageResolver the target does, so a renamed "Won"
// column is judged by its semantic at both ends rather than by its label at one.
// A deal whose stage cannot be read is an ERROR rather than an empty semantic:
// the resolver would treat empty as not-open and raise, which is safe, but the
// caller deserves the real reason instead of an approval request for a deal this
// server could not read.
//
// The version comes back from the SAME record as the semantic. Reading it again
// would be a second moment, and a tier decided at one moment and pinned at
// another binds the write to a state nothing judged.
func dealStageSemantic(
	ctx context.Context, p datasource.SystemOfRecordProvider, stages StageResolver, dealID ids.UUID,
) (semantic string, version int64, err error) {
	rec, err := p.Read(ctx, datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID})
	if err != nil {
		return "", 0, err
	}
	var fields struct {
		StageID ids.UUID `json:"stage_id"`
	}
	if err := json.Unmarshal(rec.Fields, &fields); err != nil {
		return "", 0, fmt.Errorf("crmagents: deal %s read back without a readable stage: %w", dealID, err)
	}
	// An ABSENT stage_id decodes without error and leaves the zero id, which a
	// resolver would answer with an empty semantic — a raise, so safe, but a
	// silent one. The gate would ask a human to approve a move against a deal
	// whose current stage this server never established, and the human would
	// have no way to know that is the question.
	if fields.StageID.IsZero() {
		return "", 0, fmt.Errorf("crmagents: deal %s read back with no stage to resolve", dealID)
	}
	semantic, _, err = stages.StageSemantic(ctx, fields.StageID)
	if err != nil {
		return "", 0, err
	}
	return semantic, rec.Version, nil
}

// dealMoveSummary is the sentence a human is asked to approve. It names the move
// by BOTH endpoints, because the ones that reach this gate are opposites:
// closing a deal, reopening one, and rewriting one outcome as another.
//
// "Close deal Acme as open" is what a reopen used to read as — a sentence that
// describes neither what is happening nor what it costs. A human approving from
// an inbox has the summary and nothing else, so a summary that names the wrong
// act is the whole decision going wrong.
//
// It reads the source out of the record STAGING ALREADY READ rather than
// fetching the deal again. Two reads are two moments, and the summary would
// then be free to describe a transition the staged version never had — a move
// labelled REOPEN against a version that is already open. One read, one moment,
// and the version pinned to the approval is the one the sentence describes.
//
// A stage it cannot resolve degrades to naming the destination rather than
// failing: the approval is already the safe answer, and refusing to describe it
// would turn a readable-enough decision into no decision at all.
func dealMoveSummary(ctx context.Context, stages StageResolver, rec datasource.Record, target string) string {
	label := recordLabel(rec)
	var fields struct {
		StageID ids.UUID `json:"stage_id"`
	}
	if err := json.Unmarshal(rec.Fields, &fields); err != nil || fields.StageID.IsZero() {
		return fmt.Sprintf("Move deal %s to %s", label, target)
	}
	source, _, err := stages.StageSemantic(ctx, fields.StageID)
	switch {
	case err != nil || source == "":
		return fmt.Sprintf("Move deal %s to %s", label, target)
	case source == stageSemanticOpen && target == stageSemanticOpen:
		// Reachable even though the tier gate calls this transition 🟢: a
		// per-field precedence split stages an otherwise auto-execute call, and
		// the human then reads this line about a routine move.
		return fmt.Sprintf("Move deal %s to another open stage", label)
	case source != stageSemanticOpen && target == stageSemanticOpen:
		return fmt.Sprintf("REOPEN deal %s, which is currently %s — this clears its close date, "+
			"its lost reason and the exchange rate frozen when it closed", label, source)
	case source != stageSemanticOpen:
		return fmt.Sprintf("Change deal %s from %s to %s", label, source, target)
	}
	return fmt.Sprintf("Close deal %s as %s", label, target)
}
