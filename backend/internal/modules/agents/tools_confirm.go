// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The 🟡 confirm-first tool family: every tool here is TierConfirmationRequired, so a
// call is staged for a human decision before its Handle ever runs
// (ADR-0036). Each implements StageInfo to pin the staged call to the
// target's CURRENT version — an approval is a judgment about the record
// as the human saw it, never about whatever it became since.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
		Name: "archive_record", Title: "Archive a record", Version: toolVersionV1,
		Description:   archiveRecordCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "archivePerson/archiveOrganization/archiveDeal/archiveProject/archiveRelationship",
		InputSchema: schema(`{"type":"object","required":["record_type","id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","project","relationship"]},
			"id":{"type":"string","format":"uuid"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ArchiveResult](),
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
	if err := refuseStagingElsewhere(rec); err != nil {
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
	noteEvidence(ctx, ref.Type, ref.ID)
	return json.Marshal(ArchiveResult{Archived: true, RecordType: ref.Type, ID: ref.ID})
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
		Name: "promote_lead", Title: "Promote a lead to a person", Version: toolVersionV1,
		Description:   promoteLeadCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "promoteLead",
		InputSchema: schema(`{"type":"object","required":["lead_id","trigger"],"properties":{
			"lead_id":{"type":"string","format":"uuid"},
			"trigger":{"type":"string","enum":["inbound_reply","meeting_booked","meeting_held","human_qualify"],
				"description":"The genuine engagement justifying promotion; cold outreach with no reply never promotes"},
			"evidence_note":{"type":"string"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PromoteLeadResult](),
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
	if err := refuseStagingElsewhere(rec); err != nil {
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
	noteEvidence(ctx, datasource.EntityLead, args.LeadID)
	return json.Marshal(PromoteLeadResult{Merged: merged, Person: newWireRecord(ctx, rec)})
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

// mergeableTypeNames renders the vocabulary above for a refusal, sorted so the
// message is byte-stable across processes rather than following map order.
func mergeableTypeNames() []string {
	names := make([]string, 0, len(mergeableTypes))
	for name := range mergeableTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type mergeRecords struct {
	p datasource.SystemOfRecordProvider
}

func (t mergeRecords) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "merge_records", Title: "Merge two records", Version: toolVersionV1,
		Description:   mergeRecordsCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "mergePerson/mergeOrganization",
		InputSchema: schema(`{"type":"object","required":["record_type","source_id","target_id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization"]},
			"source_id":{"type":"string","format":"uuid","description":"The record merged away (archived, redirected to the survivor)"},
			"target_id":{"type":"string","format":"uuid","description":"The surviving record everything relinks to"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[MergeRecordsResult](),
	}
}

func (t mergeRecords) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args mergeArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	if !mergeableTypes[args.RecordType] {
		return StageInfo{}, &BadArgsError{
			Cause:    fmt.Errorf("record_type %q cannot be merged", args.RecordType),
			Guidance: "mergeable types are " + strings.Join(mergeableTypeNames(), ", "),
		}
	}
	if args.SourceID == args.TargetID {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf("source and target must differ")}
	}
	// Pin the SURVIVOR's version: the human's yes is a judgment about
	// merging into B as it is now, so if B changes before redemption the
	// approval no longer covers it (version skew, re-stage). The source is read
	// because it is the other half of the change — the merge archives and
	// relinks it — so its authority is checked too and its label goes in the
	// summary.
	survivor, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.TargetID})
	if err != nil {
		return StageInfo{}, err
	}
	source, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.SourceID})
	if err != nil {
		return StageInfo{}, err
	}
	// Every record this change touches, not just the pinned one, and named so a
	// human reading the refusal knows which half of the merge blocked it.
	if err := refuseStagingElsewhere(survivor); err != nil {
		return StageInfo{}, fmt.Errorf("the record being merged INTO: %w", err)
	}
	if err := refuseStagingElsewhere(source); err != nil {
		return StageInfo{}, fmt.Errorf("the record being merged FROM: %w", err)
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
	noteEvidence(ctx, ref.Type, ref.ID)
	return json.Marshal(MergeRecordsResult{Merged: true, RecordType: ref.Type, SurvivorID: ref.ID})
}

// recordLabel pulls a human-readable name out of a record's fields for
// inbox summaries; falls back to the id.
func recordLabel(rec datasource.Record) string {
	var f struct {
		FullName    string `json:"full_name"`
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
		Email       string `json:"email"`
		Kind        string `json:"kind"`
	}
	//craft:ignore swallowed-errors label extraction is best-effort by design — unparseable fields fall through to the id below
	_ = json.Unmarshal(rec.Fields, &f)
	// An edge has no name of any sort: its identity is its kind plus two
	// endpoints, so "Archive relationship 0195c3…" tells the approving human
	// nothing about what disappears, while "employment" at least names the class.
	//
	// Scoped to that ONE type rather than added to the ladder below. `kind` is a
	// field an activity also carries, and there the id is the better answer: a
	// staged overwrite reading `Update activity "note"` would name a class where a
	// human needs to know WHICH note, and would suppress the id that told them.
	if rec.Ref.Type == datasource.EntityRelationship && f.Kind != "" {
		return fmt.Sprintf("%q", f.Kind)
	}
	for _, s := range []string{f.FullName, f.DisplayName, f.Name, f.Email} {
		if s != "" {
			return fmt.Sprintf("%q", s)
		}
	}
	return rec.Ref.ID.String()
}
