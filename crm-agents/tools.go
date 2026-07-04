package crmagents

// The canonical V1 CRUD tool set (interfaces.md §2.1), composed over the
// SystemOfRecordProvider seam so the same tools serve SoR-mode today and
// Overlay-mode unchanged (03e AC-OV-2). Record-type-generic by design:
// one read_record with a record_type argument, mapping onto the per-type
// contract operations. Writes stamp source="mcp"; captured_by is derived
// from the authenticated Principal by the store — an agent cannot forge
// provenance any more than a browser can.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/crmctx"
	"github.com/gradionhq/margince/backend/kernel/ids"
	"github.com/gradionhq/margince/backend/mcp"
	"github.com/gradionhq/margince/backend/sor"
)

// toolSource is the provenance channel every MCP write carries.
const toolSource = "mcp"

// StageResolver supplies the advance_deal tier resolver's input: the
// target stage's configured semantic (won/lost is a property of pipeline
// config, not of the request arguments).
type StageResolver interface {
	StageSemantic(ctx context.Context, stageID ids.UUID) (semantic string, pipelineID ids.UUID, err error)
}

// RegisterCoreTools wires the §2.1 CRUD set over one provider: the 🟢
// tools, `advance_deal` (🟢→🟡 dynamic), and the first two 🟡
// confirm-first tools now that the approval loop can carry them —
// `archive_record` and `promote_lead`. run_report joins when the compiled
// report engine lands; merge/disqualify/enrich/send join with their
// underlying verbs.
func RegisterCoreTools(r *Registry, p sor.SystemOfRecordProvider, stages StageResolver, promoter LeadPromoter) {
	r.Register(searchRecords{p: p})
	r.Register(readRecord{p: p})
	r.Register(createRecord{p: p})
	r.Register(updateRecord{p: p})
	r.Register(logActivity{p: p})
	r.Register(advanceDeal{p: p, stages: stages})
	r.Register(archiveRecord{p: p})
	r.Register(promoteLead{p: p, promoter: promoter})
	r.Register(mergeRecords{p: p})
}

// decodeArgs is the surface's input validation: strict JSON (unknown
// argument names are errors, not silent drops).
func decodeArgs(in json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &BadArgsError{Cause: err}
	}
	return nil
}

// BadArgsError maps to a tool-call validation failure.
type BadArgsError struct{ Cause error }

func (e *BadArgsError) Error() string { return "arguments: " + e.Cause.Error() }
func (e *BadArgsError) Unwrap() error { return e.Cause }

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// --- search_records (🟢 read) ---

type searchRecords struct{ p sor.SystemOfRecordProvider }

func (t searchRecords) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "search_records", Version: "1.0.0",
		RequiredScope: crmctx.ScopeRead, Tier: mcp.TierGreen,
		OpenAPIOp: "listPeople/listOrganizations/listDeals/listLeads",
		InputSchema: schema(`{"type":"object","properties":{
			"q":{"type":"string","description":"Full-text query over names/titles"},
			"record_type":{"type":"string","enum":["person","organization","deal","lead"],"description":"Restrict to one type; omit to sweep all four"},
			"limit":{"type":"integer","minimum":1,"maximum":50},
			"cursor":{"type":"string","description":"Keyset cursor (single record_type only)"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t searchRecords) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Q          string `json:"q"`
		RecordType string `json:"record_type"`
		Limit      int    `json:"limit"`
		Cursor     string `json:"cursor"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	q := sor.SearchQuery{Text: args.Q, Limit: args.Limit, Cursor: args.Cursor}
	if args.RecordType != "" {
		q.EntityTypes = []sor.EntityType{sor.EntityType(args.RecordType)}
	}
	res, err := t.p.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(searchResult(res))
}

type wireRecord struct {
	RecordType string          `json:"record_type"`
	ID         ids.UUID        `json:"id"`
	Fields     json.RawMessage `json:"fields"`
	Version    int64           `json:"version,omitempty"`
}

func searchResult(res sor.SearchResult) map[string]any {
	records := make([]wireRecord, 0, len(res.Records))
	for _, r := range res.Records {
		records = append(records, wireRecord{
			RecordType: string(r.Ref.Type), ID: r.Ref.ID, Fields: r.Fields, Version: r.Version,
		})
	}
	out := map[string]any{"records": records}
	if res.NextCursor != "" {
		out["next_cursor"] = res.NextCursor
	}
	return out
}

// --- read_record (🟢 read) ---

type readRecord struct{ p sor.SystemOfRecordProvider }

