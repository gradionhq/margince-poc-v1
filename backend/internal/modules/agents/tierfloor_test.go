// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the floor does with each answer it can get, and what the two generic
// verbs stage once it has tightened them.
//
// The composition root's gates prove the floor KNOWS every pair the contract
// tightens, and the integration lane proves the wiring reaches a real call.
// Neither reaches the branches here: a tool that names no record type, a floor
// that declares nothing for the pair, an argument object that will not decode.
// Each of those silently returns the declared tier, so a mistake in one is a
// bypass that looks exactly like a correct pass.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// confirmFirstForCreating answers a floor that tightens create_record for
// exactly one record type, so a test can say which call it means to be about.
// The verb is fixed because the floor's whole subject is the record type: a
// tightening keyed on the verb alone is what a ToolSpec.Tier already expresses.
func confirmFirstForCreating(recordType string) TierFloor {
	return func(gotTool, gotType string) (mcp.RiskTier, bool) {
		if gotTool == "create_record" && gotType == recordType {
			return mcp.TierConfirmationRequired, true
		}
		return mcp.TierAutoExecute, false
	}
}

func TestTheFloorTightensOnlyTheRecordTypeItNames(t *testing.T) {
	r := NewRegistry(nil, nil, WithTierFloor(confirmFirstForCreating("project")))
	tool := createRecord{}
	green := tool.Spec()

	tightened := r.tightened(tool, green, json.RawMessage(`{"record_type":"project","fields":{}}`))
	if tightened.Tier != mcp.TierConfirmationRequired {
		t.Errorf("a project create admitted at tier %v, want confirm-first — the floor named this "+
			"very pair", tightened.Tier)
	}
	if tightened.TierResolver != nil {
		t.Error("the tightened spec kept a tier resolver, which could still answer auto-execute " +
			"for a call the contract says a human must see")
	}

	untouched := r.tightened(tool, green, json.RawMessage(`{"record_type":"person","fields":{}}`))
	if untouched.Tier != green.Tier {
		t.Errorf("a person create admitted at tier %v, want the verb's own %v — tightening a pair "+
			"the contract never declared would stage work nobody asked to review",
			untouched.Tier, green.Tier)
	}
}

// The three answers that all look like "no change" and are not the same thing.
// A regression in any of them reopens #982 while every other test stays green.
func TestTheFloorLeavesTheDeclaredTierWhenItCannotAnswer(t *testing.T) {
	green := createRecord{}.Spec()
	project := json.RawMessage(`{"record_type":"project","fields":{}}`)

	cases := map[string]struct {
		registry *Registry
		tool     mcp.Tool
		args     json.RawMessage
	}{
		"no floor composed": {
			registry: NewRegistry(nil, nil), tool: createRecord{}, args: project,
		},
		"a tool that names no record type": {
			registry: NewRegistry(nil, nil, WithTierFloor(confirmFirstForCreating("project"))),
			tool:     logActivity{}, args: project,
		},
		"an argument object that will not decode": {
			registry: NewRegistry(nil, nil, WithTierFloor(confirmFirstForCreating("project"))),
			tool:     createRecord{}, args: json.RawMessage(`not json`),
		},
		"a pair the contract declares nothing for": {
			registry: NewRegistry(nil, nil, WithTierFloor(confirmFirstForCreating("deal"))),
			tool:     createRecord{}, args: project,
		},
		// The contract tightens createCustomField, and this verb cannot create a
		// custom field. Tightening anyway would turn the provider's immediate
		// refusal into an approval a human releases onto a call that then dies.
		"a record type the verb does not serve": {
			registry: NewRegistry(nil, nil, WithTierFloor(confirmFirstForCreating("custom_field"))),
			tool:     createRecord{}, args: json.RawMessage(`{"record_type":"custom_field","fields":{}}`),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.registry.tightened(c.tool, green, c.args).Tier; got != green.Tier {
				t.Errorf("tier = %v, want the declared %v", got, green.Tier)
			}
		})
	}
}

// A verb the floor could tighten must be able to say which record a call names,
// or the floor is consulted for nothing. The two generic verbs are the whole
// set today; the composition root's gate holds that claim against the contract.
func TestBothGenericVerbsNameTheRecordTypeOfACall(t *testing.T) {
	args := json.RawMessage(`{"record_type":"project","id":"` + ids.NewV7().String() + `","fields":{}}`)
	if got := (createRecord{}).RecordTypeOf(args); got != "project" {
		t.Errorf("create_record read record type %q, want project", got)
	}
	if got := (updateRecord{}).RecordTypeOf(args); got != "project" {
		t.Errorf("update_record read record type %q, want project", got)
	}
	if got := (updateRecord{}).RecordTypeOf(json.RawMessage(`{`)); got != "" {
		t.Errorf("an unreadable argument object named record type %q, want none — a guess here "+
			"would tighten or loosen a call nobody could read", got)
	}
}

