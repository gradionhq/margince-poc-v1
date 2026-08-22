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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// RegisterLifecycleTools wires the transitions over the seams the
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
	r.Register(relinkActivity{relinker: relinker, p: p})
	r.Register(relinkThread{relinker: relinker, p: p})
	r.Register(relinkActivities{relinker: relinker, p: p})
	r.Register(disqualifyLead{p: p, disqualifier: disqualifier})
	r.Register(advanceProjectPhase{p: p, advancer: advancer})
}

// ActivityRelinker moves an activity's typed link onto a record, idempotently
// on (activity, entity_type, entity_id).
type ActivityRelinker interface {
	RelinkActivity(ctx context.Context, activityID ids.UUID, entityType string, entityID ids.UUID, replaceExistingOfType bool) (json.RawMessage, error)
	// RelinkThread is the same move over every writable member of one
	// conversation; RelinkActivities over a named set. Both answer the
	// count-and-ids shape rather than a record.
	RelinkThread(ctx context.Context, threadKey string, entityType string, entityID ids.UUID, replaceExistingOfType bool) (json.RawMessage, error)
	RelinkActivities(ctx context.Context, activityIDs []ids.UUID, entityType string, entityID ids.UUID, replaceExistingOfType bool) (json.RawMessage, error)
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
	string(datasource.EntityPerson):       true,
	string(datasource.EntityOrganization): true,
	string(datasource.EntityDeal):         true,
	string(datasource.EntityLead):         true,
	string(datasource.EntityProject):      true,
}

type relinkActivityArgs struct {
	ActivityID            ids.UUID `json:"activity_id"`
	EntityType            string   `json:"entity_type"`
	EntityID              ids.UUID `json:"entity_id"`
	ReplaceExistingOfType bool     `json:"replace_existing_of_type"`
}

type relinkActivity struct {
	relinker ActivityRelinker
	// p resolves the activity a staged relink binds to. Needed only since the
	// tier became dynamic: a project destination resolves 🟡, and a 🟡 call
	// that cannot describe its subject is refused with no card minted, so the
	// human the raise asks for is never asked.
	p datasource.SystemOfRecordProvider
}

func (t relinkActivity) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "relink_activity", Title: "Re-associate an activity to a record", Version: toolVersionV1,
		Description: relinkActivityCopy.render(),
		// Dynamic because filing under a PROJECT classifies the activity as
		// commercial correspondence — write-once and monotonic — while every
		// other destination is an association a member can undo. See
		// relinkActivityTier.
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierDynamic,
		TierResolver: relinkActivityTier,
		OpenAPIOp:    "relinkActivity",
		InputSchema: schema(`{"type":"object","required":["activity_id","entity_type","entity_id"],"properties":{
			"activity_id":{"type":"string","format":"uuid","description":"The captured activity to re-associate"},
			"entity_type":{"type":"string","enum":["person","organization","deal","lead","project"]},
			"entity_id":{"type":"string","format":"uuid","description":"The record to link it to"},
			"replace_existing_of_type":{"type":"boolean","default":false,
				"description":"Replace the existing link of the same entity_type (move) rather than adding one (associate)"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PassthroughEntityResult](),
	}
}

// StageInfo decodes this door's arguments into the relink command and
// delegates, so the refusals and the staged subject come from the resolver the
// REST door reaches for the same operation (commandauto.go).
//
// Without it a project relink resolves 🟡 and then dies: Registry.stageRefusedCall
// returns the bare refusal for a tool that is not stageable, so no approval row
// is written and the decision grant this operation now carries would never have
// a card to govern.
func (t relinkActivity) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args relinkActivityArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewRelinkActivityCall(t.p, RelinkActivityCommand{
		ActivityID: args.ActivityID, EntityType: args.EntityType, EntityID: args.EntityID,
	}))
}

func (t relinkActivity) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args relinkActivityArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if err := requireLinkTarget(args.EntityType); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityActivity, args.ActivityID)
	noteEvidence(ctx, datasource.EntityType(args.EntityType), args.EntityID)
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
		Description:   disqualifyLeadCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "disqualifyLead",
		InputSchema: schema(`{"type":"object","required":["lead_id"],"properties":{
			"lead_id":{"type":"string","format":"uuid","description":"The lead to disqualify"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after approval"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PassthroughEntityResult](),
	}
}

// StageInfo decodes this door's arguments into the retirement command and
// delegates: the refusals and the staged subject live in the resolver
// (commandlifecycle.go), where the REST door reaches the same ones for the
// same operation.
func (t disqualifyLead) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args disqualifyLeadArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewDisqualifyLeadCall(t.p, DisqualifyLeadCommand(args)))
}