func (t readRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_record", Version: "1.0.0",
		RequiredScope: crmctx.ScopeRead, Tier: mcp.TierGreen,
		OpenAPIOp: "getPerson/getOrganization/getDeal/getLead/getActivity",
		InputSchema: schema(`{"type":"object","required":["record_type","id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","lead","activity"]},
			"id":{"type":"string","format":"uuid"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t readRecord) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		RecordType string   `json:"record_type"`
		ID         ids.UUID `json:"id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	rec, err := t.p.Read(ctx, sor.EntityRef{Type: sor.EntityType(args.RecordType), ID: args.ID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireRecord{
		RecordType: string(rec.Ref.Type), ID: rec.Ref.ID, Fields: rec.Fields, Version: rec.Version,
	})
}

// --- create_record (🟢 write, reversible) ---

type createRecord struct{ p sor.SystemOfRecordProvider }

func (t createRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "create_record", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierGreen,
		OpenAPIOp: "createPerson/createOrganization/createDeal/createLead",
		InputSchema: schema(`{"type":"object","required":["record_type","fields"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","lead","activity"]},
			"fields":{"type":"object","description":"The crm.yaml create-request body for the record_type (a task is record_type=activity, kind=task)"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t createRecord) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		RecordType string          `json:"record_type"`
		Fields     json.RawMessage `json:"fields"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	ref, err := t.p.Create(ctx, sor.CreateInput{
		EntityType: sor.EntityType(args.RecordType),
		Fields:     args.Fields,
		Source:     toolSource,
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// --- update_record (🟢 write, reversible) ---

type updateRecord struct{ p sor.SystemOfRecordProvider }

func (t updateRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "update_record", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierGreen,
		OpenAPIOp: "updatePerson/updateOrganization/updateDeal/updateLead",
		InputSchema: schema(`{"type":"object","required":["record_type","id","fields"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","lead"]},
			"id":{"type":"string","format":"uuid"},
			"fields":{"type":"object","description":"The crm.yaml update-request body; only sent fields change"},
			"if_version":{"type":"integer","description":"Optimistic-concurrency guard: the last-seen record version"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t updateRecord) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		RecordType string          `json:"record_type"`
		ID         ids.UUID        `json:"id"`
		Fields     json.RawMessage `json:"fields"`
		IfVersion  *int64          `json:"if_version"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	ref, err := t.p.Update(ctx, sor.UpdateInput{
		Ref:       sor.EntityRef{Type: sor.EntityType(args.RecordType), ID: args.ID},
		Patch:     args.Fields,
		Source:    toolSource,
		IfVersion: args.IfVersion,
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// --- log_activity (🟢 write) ---

type logActivity struct{ p sor.SystemOfRecordProvider }

func (t logActivity) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "log_activity", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierGreen,
		OpenAPIOp: "logActivity",
		InputSchema: schema(`{"type":"object","required":["kind"],"properties":{
			"kind":{"type":"string","enum":["note","email","call","meeting","task"]},
			"subject":{"type":"string"},"body":{"type":"string"},
			"occurred_at":{"type":"string","format":"date-time"},
			"direction":{"type":"string","enum":["inbound","outbound"]},
			"due_at":{"type":"string","format":"date-time"},
			"links":{"type":"array","items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":["person","organization","deal"]},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false}},
			"source_system":{"type":"string"},"source_id":{"type":"string"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t logActivity) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	// The args ARE the contract's create-activity body (minus provenance,
	// which this surface stamps); the provider re-validates strictly.
	ref, err := t.p.Create(ctx, sor.CreateInput{
		EntityType: sor.EntityActivity,
		Fields:     in,
		Source:     toolSource,
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// --- advance_deal (🟢→🟡 TierDynamic) ---

type advanceDealArgs struct {
	DealID     ids.UUID `json:"deal_id"`
	ToStageID  ids.UUID `json:"to_stage_id"`
	LostReason *string  `json:"lost_reason"`
	IfVersion  *int64   `json:"if_version"`
}

type advanceDeal struct {
	p      sor.SystemOfRecordProvider
	stages StageResolver
}

func (t advanceDeal) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "advance_deal", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite,
		Tier:          mcp.TierDynamic,
		TierResolver:  advanceDealTier,
		OpenAPIOp:     "advanceDeal",
		InputSchema: schema(`{"type":"object","required":["deal_id","to_stage_id"],"properties":{
			"deal_id":{"type":"string","format":"uuid"},
			"to_stage_id":{"type":"string","format":"uuid"},
			"lost_reason":{"type":"string","description":"Required when the target stage closes the deal as lost"},
			"if_version":{"type":"integer"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved a won/lost move"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

// advanceDealTier is the invocation-time exception (A34/ADR-0026): open→
// open moves are 🟢, moves onto a won/lost stage are 🟡 — money and
// irreversibility. The resolver may only ever RAISE: anything that is not
// provably an open-semantic target resolves 🟡, so an unknown or
// malformed semantic fails toward the approval gate, never away from it.
func advanceDealTier(in mcp.TierResolverInput) mcp.RiskTier {
	if in.TargetStageSemantic == "open" {
		return mcp.TierGreen
	}
	return mcp.TierYellow
}

// ResolverInput reads the target stage's semantic from pipeline config —
// a renamed "Won" column still resolves 🟡, because the semantic, not the
// label or the request, is what the gate trusts.
func (t advanceDeal) ResolverInput(ctx context.Context, in json.RawMessage) (mcp.TierResolverInput, error) {
	var args advanceDealArgs
	if err := decodeArgs(in, &args); err != nil {
		return mcp.TierResolverInput{}, err
	}
	semantic, pipelineID, err := t.stages.StageSemantic(ctx, args.ToStageID)
	if err != nil {
		return mcp.TierResolverInput{}, err
	}
	return mcp.TierResolverInput{Args: in, TargetStageSemantic: semantic, PipelineID: pipelineID.String()}, nil
}

// StageInfo pins the staged move to the deal's CURRENT version, so an
// approval given for "close this deal as it stands" cannot execute
// against a deal that changed in between.
func (t advanceDeal) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args advanceDealArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	rec, err := t.p.Read(ctx, sor.EntityRef{Type: sor.EntityDeal, ID: args.DealID})
	if err != nil {
		return StageInfo{}, err
	}
	semantic, _, err := t.stages.StageSemantic(ctx, args.ToStageID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: "deal", TargetID: args.DealID, TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Close deal %s as %s", recordLabel(rec), semantic),
	}, nil
}

func (t advanceDeal) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args advanceDealArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	ref, err := t.p.AdvanceDeal(ctx, sor.AdvanceDealInput{
		DealID:     args.DealID,
		ToStageID:  args.ToStageID,
		LostReason: args.LostReason,
		Source:     toolSource,
		IfVersion:  args.IfVersion,
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// readBack answers every write with the resulting record — the agent
// needs the post-write state (server-derived fields, bumped version)
// without a second round-trip.
func readBack(ctx context.Context, p sor.SystemOfRecordProvider, ref sor.EntityRef) (json.RawMessage, error) {
	rec, err := p.Read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("crmagents: write landed but read-back failed: %w", err)
	}
	return json.Marshal(wireRecord{
		RecordType: string(rec.Ref.Type), ID: rec.Ref.ID, Fields: rec.Fields, Version: rec.Version,
	})
}

// --- archive_record (🟡 write — visibility change, hard to undo) ---

type archiveArgs struct {
	RecordType string   `json:"record_type"`
	ID         ids.UUID `json:"id"`
}

type archiveRecord struct{ p sor.SystemOfRecordProvider }

func (t archiveRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "archive_record", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierYellow,
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
	rec, err := t.p.Read(ctx, sor.EntityRef{Type: sor.EntityType(args.RecordType), ID: args.ID})
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
	ref, err := t.p.Archive(ctx, sor.EntityRef{Type: sor.EntityType(args.RecordType), ID: args.ID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"archived": true, "record_type": ref.Type, "id": ref.ID})
}

// --- promote_lead (🟡 write — graduates a lead into the clean core) ---

// LeadPromoter is the provider extension promotion rides (the sor seam
// has no promotion verb yet — fable feedback/17).
type LeadPromoter interface {
	PromoteLead(ctx context.Context, id ids.UUID, trigger string, evidenceNote *string) (sor.EntityRef, bool, error)
}

type promoteArgs struct {
	LeadID       ids.UUID `json:"lead_id"`
	Trigger      string   `json:"trigger"`
	EvidenceNote *string  `json:"evidence_note"`
}

type promoteLead struct {
	p        sor.SystemOfRecordProvider
	promoter LeadPromoter
}

func (t promoteLead) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "promote_lead", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierYellow,
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
	rec, err := t.p.Read(ctx, sor.EntityRef{Type: sor.EntityLead, ID: args.LeadID})
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

type mergeRecords struct{ p sor.SystemOfRecordProvider }

func (t mergeRecords) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "merge_records", Version: "1.0.0",
		RequiredScope: crmctx.ScopeWrite, Tier: mcp.TierYellow,
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
	survivor, err := t.p.Read(ctx, sor.EntityRef{Type: sor.EntityType(args.RecordType), ID: args.TargetID})
	if err != nil {
		return StageInfo{}, err
	}
	source, err := t.p.Read(ctx, sor.EntityRef{Type: sor.EntityType(args.RecordType), ID: args.SourceID})
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
	ref, err := t.p.Merge(ctx, sor.MergeInput{
		Type: sor.EntityType(args.RecordType), SourceID: args.SourceID, TargetID: args.TargetID,
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
func recordLabel(rec sor.Record) string {
	var f struct {
		FullName    string `json:"full_name"`
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
		Email       string `json:"email"`
	}
	_ = json.Unmarshal(rec.Fields, &f)
	for _, s := range []string{f.FullName, f.DisplayName, f.Name, f.Email} {
		if s != "" {
			return fmt.Sprintf("%q", s)
		}
	}
	return rec.Ref.ID.String()
}
