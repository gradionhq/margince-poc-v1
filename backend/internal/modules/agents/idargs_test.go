// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// A malformed id is the caller's typo, and it must read as one on every tool
// that takes an id.
//
// The defect this closes: three tools declared their id argument as a `string`
// and called ids.Parse by hand, returning the parse error unwrapped. Nothing
// classifies a bare ids.Parse error, so the dispatcher's taxonomy fell to its
// last branch — internalFaultAdvice — and told the agent the tool "failed for
// an internal reason" and to retry, for a value the agent itself chose and is
// the only party that can fix. Every other tool declares the field as ids.UUID,
// where decodeArgs refuses it and names it.
//
// What is asserted is the PROSE the agent reads, through the real dispatcher,
// because that is the symptom: the walk does not care which error type a tool
// chooses, only that the answer is not the one unactionable answer on this
// surface. Both legal routes pass — refusing at the tool (BadArgsError) and
// delegating to the seam, which re-validates and returns a classified fault.
//
// The probe set is derived from each tool's OWN schema rather than a list kept
// here: every property declared format:uuid is probed, so a new tool with a new
// id argument inherits this the moment it is registered.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// errSeamReached stands for "the tool accepted these arguments and called me".
// Deliberately unclassified: reaching it renders internalFaultAdvice, so a tool
// that waves a malformed id through to a seam fails this walk instead of being
// silently credited for the seam's own validation.
var errSeamReached = fmt.Errorf("seam reached")

// seamProbeProvider stands in for the composite provider. Its write verbs run
// the SAME strict decode the three real providers run (datasource.StrictDecode
// against the contract body for the entity type), because that decode is the
// single validator for the tools that forward their whole argument object —
// log_activity has no decode step of its own, and a stub that skipped it would
// report a defect the product does not have.
type seamProbeProvider struct{}

// decodeLike mirrors a real provider's first act: assert the caller's fields
// against the contract body for this entity type.
//
//craft:ignore naked-any mirrors datasource.CreateInput.Fields, which the frozen seam declares as any
func decodeLike(shapes map[datasource.EntityType]reflect.Type, t datasource.EntityType, fields any) error {
	shape, ok := shapes[t]
	if !ok {
		return &datasource.UnsupportedEntityError{Type: string(t)}
	}
	raw, err := datasource.RawFields(fields)
	if err != nil {
		return err
	}
	return datasource.StrictDecode(raw, reflect.New(shape).Interface())
}

func (seamProbeProvider) Create(_ context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	if err := decodeLike(createShapes, in.EntityType, in.Fields); err != nil {
		return datasource.EntityRef{}, err
	}
	return datasource.EntityRef{}, errSeamReached
}

func (seamProbeProvider) Update(_ context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	if err := decodeLike(updateShapes, in.Ref.Type, in.Patch); err != nil {
		return datasource.EntityRef{}, err
	}
	return datasource.EntityRef{}, errSeamReached
}

func (seamProbeProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{}, errSeamReached
}

func (seamProbeProvider) Search(context.Context, datasource.SearchQuery) (datasource.SearchResult, error) {
	return datasource.SearchResult{}, errSeamReached
}

func (seamProbeProvider) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	return nil, errSeamReached
}

func (seamProbeProvider) ListFields(context.Context, datasource.EntityType) ([]datasource.FieldDef, error) {
	return nil, errSeamReached
}

func (seamProbeProvider) RunReport(context.Context, datasource.ReportPlan) (datasource.ReportResult, error) {
	return datasource.ReportResult{}, errSeamReached
}

func (seamProbeProvider) StageSemantic(context.Context, ids.UUID) (string, ids.UUID, error) {
	return "", ids.UUID{}, errSeamReached
}

func (seamProbeProvider) AdvanceDeal(context.Context, datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, errSeamReached
}

func (seamProbeProvider) Archive(context.Context, datasource.EntityRef) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, errSeamReached
}

func (seamProbeProvider) Merge(context.Context, datasource.MergeInput) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, errSeamReached
}

func (seamProbeProvider) PromoteLead(context.Context, ids.UUID, string, *string) (datasource.EntityRef, bool, error) {
	return datasource.EntityRef{}, false, errSeamReached
}

func (seamProbeProvider) Freshness(context.Context, datasource.EntityRef) (datasource.FreshnessInfo, error) {
	return datasource.FreshnessInfo{}, errSeamReached
}

