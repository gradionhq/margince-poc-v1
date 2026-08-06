// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The canonical V1 CRUD tool set (interfaces.md §2.1), composed over the
// SystemOfRecordProvider seam so the same tools serve SoR-mode today and
// Overlay-mode unchanged (03e AC-OV-2). Record-type-generic by design:
// one read_record with a record_type argument, mapping onto the per-type
// contract operations. Writes stamp source="mcp"; captured_by is derived
// from the authenticated Principal by the store — an agent cannot forge
// provenance any more than a browser can.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// toolVersionV1 is the version every shipped tool spec carries. Named because
// it appears on every tool in the package: a bare literal repeated two dozen
// times is a value nobody can grep for when the surface finally versions.
const toolVersionV1 = "1.0.0"

// toolSource is the provenance channel every MCP write carries.
const toolSource = "mcp"

// StageResolver supplies the advance_deal tier resolver's input: the
// target stage's configured semantic (won/lost is a property of pipeline
// config, not of the request arguments).
type StageResolver interface {
	StageSemantic(ctx context.Context, stageID ids.UUID) (semantic string, pipelineID ids.UUID, err error)
}

// RegisterCoreTools wires the §2.1 CRUD set over one provider: the 🟢
// tools, `advance_deal` (🟢→🟡 dynamic), and the 🟡 confirm-first tools the
// approval loop carries — `archive_record`, `promote_lead` and
// `merge_records`. The two write-shaped §2.2 intents that compose over the
// SAME provider + stage seams — `qualify_lead` and `progress_deal` —
// register here too; the read/draft intents and the lifecycle transitions
// have their own seams (RegisterIntentTools, RegisterSlippingTools,
// RegisterLifecycleTools).
//
// Every verb the contract declares with `x-mcp-tool` is registered by one of
// those functions. A declared verb with no tool is not a gap to describe here:
// TestEveryDeclaredToolVerbIsRegistered fails the build for it.
func RegisterCoreTools(r *Registry, p datasource.SystemOfRecordProvider, stages StageResolver, promoter LeadPromoter, ownership FieldOwnership) {
	r.Register(searchRecords{p: p})
	r.Register(readRecord{p: p})
	r.Register(createRecord{p: p})
	r.Register(updateRecord{p: p, ownership: ownership, staging: r.approvals})
	r.Register(logActivity{p: p})
	r.Register(advanceDeal{p: p, stages: stages})
	r.Register(progressDeal{p: p, stages: stages})
	r.Register(qualifyLead{p: p})
	r.Register(archiveRecord{p: p})
	r.Register(promoteLead{p: p, promoter: promoter})
	r.Register(mergeRecords{p: p})
}

