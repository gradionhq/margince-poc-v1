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
func RegisterIntentTools(r *Registry, retriever retrieval.Retriever, brief MeetingBriefReader) {
	if retriever == nil {
		return
	}
	r.Register(catchMeUpOn{retriever: retriever})
	r.Register(prepForMeeting{retriever: retriever, brief: brief})
}

// anchorArgs is the shared input shape: one record to build around.
type anchorArgs struct {
	RecordType string   `json:"record_type"`
	RecordID   ids.UUID `json:"record_id"`
	MaxItems   int      `json:"max_items"`
}

const anchorSchema = `{"type":"object","required":["record_type","record_id"],"properties":{
	"record_type":{"type":"string","enum":["person","organization","deal","lead","project","activity"]},
	"record_id":{"type":"string","format":"uuid"},
	"max_items":{"type":"integer","minimum":1,"maximum":20}},
	"additionalProperties":false}`

// AssembledContextJSON renders a retrieval.Context in the
// evidence-carrying wire shape both intent tools share (exported so the
// composition tests pin the exact shape the tools return).
func AssembledContextJSON(ctx context.Context, assembled retrieval.Context) (json.RawMessage, error) {
	return json.Marshal(assembledContext(ctx, assembled))
}

// assembledContext is the one place a retrieval.Context becomes tool output, so
// both intent tools report the same shape and neither can drift into its own.
//
// It sources the answer as it builds it: an assembled picture SUMMARIZES records
// rather than serving them, so nothing else on the call's path names them, and a
// summary whose records are absent from the envelope is exactly the unsourced
// element the evidence rule refuses.
func assembledContext(ctx context.Context, assembled retrieval.Context) AssembledContextResult {
	// The summaries and snippets below are record CONTENT, assembled from rows
	// the retriever read and this call never saw — so the answer is tainted with
	// them, not merely sourced to them.
	noteDerivedContent(ctx)
	noteEvidence(ctx, assembled.Anchor.Type, assembled.Anchor.ID)
	sections := make([]ContextSection, 0, len(assembled.Sections))
	for _, section := range assembled.Sections {
		items := make([]ContextItem, 0, len(section.Items))
		for _, item := range section.Items {
			evidence := make([]ContextEvidence, 0, len(item.Evidence))
			for _, ev := range item.Evidence {
				evidence = append(evidence, ContextEvidence{Source: ev.Source, Snippet: ev.Snippet})
			}
			noteEvidence(ctx, item.Ref.Type, item.Ref.ID)
			built := ContextItem{
				RecordType: item.Ref.Type, RecordID: item.Ref.ID,
				Summary: item.Summary, Evidence: evidence,
			}
			// Only for something that HAPPENED. A person has no date, and a
			// zero one would read as 0001-01-01 rather than as absent.
			if !item.OccurredAt.IsZero() {
				at := item.OccurredAt
				built.OccurredAt = &at
			}
			items = append(items, built)
		}
		sections = append(sections, ContextSection{Name: section.Name, Items: items})
	}
	return AssembledContextResult{
		Anchor:   ContextAnchor{RecordType: assembled.Anchor.Type, RecordID: assembled.Anchor.ID},
		Sections: sections,
	}
}

// --- catch_me_up_on (🟢 read) ---

type catchMeUpOn struct {
	retriever retrieval.Retriever
}

func (t catchMeUpOn) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "catch_me_up_on", Title: "Catch me up on a record", Version: toolVersionV1,
		Description:   catchMeUpOnCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "getPerson/getOrganization/getDeal + listActivities",
		InputSchema:  schema(anchorSchema),
		OutputSchema: schemaFor[AssembledContextResult](),
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
	return AssembledContextJSON(ctx, assembled)
}

// --- prep_for_meeting (🟢 read) ---

type prepForMeeting struct {
	retriever retrieval.Retriever
	// brief is the person page's own assembler. Nil is a wiring the tool
	// survives rather than refuses: an installation without it answers the
	// assembled picture, which is what this tool has always returned, instead
	// of losing a read it can still perform.
	brief MeetingBriefReader
}

func (t prepForMeeting) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "prep_for_meeting", Title: "Prepare for a meeting", Version: toolVersionV1,
		Description:   prepForMeetingCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "getPerson/getOrganization/getDeal + listActivities",
		InputSchema:  schema(anchorSchema),
		OutputSchema: schemaFor[PrepForMeetingResult](),
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

	// The prep affordance: same assembled picture, plus the open items
	// pulled forward as the meeting's focus list.
	var focus []retrieval.Item
	for _, section := range assembled.Sections {
		if section.Name == "open_tasks" {
			focus = append(focus, section.Items...)
		}
	}
	focusItems := make([]MeetingFocusItem, 0, len(focus))
	for _, item := range focus {
		focusItems = append(focusItems, MeetingFocusItem{RecordID: item.Ref.ID, Summary: item.Summary})
	}
	result := PrepForMeetingResult{
		Briefing: assembledContext(ctx, assembled), MeetingFocus: focusItems,
	}
	if written, ok := t.writtenBrief(ctx, args); ok {
		result.Brief = &written
		result.MeetingFocus = briefCommitments(written, focusItems)
	}
	return json.Marshal(result)
}

// writtenBrief is the eight-section brief, when this anchor has one.
//
// Only an ACTIVITY anchor can: the other three name a record, not a room, and
// there is no brief to assemble for them. A refusal is not one either — the
// reader assembles under the caller's own scope, so a meeting they may not
// read answers not-found, and the assembled picture they CAN have still
// stands rather than the whole call failing on the richer half.
func (t prepForMeeting) writtenBrief(ctx context.Context, args anchorArgs) (MeetingBriefResult, bool) {
	if t.brief == nil || args.RecordType != string(datasource.EntityActivity) {
		return MeetingBriefResult{}, false
	}
	written, err := t.brief(ctx, args.RecordID)
	if err != nil {
		return MeetingBriefResult{}, false
	}
	return written, true
}

// briefCommitments re-sources the focus list from the brief's own commitments
// section when there is one.
//
// The tool's copy promises the focus list names "what to act on after the
// meeting", and the prose above it now comes from the brief. Leaving the list
// keyed on the context walk's open_tasks would let the two halves of one
// answer disagree about what is outstanding. It falls back to the walk when
// the brief has no commitments to name, because an empty list would report
// nothing outstanding rather than nothing written.
func briefCommitments(written MeetingBriefResult, fallback []MeetingFocusItem) []MeetingFocusItem {
	for _, section := range written.Sections {
		if section.Kind != "commitments" || len(section.Sentences) == 0 {
			continue
		}
		out := make([]MeetingFocusItem, 0, len(section.Sentences))
		for _, sentence := range section.Sentences {
			for _, cited := range sentence.Evidence {
				out = append(out, MeetingFocusItem{RecordID: cited.RecordID, Summary: sentence.Text})
				break
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return fallback
}