var _ datasource.SystemOfRecordProvider = seamProbeProvider{}

// seamProbeRetriever is the retrieval seam's half of the same probe: both
// methods answer errSeamReached, so a tool that let malformed arguments through
// is distinguishable from one that refused them.
type seamProbeRetriever struct{}

func (seamProbeRetriever) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	return retrieval.Result{}, errSeamReached
}

func (seamProbeRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, errSeamReached
}

// seamProbeLifecycle answers errSeamReached from every lifecycle and enrich
// seam, so the walk can tell "the arguments got through" from "the tool refused
// them" — the whole point of the probe.
type seamProbeLifecycle struct{}

func (seamProbeLifecycle) RelinkActivity(context.Context, ids.UUID, string, ids.UUID, bool) (json.RawMessage, error) {
	return nil, errSeamReached
}

func (seamProbeLifecycle) DisqualifyLead(context.Context, ids.UUID) (json.RawMessage, error) {
	return nil, errSeamReached
}

func (seamProbeLifecycle) AdvanceProjectPhase(context.Context, ids.UUID, string, *string, *int64) (json.RawMessage, error) {
	return nil, errSeamReached
}

func (seamProbeLifecycle) EnrichCompany(context.Context, ids.UUID, string, string) (json.RawMessage, error) {
	return nil, errSeamReached
}

// idProbeDispatcher is the whole product surface behind the real dispatcher,
// with the seam probe underneath. fullRegistry passes nil seams, which is
// enough to read specs and panics the moment a handler runs; this walk runs
// handlers.
func idProbeDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterCoreTools(r, seamProbeProvider{}, seamProbeProvider{}, nil, noConflicts{})
	RegisterPipelineTool(r, func(context.Context) ([]Pipeline, error) { return nil, errSeamReached })
	RegisterReportTool(r, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return nil, errSeamReached
	}, probeReportCatalog)
	RegisterIntentTools(r, inertRetriever{})
	RegisterSlippingTools(r,
		func(context.Context) ([]SlippingDeal, error) { return nil, errSeamReached },
		func(context.Context, SlippingDeal) (ids.UUID, string, error) { return ids.UUID{}, "", errSeamReached })
	RegisterNetworkTools(r,
		func(context.Context, ids.UUID) ([]KnownColleague, bool, error) { return nil, false, errSeamReached },
		func(context.Context, ids.UUID) (DealCoverageAnswer, error) {
			return DealCoverageAnswer{}, errSeamReached
		},
		func(context.Context, ids.UUID) ([]IntroRoute, bool, error) { return nil, false, errSeamReached },
		func(context.Context) (AtRiskReport, error) { return AtRiskReport{}, errSeamReached })
	RegisterCommsTools(r, &recordingComms{}, seamProbeProvider{})
	RegisterLifecycleTools(r, seamProbeProvider{},
		seamProbeLifecycle{}, seamProbeLifecycle{}, seamProbeLifecycle{})
	RegisterEnrichTool(r, seamProbeProvider{}, seamProbeLifecycle{})
	RegisterQueryTool(r, seamProbeProvider{}, func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return QueryAnswer{}, errSeamReached
	})
	RegisterContextSearchTool(r, seamProbeProvider{}, seamProbeRetriever{})
	RegisterResolveTool(r, seamProbeProvider{}, func(context.Context, []ResolveCandidate) ([]ResolveOutcome, error) {
		return nil, errSeamReached
	})
	return NewDispatcher(r, bindAuthenticated, "margince-crm", "test").
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// errorText returns the prose an in-band tool error carries — what the agent
// actually reads back.
func errorText(t *testing.T, res map[string]any) string {
	t.Helper()
	content, ok := res["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("error result carries no content block: %#v", res)
	}
	text, ok := content[0][fieldText].(string)
	if !ok {
		t.Fatalf("error result's first block is not text: %#v", content[0])
	}
	return text
}

