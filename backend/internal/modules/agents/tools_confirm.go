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
	"slices"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// --- archive_record (🟡 write — visibility change, hard to undo) ---

// archivableRecordTypes is what SystemOfRecordProvider.Archive actually routes
// (compose/provider.go) — narrower than datasource.EntityTypes(), which is the
// whole seam vocabulary and includes `lead`, a type Archive does not serve.
//
// Stage-time truth has to equal execute-time truth here: this tool stages a
// confirmation FIRST and archives on the approved retry, so a type admitted at
// staging and refused at execution spends a human's approval on a call that
// could never run. That is what happened to `activity` — the contract declared
// archiveActivity as this tool's, the seam had no branch for it, and the
// refusal arrived only after a human had said yes. The list is stated here
// rather than derived so adding a branch to Archive is a deliberate edit in
// both places.
var archivableRecordTypes = []string{
	string(datasource.EntityPerson), string(datasource.EntityOrganization),
	string(datasource.EntityDeal), string(datasource.EntityProject),
	string(datasource.EntityRelationship), string(datasource.EntityActivity),
}

func archivableByTheRecordSeam(recordType string) bool {
	return slices.Contains(archivableRecordTypes, recordType)
}

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
		OpenAPIOp: "archivePerson/archiveOrganization/archiveDeal/archiveProject/archiveRelationship/archiveActivity",
		InputSchema: schema(`{"type":"object","required":["record_type","id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","project","relationship","activity"]},
			"id":{"type":"string","format":"uuid"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after approval"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ArchiveResult](),
	}
}

// StageInfo decodes this door's arguments into the archive command and
// delegates: the refusals and the staged subject live in the resolver
// (command.go), where the REST door reaches the same ones for the same
// operation.
//
// This door's wire shape IS the command's field set — the arguments differ
// only in carrying JSON tags — so it converts rather than restating the fields,
// and a command that grows one fails to compile here instead of quietly
// leaving it unset.
//
// ONE check runs HERE, before the command is built, for exactly the reason
// createRecord.StageInfo's does (tools.go): a record type this verb's OWN
// write path cannot express. Handle archives exclusively through
// datasource.SystemOfRecordProvider.Archive — and the surface does not enforce
// the InputSchema enum at this layer, so a raw tool call can put any string
// here. Without it, `record_type:"tag"` stages an approval a human releases
// onto a retry that dies at the provider, and an arbitrary string stages one
// with a target type the approvals surface has no visibility rule for at all:
// a zombie authority object, minted at the caller's choosing.
//
// It asks archivableRecordTypes rather than the seam's whole vocabulary,
// because those two are not the same question: `lead` is a seam entity that
// Archive does not route, so the broader check let it stage and fail after
// approval — the trap `activity` fell into until Archive learned to serve it.
//
// That is a fact about THIS door's executor rather than about the operation,
// which is why it cannot live in the resolver: the same command reaching it
// from REST names an archive whose OWN module's handler performs it fine, and
// archiveResolver.Guards stands down for exactly that reason.
func (t archiveRecord) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args archiveArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	if !archivableByTheRecordSeam(args.RecordType) {
		return StageInfo{}, &BadArgsError{Cause: fmt.Errorf(
			"this verb does not archive %q records, so no approval of it could ever be carried out; it archives %s",
			args.RecordType, strings.Join(archivableRecordTypes, ", "))}
	}
	return StageSubject(ctx, NewArchiveCall(t.p, ArchiveCommand(args)))
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
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after approval"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PromoteLeadResult](),
	}
}

// StageInfo decodes this door's arguments into the promotion command and
// delegates: the refusals and the staged subject live in the resolver
// (commandlifecycle.go), where the REST door reaches the same ones for the
// same operation.
func (t promoteLead) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args promoteArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewPromoteLeadCall(t.p, PromoteLeadCommand{
		LeadID:  args.LeadID,
		Trigger: args.Trigger,
	}))
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
	if err := requireGenuineTrigger(args.Trigger); err != nil {
		return nil, err
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
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after approval"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[MergeRecordsResult](),
	}
}

// StageInfo decodes this door's arguments into the merge command and
// delegates: the refusals and the staged subject live in the resolver
// (commandrecord.go), where the REST door reaches the same ones for the same
// operation.
//
// This door's wire shape IS the command's field set — the arguments differ
// only in carrying JSON tags — so it converts rather than restating the
// fields, and a command that grows one fails to compile here instead of
// quietly leaving it unset.
func (t mergeRecords) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args mergeArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewMergeCall(t.p, MergeCommand(args)))
}

func (t mergeRecords) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args mergeArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	// The resolver's own refusal (commandrecord.go), predicate and sentence
	// both: the approved retry re-enters here without passing Guards, so a type
	// the staging refused must be refused again, and read the same way when it
	// is.
	if err := requireMergeableType(args.RecordType); err != nil {
		return nil, err
	}
	ref, err := t.p.Merge(ctx, datasource.MergeInput{
		Type: datasource.EntityType(args.RecordType), SourceID: args.SourceID, TargetID: args.TargetID,
	})
	if err != nil {
		return nil, err
	}
	// BOTH records, not only the one that survived: the source is what was
	// folded in, and an evidence list naming only the survivor describes half of
	// what happened.
	noteEvidence(ctx, ref.Type, ref.ID)
	noteEvidence(ctx, datasource.EntityType(args.RecordType), args.SourceID)
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
		Subject     string `json:"subject"`
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
	// An activity's subject is the WHICH the kind cannot supply — "Archive
	// activity 0195c3…" names nothing a human can weigh, while `Archive activity
	// "Kickoff Migration Shopsystem"` is the decision they are being asked for.
	// Also scoped to the one type, and to the field that identifies rather than
	// classifies: an activity carrying no subject still falls through to the id.
	if rec.Ref.Type == datasource.EntityActivity && f.Subject != "" {
		return fmt.Sprintf("%q", f.Subject)
	}
	for _, s := range []string{f.FullName, f.DisplayName, f.Name, f.Email} {
		if s != "" {
			return fmt.Sprintf("%q", s)
		}
	}
	return rec.Ref.ID.String()
}
