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
	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	// ProjectID narrows the picture to one body of work. It is a co-filter on
	// the anchor, not an anchor: material filed under another project drops
	// out, material filed under none stays (retrieval.AssembleOptions).
	ProjectID *ids.UUID `json:"project_id"`
}

const anchorSchema = `{"type":"object","required":["record_type","record_id"],"properties":{
	"record_type":{"type":"string","enum":["person","organization","deal","lead","project","activity"]},
	"record_id":{"type":"string","format":"uuid"},
	"max_items":{"type":"integer","minimum":1,"maximum":20},
	"project_id":{"type":"string","format":"uuid","description":"Keep only what is filed under this project or under none"}},
	"additionalProperties":false}`

// assembleOptions carries the caller's narrowing to the retriever.
func (a anchorArgs) assembleOptions() retrieval.AssembleOptions {
	opts := retrieval.AssembleOptions{MaxItems: a.MaxItems}
	if a.ProjectID != nil {
		opts.ProjectID = a.ProjectID.String()
	}
	return opts
}

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
		args.assembleOptions())
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
		OpenAPIOp:    "getMeetingBrief | getPerson/getOrganization/getDeal + listActivities",
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
		args.assembleOptions())
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
	// The focus list keeps naming open TASKS from the walk. The brief's
	// commitments cite the conversation a promise was extracted from, not the
	// promise itself, so re-sourcing the list from them would publish a
	// message id under a field whose contract says "what to act on" — and two
	// promises made in one email would collide on it.
	written, hasBrief, err := t.writtenBrief(ctx, args)
	if err != nil {
		return nil, err
	}
	if hasBrief {
		noteBriefEvidence(ctx, written)
		result.Brief = &written
	}
	return json.Marshal(result)
}

// noteBriefEvidence charges the brief's own citations against the read bound
// and puts them in the envelope.
//
// Naming a record to an agent is handing that record over, which is why
// noteEvidence charges rather than only recording. The brief names attendees,
// the meetings this room held before, and the conversations promises were
// extracted from — records the context walk beside it never touched. Left
// uncharged, the richest read on this surface would also be the cheapest, and
// its sources would be absent from the envelope that is supposed to say where
// an answer came from.
func noteBriefEvidence(ctx context.Context, written MeetingBriefResult) {
	noteEvidence(ctx, datasource.EntityActivity, written.ActivityID)
	for _, section := range written.Sections {
		for _, sentence := range section.Sentences {
			for _, cited := range sentence.Evidence {
				noteEvidence(ctx, datasource.EntityType(cited.RecordType), cited.RecordID)
			}
		}
	}
}

// writtenBrief is the eight-section brief, when this anchor has one.
//
// Only an ACTIVITY anchor can have one: the other three name a record rather
// than a room. A nil reader is a legal wiring — a role composed without the
// seam answers the assembled picture, which is what this tool always answered.
//
// NOT-FOUND is the one error that falls back rather than failing. It is what
// the service returns for an activity that is not a booked meeting, and for a
// meeting outside this caller's own scope: both are "there is no brief for you
// here", and the assembled picture the caller CAN have still stands. Every
// other error is returned — a permission failure or a database fault reported
// as a brief-less answer would look exactly like an ordinary meeting-less
// record, and the caller would act on a picture it was never told was partial.
func (t prepForMeeting) writtenBrief(ctx context.Context, args anchorArgs) (MeetingBriefResult, bool, error) {
	if t.brief == nil || args.RecordType != string(datasource.EntityActivity) {
		return MeetingBriefResult{}, false, nil
	}
	written, err := t.brief(ctx, args.RecordID)
	switch {
	case errors.Is(err, apperrors.ErrNotFound):
		return MeetingBriefResult{}, false, nil
	case err != nil:
		return MeetingBriefResult{}, false, err
	}
	return written, true, nil
}
