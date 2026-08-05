// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The three record-lifecycle transitions the contract declares as tools:
// moving an activity's association, retiring a lead, and stepping a project
// along its phase ladder.
//
// Each reaches its owning module through a seam the composition layer
// implements, because a module never imports a sibling (ADR-0054). The seam is
// deliberately the module's OWN entry point rather than the SQL underneath it:
// the tool is a second transport onto one behaviour, so the version fence, the
// RBAC gate and the audit+outbox write shape are the ones the REST route
// already goes through. A tool that reimplemented any of them would be a second
// answer to the same question.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// RegisterLifecycleTools wires the three transitions over the seams the
// composition layer implements. Separate from RegisterCoreTools because these
// reach three different owning modules rather than the one provider seam the
// CRUD set shares.
func RegisterLifecycleTools(
	r *Registry,
	p datasource.SystemOfRecordProvider,
	relinker ActivityRelinker,
	disqualifier LeadDisqualifier,
	advancer ProjectPhaseAdvancer,
) {
	r.Register(relinkActivity{relinker: relinker})
	r.Register(disqualifyLead{p: p, disqualifier: disqualifier})
	r.Register(advanceProjectPhase{p: p, advancer: advancer})
}

// ActivityRelinker moves an activity's typed link onto a record, idempotently
// on (activity, entity_type, entity_id).
type ActivityRelinker interface {
	RelinkActivity(ctx context.Context, activityID ids.UUID, entityType string, entityID ids.UUID, replaceExistingOfType bool) (json.RawMessage, error)
}

// LeadDisqualifier retires a lead: status disqualified + archived_at, the row
// surviving so it stays fetchable by id.
type LeadDisqualifier interface {
	DisqualifyLead(ctx context.Context, id ids.UUID) (json.RawMessage, error)
}

// ProjectPhaseAdvancer steps a project along the phase ladder, recording the
// transition. ifVersion carries the caller's read version so a concurrent move
// is skew rather than a blind overwrite.
type ProjectPhaseAdvancer interface {
	AdvanceProjectPhase(ctx context.Context, id ids.UUID, toPhase string, reason *string, ifVersion *int64) (json.RawMessage, error)
}

// --- relink_activity (🟢 write) ---

// relinkTargets is the link-target vocabulary, mirroring the contract enum so a
// target the store would refuse is refused before it reaches the store.
var relinkTargets = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "project": true,
}

type relinkActivityArgs struct {
	ActivityID            ids.UUID `json:"activity_id"`
	EntityType            string   `json:"entity_type"`
	EntityID              ids.UUID `json:"entity_id"`
	ReplaceExistingOfType bool     `json:"replace_existing_of_type"`
}

type relinkActivity struct {
	relinker ActivityRelinker
}

func (t relinkActivity) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "relink_activity", Title: "Re-associate an activity to a record", Version: toolVersionV1,
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "relinkActivity",
		InputSchema: schema(`{"type":"object","required":["activity_id","entity_type","entity_id"],"properties":{
			"activity_id":{"type":"string","format":"uuid","description":"The captured activity to re-associate"},
			"entity_type":{"type":"string","enum":["person","organization","deal","lead","project"]},
			"entity_id":{"type":"string","format":"uuid","description":"The record to link it to"},
			"replace_existing_of_type":{"type":"boolean","default":false,
				"description":"Replace the existing link of the same entity_type (move) rather than adding one (associate)"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t relinkActivity) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args relinkActivityArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if !relinkTargets[args.EntityType] {
		return nil, &BadArgsError{Cause: fmt.Errorf("entity_type %q is not a link target", args.EntityType)}
	}
	return t.relinker.RelinkActivity(ctx, args.ActivityID, args.EntityType, args.EntityID, args.ReplaceExistingOfType)
}

// --- disqualify_lead (🟡 write) ---

type disqualifyLeadArgs struct {
	LeadID ids.UUID `json:"lead_id"`
}

type disqualifyLead struct {
	p            datasource.SystemOfRecordProvider
	disqualifier LeadDisqualifier
}

func (t disqualifyLead) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "disqualify_lead", Title: "Disqualify a lead", Version: toolVersionV1,
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "disqualifyLead",
		InputSchema: schema(`{"type":"object","required":["lead_id"],"properties":{
			"lead_id":{"type":"string","format":"uuid","description":"The lead to disqualify"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t disqualifyLead) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args disqualifyLeadArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
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
		Summary: fmt.Sprintf("Disqualify lead %s", recordLabel(rec)),
	}, nil
}

func (t disqualifyLead) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args disqualifyLeadArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	return t.disqualifier.DisqualifyLead(ctx, args.LeadID)
}

// --- advance_project_phase (🟡 write) ---

// projectPhases mirrors the contract's phase ladder. Movement along it is
// free-form in both directions — a closed project may re-open — so this checks
// only that the phase named exists.
var projectPhases = map[string]bool{
	"initiative": true, "pursuing": true, "delivering": true, "closed": true,
}

type advanceProjectPhaseArgs struct {
	ProjectID ids.UUID `json:"project_id"`
	ToPhase   string   `json:"to_phase"`
	Reason    *string  `json:"reason"`
}

type advanceProjectPhase struct {
	p        datasource.SystemOfRecordProvider
	advancer ProjectPhaseAdvancer
}

func (t advanceProjectPhase) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "advance_project_phase", Title: "Move a project to a phase", Version: toolVersionV1,
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "advanceProjectPhase",
		InputSchema: schema(`{"type":"object","required":["project_id","to_phase"],"properties":{
			"project_id":{"type":"string","format":"uuid"},
			"to_phase":{"type":"string","enum":["initiative","pursuing","delivering","closed"]},
			"reason":{"type":"string","description":"Required when to_phase is closed; recorded on the phase-history row either way"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after a human approved the staged call"}},
			"additionalProperties":false}`),
		OutputSchema: schema(`{"type":"object"}`),
	}
}

func (t advanceProjectPhase) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	args, err := t.readArgs(in)
	if err != nil {
		return StageInfo{}, err
	}
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityProject, ID: args.ProjectID})
	if err != nil {
		return StageInfo{}, err
	}
	if err := refuseStagingElsewhere(rec); err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType: "project", TargetID: args.ProjectID, TargetVersion: &rec.Version,
		Summary: fmt.Sprintf("Move project %s to %s", recordLabel(rec), args.ToPhase),
	}, nil
}

func (t advanceProjectPhase) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := t.readArgs(in)
	if err != nil {
		return nil, err
	}
	// The version the phase decision was read from pins the write: a project
	// moved underneath is skew, not a transition to overwrite blindly.
	rec, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityProject, ID: args.ProjectID})
	if err != nil {
		return nil, err
	}
	return t.advancer.AdvanceProjectPhase(ctx, args.ProjectID, args.ToPhase, args.Reason, &rec.Version)
}

// readArgs decodes and admits the phase in one place, so the staging path and
// the execution path cannot disagree about what a valid transition is — a phase
// this refuses must never reach a human's inbox.
func (t advanceProjectPhase) readArgs(in json.RawMessage) (advanceProjectPhaseArgs, error) {
	var args advanceProjectPhaseArgs
	if err := decodeArgs(in, &args); err != nil {
		return advanceProjectPhaseArgs{}, err
	}
	if !projectPhases[args.ToPhase] {
		return advanceProjectPhaseArgs{}, &BadArgsError{Cause: fmt.Errorf("to_phase %q is not a project phase", args.ToPhase)}
	}
	return args, nil
}