// A staged create must be refusable for the same reasons Handle refuses it,
// because the approved retry re-enters through Handle: a rule enforced only
// there is one a human's yes is spent discovering.
func TestAStagedCreateRefusesWhatItsHandlerWouldRefuse(t *testing.T) {
	tool := createRecord{}
	if _, err := tool.StageInfo(context.Background(),
		json.RawMessage(`{"record_type":"person","fields":{"not_a_field":"x"}}`)); err == nil {
		t.Error("a create naming a field the contract does not declare staged cleanly; a human " +
			"would approve it and the retry would then be refused with the approval already spent")
	}
	if _, err := tool.StageInfo(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("an unreadable create staged cleanly")
	}
	// rejectUnknownFields deliberately passes a record type it does not know —
	// naming the served vocabulary is the provider's refusal to make. That is
	// right for a call about to reach the provider and wrong for one about to
	// reach a human, so the verb's own served set is checked first.
	if _, err := tool.StageInfo(context.Background(),
		json.RawMessage(`{"record_type":"custom_field","fields":{}}`)); err == nil {
		t.Error("a create of a record type this verb cannot create staged cleanly; the approved " +
			"retry would die at the provider with the human's one-shot approval already spent")
	}
}

// The summary is the whole of what a triaging human reads on the inbox row.
func TestAGenericWriteSaysWhatItWouldSet(t *testing.T) {
	line := describeGenericWrite("Create", "project", json.RawMessage(`{"name":"N","organization_id":"o"}`))
	if !strings.Contains(line, "project") || !strings.Contains(line, "name") ||
		!strings.Contains(line, "organization_id") {
		t.Errorf("summary %q names neither the record type nor the fields the call sets", line)
	}

	if got := describeGenericWrite("Update", "deal", json.RawMessage(`{}`)); got != "Update a deal" {
		t.Errorf("an empty patch rendered %q, want the act alone rather than a dangling list", got)
	}
	if got := describeGenericWrite("Update", "deal", json.RawMessage(`not json`)); got != "Update a deal" {
		t.Errorf("an unreadable patch rendered %q; the summary may not invent fields", got)
	}

	wide := map[string]int{}
	for i := range summaryFieldLimit + 4 {
		wide["f"+string(rune('a'+i))] = i
	}
	raw, err := json.Marshal(wide)
	if err != nil {
		t.Fatalf("building the wide patch: %v", err)
	}
	if line := describeGenericWrite("Update", "person", raw); !strings.Contains(line, "more") {
		t.Errorf("a %d-field patch rendered %q with no overflow marker, so the inbox line silently "+
			"drops fields the call would set", len(wide), line)
	}
}

// Stageable and NamesRecordType are read by the composition root's gates, and a
// gate that answers for a verb nobody registered would certify an empty surface.
func TestTheRegistryAnswersNothingForAnUnregisteredVerb(t *testing.T) {
	r := NewRegistry(nil, nil)
	if r.Stageable("summon_demon") {
		t.Error("an unregistered verb reported as stageable")
	}
	if r.NamesRecordType("summon_demon") {
		t.Error("an unregistered verb reported as naming a record type")
	}
}

// A staged update reads the row it patches, so a record the caller cannot see is
// refused before a human is asked about it rather than after.
func TestAStagedUpdateRefusesARecordItCannotRead(t *testing.T) {
	tool := updateRecord{p: unreadableProvider{}}
	_, err := tool.StageInfo(context.Background(), json.RawMessage(
		`{"record_type":"person","id":"`+ids.NewV7().String()+`","fields":{"full_name":"X"}}`))
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("staging a patch against an unreadable record answered %v, want not-found — "+
			"otherwise the inbox shows a change against a row the approver cannot see", err)
	}
	if _, err := tool.StageInfo(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("an unreadable patch staged cleanly")
	}
	// The field check runs before the read, so a patch naming a field the
	// contract does not declare is refused whether or not its record exists.
	if _, err := tool.StageInfo(context.Background(), json.RawMessage(
		`{"record_type":"person","id":"`+ids.NewV7().String()+`","fields":{"not_a_field":"x"}}`)); err == nil {
		t.Error("a patch naming an undeclared field staged cleanly; a human would approve it and " +
			"the retry would then be refused with the approval already spent")
	}
}
