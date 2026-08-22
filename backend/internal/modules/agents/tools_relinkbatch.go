// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The two batch forms of relink_activity, as tools. They reach the owning
// module through the same seam the single relink uses (ActivityRelinker), so
// the per-row write check, the audit+outbox write shape and the project
// retention stamp are the store's own and never restated here.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// RelinkBatchResult is what the thread and named-set relinks answer: the count
// of rows that gained the link, and nothing else. The ids are deliberately not
// echoed — a replay of the same key, or an inbox reading the card, would hand
// back rows the reader may since have lost sight of, and a count discloses no
// row.
type RelinkBatchResult struct {
	Relinked int `json:"relinked"`
}

// --- relink_thread (dynamic write) ---

type relinkThreadArgs struct {
	ThreadKey             string   `json:"thread_key"`
	EntityType            string   `json:"entity_type"`
	EntityID              ids.UUID `json:"entity_id"`
	ReplaceExistingOfType bool     `json:"replace_existing_of_type"`
}

type relinkThread struct {
	relinker ActivityRelinker
	p        datasource.SystemOfRecordProvider
}

func (t relinkThread) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "relink_thread", Title: "Re-associate a whole conversation to a record", Version: toolVersionV1,
		Description: relinkThreadCopy.render(),
		// Dynamic for the reason relink_activity is: a PROJECT destination is
		// a write-once retention classification, here over every message in
		// the thread. relinkActivityTier reads `entity_type` off these
		// arguments exactly as it does off the single form's.
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierDynamic,
		TierResolver: relinkActivityTier,
		OpenAPIOp:    "relinkThread",
		InputSchema: schema(`{"type":"object","required":["thread_key","entity_type","entity_id"],"properties":{
			"thread_key":{"type":"string","minLength":1},
			"entity_type":{"type":"string","enum":["person","organization","deal","lead","project"]},
			"entity_id":{"type":"string","format":"uuid"},
			"replace_existing_of_type":{"type":"boolean","default":false,"description":"Move rather than associate"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[RelinkBatchResult](),
	}
}

// StageInfo decodes this door's arguments into the thread command and
// delegates, so the refusals and the staged sentence come from the resolver
// the REST door reaches for the same operation (commandrelinkbatch.go).
func (t relinkThread) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args relinkThreadArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewRelinkThreadCall(t.p, RelinkThreadCommand{
		ThreadKey: args.ThreadKey, EntityType: args.EntityType, EntityID: args.EntityID,
	}))
}

func (t relinkThread) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args relinkThreadArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if err := requireLinkTarget(args.EntityType); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityType(args.EntityType), args.EntityID)
	return t.relinker.RelinkThread(ctx, args.ThreadKey, args.EntityType, args.EntityID, args.ReplaceExistingOfType)
}

// --- relink_activities (dynamic write) ---

type relinkActivitiesArgs struct {
	ActivityIDs           []ids.UUID `json:"activity_ids"`
	EntityType            string     `json:"entity_type"`
	EntityID              ids.UUID   `json:"entity_id"`
	ReplaceExistingOfType bool       `json:"replace_existing_of_type"`
}

type relinkActivities struct {
	relinker ActivityRelinker
	p        datasource.SystemOfRecordProvider
}

func (t relinkActivities) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "relink_activities", Title: "Re-associate a set of activities to a record", Version: toolVersionV1,
		Description:   relinkActivitiesCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierDynamic,
		TierResolver: relinkActivityTier,
		OpenAPIOp:    "relinkActivities",
		InputSchema: schema(`{"type":"object","required":["activity_ids","entity_type","entity_id"],"properties":{
			"activity_ids":{"type":"array","minItems":1,"maxItems":500,"items":{"type":"string","format":"uuid"}},
			"entity_type":{"type":"string","enum":["person","organization","deal","lead","project"]},
			"entity_id":{"type":"string","format":"uuid"},
			"replace_existing_of_type":{"type":"boolean","default":false,"description":"Move rather than associate"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[RelinkBatchResult](),
	}
}

// StageInfo delegates to the resolver the REST door shares
// (commandrelinkbatch.go), for the reason relinkThread.StageInfo gives.
func (t relinkActivities) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args relinkActivitiesArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewRelinkActivitiesCall(t.p, RelinkActivitiesCommand{
		ActivityIDs: args.ActivityIDs, EntityType: args.EntityType, EntityID: args.EntityID,
	}))
}

func (t relinkActivities) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args relinkActivitiesArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if err := requireLinkTarget(args.EntityType); err != nil {
		return nil, err
	}
	for _, id := range args.ActivityIDs {
		noteEvidence(ctx, datasource.EntityActivity, id)
	}
	noteEvidence(ctx, datasource.EntityType(args.EntityType), args.EntityID)
	return t.relinker.RelinkActivities(ctx, args.ActivityIDs, args.EntityType, args.EntityID, args.ReplaceExistingOfType)
}
