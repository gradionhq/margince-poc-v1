// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// progress_deal (interfaces.md §2.2): the intent-level composition of
// advance_deal + log_activity — one verb for "move the deal and note
// why". It inherits advance_deal's TierDynamic resolver unchanged (🟢
// open→open, 🟡 to won/lost), because an intent composition never widens
// authority beyond the §2.1 calls it composes.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

type progressDealArgs struct {
	DealID     ids.UUID `json:"deal_id"`
	ToStageID  ids.UUID `json:"to_stage_id"`
	LostReason *string  `json:"lost_reason"`
	Note       *string  `json:"note"`
	IfVersion  *int64   `json:"if_version"`
}

type progressDeal struct {
	p      datasource.SystemOfRecordProvider
	stages StageResolver
}

func (t progressDeal) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "progress_deal", Version: "1.0.0",
		RequiredScope: principal.ScopeWrite,
		Tier:          mcp.TierDynamic,
		// The SAME resolver as advance_deal: the intent composition never
		// widens authority, so the won/lost 🟡 floor holds identically.
		TierResolver: advanceDealTier,
		OpenAPIOp:    "advanceDeal + logActivity",
		InputSchema: schema(`{"type":"object","required":["deal_id","to_stage_id"],"properties":{
			"deal_id":{"type":"string","format":"uuid"},
			"to_stage_id":{"type":"string","format":"uuid"},
			"lost_reason":{"type":"string","description":"Required when the target stage closes the deal as lost"},
			"note":{"type":"string","description":"Logged as a note on the deal's timeline after the move"},
			"if_version":{"type":"integer"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved a won/lost move"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

// ResolverInput mirrors advance_deal's: the target stage's configured
// semantic decides the tier, never the request's labels.
func (t progressDeal) ResolverInput(ctx context.Context, in json.RawMessage) (mcp.TierResolverInput, error) {
	var args progressDealArgs
	if err := decodeArgs(in, &args); err != nil {
		return mcp.TierResolverInput{}, err
	}
	semantic, pipelineID, err := t.stages.StageSemantic(ctx, args.ToStageID)
	if err != nil {
		return mcp.TierResolverInput{}, err
	}
	return mcp.TierResolverInput{Args: in, TargetStageSemantic: semantic, PipelineID: pipelineID.String()}, nil
}

// StageInfo pins the staged move to the deal's CURRENT version, exactly
// like advance_deal — the approval covers the deal as the human saw it.
func (t progressDeal) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args progressDealArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityDeal, ID: args.DealID})
	if err != nil {
		return StageInfo{}, err
	}
	semantic, _, err := t.stages.StageSemantic(ctx, args.ToStageID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: "deal", TargetID: args.DealID, TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Progress deal %s to %s", recordLabel(rec), semantic),
	}, nil
}

func (t progressDeal) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args progressDealArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if _, err := t.p.AdvanceDeal(ctx, datasource.AdvanceDealInput{
		DealID:     args.DealID,
		ToStageID:  args.ToStageID,
		LostReason: args.LostReason,
		Source:     toolSource,
		IfVersion:  args.IfVersion,
	}); err != nil {
		return nil, err
	}
	out := map[string]any{}
	if args.Note != nil && strings.TrimSpace(*args.Note) != "" {
		fields, err := json.Marshal(map[string]any{
			"kind": "note",
			"body": strings.TrimSpace(*args.Note),
			"links": []map[string]any{
				{"entity_type": "deal", "entity_id": args.DealID},
			},
		})
		if err != nil {
			return nil, err
		}
		ref, err := t.p.Create(ctx, datasource.CreateInput{
			EntityType: datasource.EntityActivity,
			Fields:     fields,
			Source:     toolSource,
		})
		if err != nil {
			return nil, fmt.Errorf("crmagents: deal advanced but logging the note failed — the move stands, retry via log_activity: %w", err)
		}
		out["note_activity_id"] = ref.ID
	}
	dealJSON, err := readBack(ctx, t.p, datasource.EntityRef{Type: datasource.EntityDeal, ID: args.DealID})
	if err != nil {
		return nil, err
	}
	out["deal"] = json.RawMessage(dealJSON)
	return json.Marshal(out)
}
