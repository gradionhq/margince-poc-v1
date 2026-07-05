// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The 🟡 confirm-first tool family: every tool here is TierYellow, so a
// call is staged for a human decision before its Handle ever runs
// (ADR-0036). Each implements StageInfo to pin the staged call to the
// target's CURRENT version — an approval is a judgment about the record
// as the human saw it, never about whatever it became since.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// --- archive_record (🟡 write — visibility change, hard to undo) ---

type archiveArgs struct {
	RecordType string   `json:"record_type"`
	ID         ids.UUID `json:"id"`
}

type archiveRecord struct {
	p datasource.SystemOfRecordProvider
}

func (t archiveRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "archive_record", Version: "1.0.0",
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierYellow,
		OpenAPIOp: "archivePerson/archiveOrganization/archiveDeal",
		InputSchema: schema(`{"type":"object","required":["record_type","id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal"]},
			"id":{"type":"string","format":"uuid"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t archiveRecord) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args archiveArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.ID})
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: args.RecordType, TargetID: args.ID, TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Archive %s %s", args.RecordType, recordLabel(rec)),
	}, nil
}

func (t archiveRecord) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args archiveArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	ref, err := t.p.Archive(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.ID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"archived": true, "record_type": ref.Type, "id": ref.ID})
}

// --- promote_lead (🟡 write — graduates a lead into the clean core) ---

// LeadPromoter is the provider extension promotion rides (the sor seam
// has no promotion verb yet — fable feedback/17).
type LeadPromoter interface {
	PromoteLead(ctx context.Context, id ids.UUID, trigger string, evidenceNote *string) (datasource.EntityRef, bool, error)
}

type promoteArgs struct {
	LeadID       ids.UUID `json:"lead_id"`
	Trigger      string   `json:"trigger"`
	EvidenceNote *string  `json:"evidence_note"`
}

type promoteLead struct {
	p        datasource.SystemOfRecordProvider
	promoter LeadPromoter
}

func (t promoteLead) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "promote_lead", Version: "1.0.0",
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierYellow,
		OpenAPIOp: "promoteLead",
		InputSchema: schema(`{"type":"object","required":["lead_id","trigger"],"properties":{
			"lead_id":{"type":"string","format":"uuid"},
			"trigger":{"type":"string","enum":["inbound_reply","meeting_booked","meeting_held","human_qualify"],
				"description":"The genuine engagement justifying promotion; cold outreach with no reply never promotes"},
			"evidence_note":{"type":"string"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t promoteLead) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args promoteArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	if !validTriggers[args.Trigger] {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf("trigger %q is not genuine engagement", args.Trigger)}
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityLead, ID: args.LeadID})
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: "lead", TargetID: args.LeadID, TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Promote lead %s to a contact (%s)", recordLabel(rec), args.Trigger),
	}, nil
}

// validTriggers mirrors the contract enum — checked BEFORE staging so a
// forbidden trigger (cold outbound) can never even reach the inbox.
var validTriggers = map[string]bool{
	"inbound_reply": true, "meeting_booked": true, "meeting_held": true, "human_qualify": true,
}

func (t promoteLead) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args promoteArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if !validTriggers[args.Trigger] {
		return nil, &BadArgsError{Cause: fmt.Errorf("trigger %q is not genuine engagement", args.Trigger)}
	}
	ref, merged, err := t.promoter.PromoteLead(ctx, args.LeadID, args.Trigger, args.EvidenceNote)
	if err != nil {
		return nil, err
	}
	rec, err := t.p.Read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("crmagents: promotion landed but read-back failed: %w", err)
	}
	return json.Marshal(map[string]any{
		"merged": merged,
		"person": wireRecord{RecordType: string(rec.Ref.Type), ID: rec.Ref.ID, Fields: rec.Fields, Version: rec.Version},
	})
}

// --- merge_records (🟡 write — collapses two records into one) ---

type mergeArgs struct {
	RecordType string   `json:"record_type"`
	SourceID   ids.UUID `json:"source_id"`
	TargetID   ids.UUID `json:"target_id"`
}

// mergeableTypes: only person and organization have a merge verb (deals and
// leads leave through their own lifecycle).
var mergeableTypes = map[string]bool{"person": true, "organization": true}

type mergeRecords struct {
	p datasource.SystemOfRecordProvider
}

func (t mergeRecords) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "merge_records", Version: "1.0.0",
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierYellow,
		OpenAPIOp: "mergePerson/mergeOrganization",
		InputSchema: schema(`{"type":"object","required":["record_type","source_id","target_id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization"]},
			"source_id":{"type":"string","format":"uuid","description":"The record merged away (archived, redirected to the survivor)"},
			"target_id":{"type":"string","format":"uuid","description":"The surviving record everything relinks to"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t mergeRecords) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args mergeArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	if !mergeableTypes[args.RecordType] {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf("record_type %q cannot be merged", args.RecordType)}
	}
	if args.SourceID == args.TargetID {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf("source and target must differ")}
	}
	// Pin the SURVIVOR's version: the human's yes is a judgment about
	// merging into B as it is now, so if B changes before redemption the
	// approval no longer covers it (version skew, re-stage). Read the source
	// too, only to label the inbox entry.
	survivor, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.TargetID})
	if err != nil {
		return StageInfo{}, err
	}
	source, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.SourceID})
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: args.RecordType, TargetID: args.TargetID, TargetVersion: &survivor.Version,
		Summary: fmt.Sprintf("Merge %s %s into %s", args.RecordType, recordLabel(source), recordLabel(survivor)),
	}, nil
}

func (t mergeRecords) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args mergeArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if !mergeableTypes[args.RecordType] {
		return nil, &BadArgsError{Cause: fmt.Errorf("record_type %q cannot be merged", args.RecordType)}
	}
	ref, err := t.p.Merge(ctx, datasource.MergeInput{
		Type: datasource.EntityType(args.RecordType), SourceID: args.SourceID, TargetID: args.TargetID,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"merged": true, "record_type": ref.Type, "survivor_id": ref.ID,
	})
}

// recordLabel pulls a human-readable name out of a record's fields for
// inbox summaries; falls back to the id.
func recordLabel(rec datasource.Record) string {
	var f struct {
		FullName    string `json:"full_name"`
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
		Email       string `json:"email"`
	}
	//craft:ignore swallowed-errors label extraction is best-effort by design — unparseable fields fall through to the id below
	_ = json.Unmarshal(rec.Fields, &f)
	for _, s := range []string{f.FullName, f.DisplayName, f.Name, f.Email} {
		if s != "" {
			return fmt.Sprintf("%q", s)
		}
	}
	return rec.Ref.ID.String()
}