func (t disqualifyLead) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args disqualifyLeadArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityLead, args.LeadID)
	return t.disqualifier.DisqualifyLead(ctx, args.LeadID)
}

// --- advance_project_phase (🟡 write) ---

// projectPhases mirrors the contract's phase ladder. Movement along it is
// free-form in both directions — a closed project may re-open — so this checks
// only that the phase named exists.
var projectPhases = map[string]bool{
	"initiative": true, "pursuing": true, "delivering": true, projectPhaseClosed: true,
}

// projectPhaseClosed is the one phase with a rule attached: the contract
// requires a reason to close.
const projectPhaseClosed = "closed"

type advanceProjectPhaseArgs struct {
	ProjectID ids.UUID `json:"project_id"`
	ToPhase   string   `json:"to_phase"`
	Reason    *string  `json:"reason"`
	IfVersion *int64   `json:"if_version"`
}

type advanceProjectPhase struct {
	p        datasource.SystemOfRecordProvider
	advancer ProjectPhaseAdvancer
}

func (t advanceProjectPhase) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "advance_project_phase", Title: "Move a project to a phase", Version: toolVersionV1,
		Description:   advanceProjectPhaseCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "advanceProjectPhase",
		InputSchema: schema(`{"type":"object","required":["project_id","to_phase"],"properties":{
			"project_id":{"type":"string","format":"uuid"},
			"to_phase":{"type":"string","enum":["initiative","pursuing","delivering","closed"]},
			"reason":{"type":"string","description":"Required when to_phase is closed; recorded on the phase-history row either way"},
			"if_version":{"type":"integer","description":"The version the caller read; the write is refused as skew if the project moved since"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after approval"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PassthroughEntityResult](),
	}
}

// StageInfo decodes this door's arguments into the phase-transition command
// and delegates: the refusals — the phase vocabulary, the reason a close
// requires, and the project itself — and the staged subject live in the
// resolver (commandlifecycle.go), where the REST door reaches the same ones
// for the same operation. It decodes without admitting, because Guards admits:
// StageSubject runs the refusals before the subject, so a phase this door
// would have rejected still never reaches a human's inbox.
func (t advanceProjectPhase) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	var args advanceProjectPhaseArgs
	if err := decodeArgs(in, &args); err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewAdvanceProjectPhaseCall(t.p, AdvanceProjectPhaseCommand{
		ProjectID: args.ProjectID,
		ToPhase:   args.ToPhase,
		Reason:    args.Reason,
	}))
}

func (t advanceProjectPhase) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	args, err := t.readArgs(in)
	if err != nil {
		return nil, err
	}
	// The caller's version pins the write, or — on the redeemed retry of an
	// approved call that named none — the version the human approved
	// (pinForWrite). A version this tool read microseconds earlier would be
	// compared against itself and could never detect skew: a pin in name only.
	pin, err := pinForWrite(ctx, args.IfVersion)
	if err != nil {
		return nil, err
	}
	noteEvidence(ctx, datasource.EntityProject, args.ProjectID)
	return t.advancer.AdvanceProjectPhase(ctx, args.ProjectID, args.ToPhase, args.Reason, pin)
}

// readArgs decodes and admits the transition for the EXECUTION path, through
// the resolver's own requireProjectPhase (commandlifecycle.go) rather than a
// second copy of it. Closing without a reason is refused by the contract, and
// refusing it at both doors is what keeps a human from spending an approval on
// a rule the agent could have been told before anyone was asked.
func (t advanceProjectPhase) readArgs(in json.RawMessage) (advanceProjectPhaseArgs, error) {
	var args advanceProjectPhaseArgs
	if err := decodeArgs(in, &args); err != nil {
		return advanceProjectPhaseArgs{}, err
	}
	if err := requireProjectPhase(args.ToPhase, args.Reason); err != nil {
		return advanceProjectPhaseArgs{}, err
	}
	return args, nil
}