// uuidProps reports the argument names a tool declares as format:uuid,
// including those nested one level inside an array-of-object property
// (book_meeting's and log_activity's `links` carry entity_id there), and which
// of the top-level ones the schema marks required.
//
// A nested id is never reported as required: it is required only GIVEN its
// parent array, and an absent parent is a different (legal) call.
func uuidProps(t *testing.T, name string, inputSchema json.RawMessage) (all, required []string) {
	t.Helper()
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Format string `json:"format"`
			Items  struct {
				Properties map[string]struct {
					Format string `json:"format"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		t.Fatalf("%s: inputSchema does not parse: %v", name, err)
	}
	isRequired := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		isRequired[r] = true
	}
	for prop, def := range schema.Properties {
		if def.Format == "uuid" {
			all = append(all, prop)
			if isRequired[prop] {
				required = append(required, prop)
			}
			continue
		}
		for nested, nestedDef := range def.Items.Properties {
			if nestedDef.Format == "uuid" {
				all = append(all, prop+"[]."+nested)
			}
		}
	}
	sort.Strings(all)
	sort.Strings(required)
	return all, required
}

// malformedCall renders a tools/call putting a non-canonical UUID in prop and
// nothing else. Only the offending property is set: the refusal under test has
// to happen while the arguments decode, once the call is otherwise complete.
func malformedCall(t *testing.T, spec mcp.ToolSpec, prop string) json.RawMessage {
	t.Helper()
	const notAUUID = `"not-a-uuid"`
	// Every OTHER required argument is supplied, plausibly, by the same helper
	// the absent-id walk uses: the registry holds `required` before it holds
	// shapes — an argument that is missing has no shape to be wrong about — so a
	// call carrying only the malformed id would be refused for the arguments it
	// also omitted, and this walk would stop testing what it is named for.
	outer, nested, isNested := strings.Cut(prop, "[].")
	var args map[string]json.RawMessage
	if err := json.Unmarshal(absentIDArgs(t, spec.Name, spec.InputSchema, outer), &args); err != nil {
		t.Fatalf("building a call for %s: %v", spec.Name, err)
	}
	if isNested {
		args[outer] = json.RawMessage(fmt.Sprintf(`[{%q:%s}]`, nested, notAUUID))
	} else {
		args[prop] = json.RawMessage(notAUUID)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("building a call for %s: %v", spec.Name, err)
	}
	return json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, spec.Name, encoded))
}

func TestAMalformedIDIsRefusedAsTheCallersMistakeOnEveryTool(t *testing.T) {
	s := idProbeDispatcher(t)
	// EVERY scope the surface defines. A missing one does not shrink the walk
	// visibly — the gate refuses on authority before validation runs, and the
	// refusal is not internalFaultAdvice, so the case reads as a pass while
	// testing nothing. ScopeDraft was absent and draft_email was silently
	// uncovered; the guard below is what makes that impossible to repeat.
	ctx := scopedAgentCtx(principal.ScopeRead, principal.ScopeDraft,
		principal.ScopeWrite, principal.ScopeSend, principal.ScopeEnrich)

	probed := 0
	// registry.tools IS the universe — walking it rather than a name list is
	// what makes a newly registered tool inherit the obligation.
	for name, tool := range s.registry.tools {
		allProps, _ := uuidProps(t, name, tool.Spec().InputSchema)
		for _, prop := range allProps {
			probed++
			res := s.call(ctx, malformedCall(t, tool.Spec(), prop))
			if res["isError"] != true {
				t.Errorf("%s accepted %q as a UUID", name, prop)
				continue
			}
			answer := errorText(t, res)
			// The walk has to have REACHED validation. An authority refusal is
			// not the internal fault, so it would otherwise count as a pass
			// while the argument was never looked at.
			if strings.Contains(answer, "scope") || strings.Contains(answer, "seat") {
				t.Errorf("%s refused a malformed %q on authority, not on the argument: %q\n"+
					"This case proves nothing about validation — grant the scope in this walk's "+
					"context so the call reaches the tool.", name, prop, answer)
				continue
			}
			if strings.Contains(answer, internalFaultAdvice) {
				t.Errorf("%s reported a malformed %q as an internal fault: %q\n"+
					"An agent is the only party that can fix its own typo, and this answer "+
					"withholds the argument and offers a retry that cannot succeed. Declare the "+
					"argument as ids.UUID so decodeArgs refuses it.", name, prop, answer)
				continue
			}
			// And it must say WHICH id. `merge_records` takes source_id and
			// target_id; `advance_deal` takes deal_id and to_stage_id — a refusal
			// that quotes only the bad value leaves the agent guessing which slot
			// it sat in. Nested and approval ids are out of scope here: the
			// chokepoint skips array members (required only GIVEN their parent),
			// and approval_id is consumed by splitApproval before it is reached.
			if !strings.Contains(prop, "[].") && prop != "approval_id" &&
				!strings.Contains(answer, prop) {
				t.Errorf("%s refused a malformed %q without naming it: %q", name, prop, answer)
			}
		}
	}
	// The walk is only as good as its reach: a registry that stopped declaring
	// uuid formats would pass every assertion above by probing nothing.
	if probed == 0 {
		t.Fatal("no format:uuid argument found on any tool — the walk proved nothing")
	}
}

func TestARequiredIDMustBePresentNotZero(t *testing.T) {
	// ids.UUID refuses a MALFORMED value inside decodeArgs and zero-values an
	// ABSENT key without complaint, so "required" in a schema is a claim only a
	// check in the handler makes true. Left unchecked the zero UUID reaches a
	// store lookup that matches nothing and answers a bare not-found naming no
	// argument — which is how create_record's deal and project cases became
	// unusable, and the same shape on every other tool that takes an id.
	//
	// Derived from each tool's own `required` array, not a list kept here. A walk
	// that derives its field names but hand-lists its subjects certifies every
	// subject it never looked at.
	//
	// Probed through Registry.Invoke, which is where the enforcement lives and
	// the only path to a Handle in this package. Not through the handler: a
	// per-handler check is what thirteen tools individually forgot, and a walk
	// that probes handlers would pass again the moment a fourteenth does. Not
	// through the dispatcher either — that adds the seat/scope gate without
	// adding anything this walk is about.
	registry := idProbeDispatcher(t).registry
	// Every scope the surface defines, so admission never stands between the
	// walk and the validation it is here to check.
	ctx := scopedAgentCtx(principal.ScopeRead, principal.ScopeDraft,
		principal.ScopeWrite, principal.ScopeSend, principal.ScopeEnrich)

	probed := 0
	for name, tool := range registry.tools {
		_, required := uuidProps(t, name, tool.Spec().InputSchema)
		for _, prop := range required {
			probed++
			// Every OTHER required argument is supplied, so the only thing
			// missing is the id under test — otherwise a tool refusing on a
			// different absent field would pass while never checking the id.
			args := absentIDArgs(t, name, tool.Spec().InputSchema, prop)
			var badArgs *BadArgsError
			_, err := registry.Invoke(ctx, name, args)
			if err == nil {
				t.Errorf("%s accepted an absent %q", name, prop)
				continue
			}
			if !errors.As(err, &badArgs) {
				t.Errorf("%s answered an absent %q with %T (%v), want *BadArgsError.\n"+
					"An unguarded zero UUID reaches a lookup that matches nothing, and the caller "+
					"is told a record it never named does not exist.", name, prop, err, err)
				continue
			}
			if !strings.Contains(badArgs.Error(), prop) {
				t.Errorf("%s refused an absent %q without naming it: %q",
					name, prop, badArgs.Error())
			}
		}
	}
	if probed == 0 {
		t.Fatal("no required format:uuid argument found on any tool — the walk proved nothing")
	}
}

// absentIDArgs renders an arguments object satisfying every required argument
// EXCEPT omit, so the refusal under test is about that argument alone.
func absentIDArgs(t *testing.T, tool string, inputSchema json.RawMessage, omit string) json.RawMessage {
	t.Helper()
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type   string          `json:"type"`
			Format string          `json:"format"`
			Enum   []string        `json:"enum"`
			Items  json.RawMessage `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		t.Fatalf("%s: inputSchema does not parse: %v", tool, err)
	}
	args := map[string]any{}
	for _, name := range schema.Required {
		if name == omit {
			continue
		}
		def, declared := schema.Properties[name]
		if !declared {
			t.Fatalf("%s declares %q required but does not define it", tool, name)
		}
		switch {
		case def.Format == "uuid":
			args[name] = ids.NewV7().String()
		case len(def.Enum) > 0:
			// A closed vocabulary refuses anything else, and that refusal would
			// mask the one under test.
			args[name] = def.Enum[0]
		case def.Type == "array":
			args[name] = []any{}
		case def.Type == "object":
			args[name] = map[string]any{}
		case def.Type == "integer", def.Type == "number":
			args[name] = 1
		case def.Type == "boolean":
			args[name] = true
		default:
			args[name] = "probe"
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal probe args: %v", err)
	}
	return encoded
}
