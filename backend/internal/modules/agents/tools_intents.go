// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The Layer-2 intent tools (features/07 §2): named user intents over
// the retrieval seam — the assembled, provenance-stamped picture, not
// raw rows the caller re-stitches. Both are 🟢 reads; every item they
// return carries evidence, and what cannot be evidenced is absent.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// RegisterIntentTools wires the intent surface; compose passes the
// search module's Retriever. No retriever, no tools — a surface that
// cannot ground does not pretend to.
func RegisterIntentTools(r *Registry, retriever retrieval.Retriever) {
	if retriever == nil {
		return
	}
	r.Register(catchMeUpOn{retriever: retriever})
	r.Register(prepForMeeting{retriever: retriever})
}

// anchorArgs is the shared input shape: one record to build around.
type anchorArgs struct {
	RecordType string   `json:"record_type"`
	RecordID   ids.UUID `json:"record_id"`
	MaxItems   int      `json:"max_items"`
}

const anchorSchema = `{"type":"object","required":["record_type","record_id"],"properties":{
	"record_type":{"type":"string","enum":["person","organization","deal","lead"]},
	"record_id":{"type":"string","format":"uuid"},
	"max_items":{"type":"integer","minimum":1,"maximum":20}},
	"additionalProperties":false}`

// AssembledContextJSON renders a retrieval.Context in the
// evidence-carrying wire shape both intent tools share (exported so the
// composition tests pin the exact shape the tools return).
func AssembledContextJSON(assembled retrieval.Context) (json.RawMessage, error) {
	sections := make([]map[string]any, 0, len(assembled.Sections))
	for _, section := range assembled.Sections {
		items := make([]map[string]any, 0, len(section.Items))
		for _, item := range section.Items {
			evidence := make([]map[string]string, 0, len(item.Evidence))
			for _, ev := range item.Evidence {
				evidence = append(evidence, map[string]string{"source": ev.Source, "snippet": ev.Snippet})
			}
			items = append(items, map[string]any{
				"record_type": item.Ref.Type, "record_id": item.Ref.ID,
				"summary": item.Summary, "evidence": evidence,
			})
		}
		sections = append(sections, map[string]any{"name": section.Name, "items": items})
	}
	return json.Marshal(map[string]any{
		"anchor":   map[string]any{"record_type": assembled.Anchor.Type, "record_id": assembled.Anchor.ID},
		"sections": sections,
	})
}

// --- catch_me_up_on (🟢 read) ---

type catchMeUpOn struct {
	retriever retrieval.Retriever
}

func (t catchMeUpOn) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "catch_me_up_on", Version: "1.0.0",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "getPerson/getOrganization/getDeal + listActivities",
		InputSchema:  schema(anchorSchema),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t catchMeUpOn) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args anchorArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	assembled, err := t.retriever.AssembleContext(ctx,
		datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.RecordID},
		retrieval.AssembleOptions{MaxItems: args.MaxItems})
	if err != nil {
		return nil, err
	}
	return AssembledContextJSON(assembled)
}

// --- prep_for_meeting (🟢 read) ---

type prepForMeeting struct {
	retriever retrieval.Retriever
}

func (t prepForMeeting) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "prep_for_meeting", Version: "1.0.0",
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "getPerson/getOrganization/getDeal + listActivities",
		InputSchema:  schema(anchorSchema),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t prepForMeeting) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args anchorArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	assembled, err := t.retriever.AssembleContext(ctx,
		datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.RecordID},
		retrieval.AssembleOptions{MaxItems: args.MaxItems})
	if err != nil {
		return nil, err
	}
	briefing, err := AssembledContextJSON(assembled)
	if err != nil {
		return nil, err
	}
	// The prep affordance: same assembled picture, plus the open items
	// pulled forward as the meeting's focus list.
	var focus []retrieval.Item
	for _, section := range assembled.Sections {
		if section.Name == "open_tasks" {
			focus = append(focus, section.Items...)
		}
	}
	focusItems := make([]map[string]any, 0, len(focus))
	for _, item := range focus {
		focusItems = append(focusItems, map[string]any{
			"record_id": item.Ref.ID, "summary": item.Summary,
		})
	}
	return json.Marshal(map[string]any{
		"briefing":      json.RawMessage(briefing),
		"meeting_focus": focusItems,
	})
}
