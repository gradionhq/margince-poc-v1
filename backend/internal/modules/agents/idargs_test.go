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
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
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

// idProbeDispatcher is the whole product surface behind the real dispatcher,
// with the seam probe underneath. fullRegistry passes nil seams, which is
// enough to read specs and panics the moment a handler runs; this walk runs
// handlers.
func idProbeDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterCoreTools(r, seamProbeProvider{}, seamProbeProvider{}, nil, noConflicts{})
	RegisterReportTool(r, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return nil, errSeamReached
	})
	RegisterIntentTools(r, inertRetriever{})
	RegisterSlippingTools(r,
		func(context.Context) ([]SlippingDeal, error) { return nil, errSeamReached },
		func(context.Context, SlippingDeal) (ids.UUID, string, error) { return ids.UUID{}, "", errSeamReached })
	RegisterNetworkTools(r,
		func(context.Context, ids.UUID) ([]KnownColleague, error) { return nil, errSeamReached },
		func(context.Context, ids.UUID) (DealCoverageAnswer, error) {
			return DealCoverageAnswer{}, errSeamReached
		},
		func(context.Context, ids.UUID) ([]IntroRoute, bool, error) { return nil, false, errSeamReached },
		func(context.Context) (AtRiskReport, error) { return AtRiskReport{}, errSeamReached })
	RegisterCommsTools(r, &recordingComms{}, seamProbeProvider{})
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
// (book_meeting's and log_activity's `links` carry entity_id there).
func uuidProps(t *testing.T, name string, inputSchema json.RawMessage) []string {
	t.Helper()
	var schema struct {
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
	var found []string
	for prop, def := range schema.Properties {
		if def.Format == "uuid" {
			found = append(found, prop)
			continue
		}
		for nested, nestedDef := range def.Items.Properties {
			if nestedDef.Format == "uuid" {
				found = append(found, prop+"[]."+nested)
			}
		}
	}
	return found
}

// malformedCall renders a tools/call putting a non-canonical UUID in prop and
// nothing else. Only the offending property is set: the refusal under test has
// to happen while the arguments decode, before any other validation.
func malformedCall(tool, prop string) json.RawMessage {
	const notAUUID = `"not-a-uuid"`
	args := fmt.Sprintf(`{%q:%s}`, prop, notAUUID)
	if outer, nested, isNested := strings.Cut(prop, "[]."); isNested {
		args = fmt.Sprintf(`{%q:[{%q:%s}]}`, outer, nested, notAUUID)
	}
	return json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, args))
}

func TestAMalformedIDIsRefusedAsTheCallersMistakeOnEveryTool(t *testing.T) {
	s := idProbeDispatcher(t)
	// Every scope the surface defines, so admission never stands between the
	// walk and the validation it is here to check.
	ctx := scopedAgentCtx(principal.ScopeRead, principal.ScopeWrite, principal.ScopeSend)

	probed := 0
	// registry.tools IS the universe — walking it rather than a name list is
	// what makes a newly registered tool inherit the obligation.
	for name, tool := range s.registry.tools {
		for _, prop := range uuidProps(t, name, tool.Spec().InputSchema) {
			probed++
			res := s.call(ctx, malformedCall(name, prop))
			if res["isError"] != true {
				t.Errorf("%s accepted %q as a UUID", name, prop)
				continue
			}
			if answer := errorText(t, res); strings.Contains(answer, internalFaultAdvice) {
				t.Errorf("%s reported a malformed %q as an internal fault: %q\n"+
					"An agent is the only party that can fix its own typo, and this answer "+
					"withholds the argument and offers a retry that cannot succeed. Declare the "+
					"argument as ids.UUID so decodeArgs refuses it.", name, prop, answer)
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
	// ids.UUID refuses a malformed value and zero-values an ABSENT key without
	// complaint, so "required" in a schema is a claim only requireID makes true.
	// Left unchecked the zero UUID reaches a store lookup that matches nothing
	// and answers a bare not-found naming no argument.
	s := idProbeDispatcher(t)
	ctx := scopedAgentCtx(principal.ScopeRead)
	for _, tc := range []struct{ tool, field string }{
		{"who_knows", "person_id"},
		{"account_coverage", "deal_id"},
		{"intro_path_to", "organization_id"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			res := s.call(ctx, json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":{}}`, tc.tool)))
			if res["isError"] != true {
				t.Fatalf("an absent %s was accepted", tc.field)
			}
			answer := errorText(t, res)
			if strings.Contains(answer, internalFaultAdvice) {
				t.Fatalf("an absent %s reported an internal fault: %q", tc.field, answer)
			}
			if !strings.Contains(answer, tc.field) {
				t.Errorf("the refusal does not name %s: %q", tc.field, answer)
			}
		})
	}
}