// FieldOwnership answers the human-edit-precedence question
// (interfaces.md §2.1): which of a patch's fields hold a value whose
// most recent write was HUMAN, with a differing proposed value. The
// audit trail is the source of truth; compose implements this over it —
// this module never reads storage directly.
type FieldOwnership interface {
	HumanOwnedConflicts(ctx context.Context, entityType string, id ids.UUID, patch json.RawMessage) ([]string, error)
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// --- search_records (🟢 read) ---

type searchRecords struct {
	p datasource.SystemOfRecordProvider
}

func (t searchRecords) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "search_records", Title: "Search records", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listPeople/listOrganizations/listDeals/listLeads/listProjects",
		InputSchema: schema(`{"type":"object","properties":{
			"q":{"type":"string","description":"Full-text query over names/titles"},
			"record_type":{"type":"string","enum":["person","organization","deal","lead","project"],"description":"Restrict to one type; omit to sweep all five"},
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
	q := datasource.SearchQuery{Text: args.Q, Limit: args.Limit, Cursor: args.Cursor}
	if args.RecordType != "" {
		q.EntityTypes = []datasource.EntityType{datasource.EntityType(args.RecordType)}
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
	// TrustTier is "external" when the record did not come from the native
	// store (datasource.Record.Freshness.Authoritative is false) — the
	// overlay mirror's T2 label (AC-OV-5). It is the same marker the REST
	// search surface emits (compose.ContractSearchResults); carrying it
	// here keeps mirror-backed content tainted end-to-end for Surface-A MCP
	// clients too, not only inside the runner's blanket untrusted-wrapping.
	// Omitted (empty) for authoritative native reads.
	TrustTier string `json:"trust_tier,omitempty"`
}

// newWireRecord is the ONE place a datasource.Record becomes MCP tool
// output — every read/search/read-back rides it, so the external trust
// taint is stamped uniformly and can never be silently dropped by one
// call site again.
func newWireRecord(rec datasource.Record) wireRecord {
	w := wireRecord{
		RecordType: string(rec.Ref.Type), ID: rec.Ref.ID, Fields: rec.Fields, Version: rec.Version,
	}
	if !rec.Freshness.Authoritative {
		w.TrustTier = "external"
	}
	return w
}

func searchResult(res datasource.SearchResult) map[string]any {
	records := make([]wireRecord, 0, len(res.Records))
	for _, r := range res.Records {
		records = append(records, newWireRecord(r))
	}
	out := map[string]any{"records": records}
	if res.NextCursor != "" {
		out["next_cursor"] = res.NextCursor
	}
	return out
}

// --- read_record (🟢 read) ---

type readRecord struct {
	p datasource.SystemOfRecordProvider
}

func (t readRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_record", Title: "Read a record", Version: toolVersionV1,
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getPerson/getOrganization/getDeal/getLead/getActivity/getProject",
		InputSchema: schema(`{"type":"object","required":["record_type","id"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","lead","activity","project"]},
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
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(args.RecordType), ID: args.ID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(newWireRecord(rec))
}

// --- create_record (🟢 write, reversible) ---

type createRecord struct {
	p datasource.SystemOfRecordProvider
}

func (t createRecord) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "create_record", Title: "Create a record", Version: toolVersionV1,
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "createPerson/createOrganization/createDeal/createLead/createProject/createRelationship",
		InputSchema: schema(`{"type":"object","required":["record_type","fields"],"properties":{
			"record_type":{"type":"string","enum":["person","organization","deal","lead","activity","project","relationship"]},
			"fields":{"type":"object","description":` + jsonString(describeRecordFields(createShapes, createRecordShapes)) + `}},
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
	if err := rejectUnknownFields(createShapes, args.RecordType, args.Fields); err != nil {
		return nil, err
	}
	ref, err := t.p.Create(ctx, datasource.CreateInput{
		EntityType: datasource.EntityType(args.RecordType),
		Fields:     args.Fields,
		Source:     toolSource,
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// --- log_activity (🟢 write) ---

type logActivity struct {
	p datasource.SystemOfRecordProvider
}

func (t logActivity) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "log_activity", Title: "Log an activity", Version: toolVersionV1,
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "logActivity",
		// The two vocabularies are SPLICED from the contract, never spelled
		// here: this tool's body IS crm.yaml's CreateActivityRequest, and both
		// hand-written copies had already drifted from it in opposite
		// directions — `kind` was missing whatsapp and telegram, so a message
		// the server stores could not be logged; `entity_type` offered a
		// `project` link the contract does not accept, so an agent doing what
		// the schema said was refused.
		InputSchema: schema(`{"type":"object","required":["kind"],"properties":{
			"kind":{"type":"string","enum":` + activityKindEnum + `},
			"subject":{"type":"string"},"body":{"type":"string"},
			"occurred_at":{"type":"string","format":"date-time"` + timestampNote + `},
			"direction":{"type":"string","enum":["inbound","outbound"]},
			"due_at":{"type":"string","format":"date-time"` + timestampNote + `},
			"links":{"type":"array","items":{"type":"object","required":["entity_type","entity_id"],"properties":{
				"entity_type":{"type":"string","enum":` + activityLinkEntityTypeEnum + `},
				"entity_id":{"type":"string","format":"uuid"}},"additionalProperties":false},
				"description":"What the activity is about. Omit it and the activity is stored unattached to any record — it will not appear on a person's, company's or deal's timeline."},
			"source_system":{"type":"string"},"source_id":{"type":"string"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t logActivity) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	// The args ARE the contract's create-activity body (minus provenance,
	// which this surface stamps); the provider re-validates strictly.
	ref, err := t.p.Create(ctx, datasource.CreateInput{
		EntityType: datasource.EntityActivity,
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
	p      datasource.SystemOfRecordProvider
	stages StageResolver
}

func (t advanceDeal) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "advance_deal", Title: "Advance a deal to a stage", Version: toolVersionV1,
		RequiredScope: principal.ScopeWrite,
		Tier:          mcp.TierDynamic,
		TierResolver:  advanceDealTier,
		OpenAPIOp:     "advanceDeal",
		InputSchema: schema(`{"type":"object","required":["deal_id","to_stage_id"],"properties":{
			"deal_id":{"type":"string","format":"uuid"},
			"to_stage_id":{"type":"string","format":"uuid"` + stageIDNote + `},
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
		return mcp.TierAutoExecute
	}
	return mcp.TierConfirmationRequired
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
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityDeal, ID: args.DealID})
	if err != nil {
		return StageInfo{}, err
	}
	if err := refuseStagingElsewhere(rec); err != nil {
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
	ref, err := t.p.AdvanceDeal(ctx, datasource.AdvanceDealInput{
		DealID:     args.DealID,
		ToStageID:  args.ToStageID,
		LostReason: args.LostReason,
		Source:     toolSource,
		IfVersion:  pinForWrite(ctx, args.IfVersion),
	})
	if err != nil {
		return nil, err
	}
	return readBack(ctx, t.p, ref)
}

// readBack answers every write with the resulting record — the agent
// needs the post-write state (server-derived fields, bumped version)
// without a second round-trip.
func readBack(ctx context.Context, p datasource.SystemOfRecordProvider, ref datasource.EntityRef) (json.RawMessage, error) {
	rec, err := p.Read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("crmagents: write landed but read-back failed: %w", err)
	}
	return json.Marshal(newWireRecord(rec))
}
