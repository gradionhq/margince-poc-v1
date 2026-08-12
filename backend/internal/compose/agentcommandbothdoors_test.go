// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// One operation, two doors, one command.
//
// For an operation a client can reach BOTH ways — the REST route and the tool
// verb its `x-mcp-tool` names — the two doors must resolve to the same governed
// call: the same record, the same version to pin, the same sentence a human
// reads. That is the whole claim of the command seam (modules/agents/command.go)
// and the only claim no single-door test can make, because each door is
// perfectly self-consistent while describing a different act.
//
// The fixtures are written PER DOOR, from each door's own published contract:
// the REST request from the route and requestBody crm.yaml declares, the tool
// arguments from the verb's own InputSchema. Deriving one from the other — call
// a decoder, feed its output to the twin — would compare a decoder with itself
// and pass for any pair of doors that agreed on nothing.
//
// The enrich verb is why the summary is compared and not only the target. Its
// two operations are one verb at two DEPTHS against one organization, so a
// swapped depth stages an identical target under a sentence describing the
// other act: a human releasing a whole-site crawl on a line written for a
// single page read.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// bothDoorsFixture is one intended act, written twice — once as the request the
// REST route carries, once as the arguments the tool schema declares. Both
// builders take the same two ids so the two spellings name the same records
// without either being derived from the other: primary is the record the route
// names, secondary the second record an operation that has one (a merge's
// survivor) names in its body.
type bothDoorsFixture struct {
	rest func(primary, secondary ids.UUID) (*http.Request, []byte)
	args func(primary, secondary ids.UUID) string
}

// doorRequest builds what the chi router would hand the gate: the concrete
// path, the routed {id} bound the way the router binds it, and the body the
// gate buffered.
func doorRequest(method, path string, routed ids.UUID, body string) (*http.Request, []byte) {
	payload := []byte(body)
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	if routed.IsZero() {
		return req, payload
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", routed.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)), payload
}

// archiveDoors, createDoors, updateDoors and mergeDoors write the four generic
// families once each. The families are generic on BOTH doors — one route shape,
// one tool verb, the record type as an argument — so a per-operation literal
// would be the same four lines twenty times, and a divergence would hide in the
// repetition rather than stand out.
func archiveDoors(collection, recordType string) bothDoorsFixture {
	return bothDoorsFixture{
		rest: func(primary, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodDelete, "/v1/"+collection+"/"+primary.String(), primary, "")
		},
		args: func(primary, _ ids.UUID) string {
			return `{"record_type":"` + recordType + `","id":"` + primary.String() + `"}`
		},
	}
}

func createDoors(collection, recordType, fields string) bothDoorsFixture {
	return bothDoorsFixture{
		rest: func(_, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodPost, "/v1/"+collection, ids.UUID{}, fields)
		},
		args: func(_, _ ids.UUID) string {
			return `{"record_type":"` + recordType + `","fields":` + fields + `}`
		},
	}
}

func updateDoors(collection, recordType, fields string) bothDoorsFixture {
	return bothDoorsFixture{
		rest: func(primary, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodPatch, "/v1/"+collection+"/"+primary.String(), primary, fields)
		},
		args: func(primary, _ ids.UUID) string {
			return `{"record_type":"` + recordType + `","id":"` + primary.String() + `","fields":` + fields + `}`
		},
	}
}

// mergeDoors is the one family where the two doors name the two records
// differently on purpose: the route names the record merged AWAY and the body
// the survivor, while the tool names both as arguments. An approval binds to
// the survivor either way, which is exactly what a per-door fixture can prove
// and a derived one cannot.
func mergeDoors(collection, recordType string) bothDoorsFixture {
	return bothDoorsFixture{
		rest: func(primary, secondary ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodPost, "/v1/"+collection+"/"+primary.String()+"/merge", primary,
				`{"target_id":"`+secondary.String()+`"}`)
		},
		args: func(primary, secondary ids.UUID) string {
			return `{"record_type":"` + recordType + `","source_id":"` + primary.String() +
				`","target_id":"` + secondary.String() + `"}`
		},
	}
}

// enrichDoors is the pair the gate exists for. The REST door has no depth
// argument at all — its two ROUTES are the two depths — so the depth is
// structural on one door and a word on the other, which is the one shape where
// two doors can agree on every field and still mean different acts.
func enrichDoors(path string, depth agents.EnrichDepth) bothDoorsFixture {
	return bothDoorsFixture{
		rest: func(primary, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodPost, "/v1/organizations/"+primary.String()+"/"+path, primary, "")
		},
		args: func(primary, _ ids.UUID) string {
			return `{"organization_id":"` + primary.String() + `","depth":"` + string(depth) + `"}`
		},
	}
}

// bothDoorsFixtures is the act each twinned operation performs, per door.
//
// The `fields` payloads name only members the record type's own contract shape
// declares (agents.createRecordShapes / updateRecordShapes): the resolvers
// refuse an unknown key before staging anything, so a typo here would be
// answered as a refusal on both doors and the comparison would hold vacuously.
var bothDoorsFixtures = map[string]bothDoorsFixture{
	"archivePerson":       archiveDoors("people", "person"),
	"archiveOrganization": archiveDoors("organizations", "organization"),
	"archiveDeal":         archiveDoors("deals", "deal"),
	"archiveProject":      archiveDoors("projects", "project"),
	"archiveRelationship": archiveDoors("relationships", "relationship"),

	"createPerson":       createDoors("people", "person", `{"full_name":"Ada Lovelace"}`),
	"createOrganization": createDoors("organizations", "organization", `{"display_name":"Acme"}`),
	"createDeal": createDoors("deals", "deal", `{"name":"Acme renewal",`+
		`"pipeline_id":"019ff000-0000-7000-8000-000000000011","stage_id":"019ff000-0000-7000-8000-000000000012"}`),
	"createLead": createDoors("leads", "lead", `{"full_name":"Grace Hopper","company_name":"Acme"}`),
	"createProject": createDoors("projects", "project",
		`{"name":"Acme rollout","organization_id":"019ff000-0000-7000-8000-000000000013"}`),
	"createRelationship": createDoors("relationships", "relationship",
		`{"kind":"employment","person_id":"019ff000-0000-7000-8000-000000000014"}`),

	"updatePerson":       updateDoors("people", "person", `{"title":"CTO"}`),
	"updateOrganization": updateDoors("organizations", "organization", `{"industry":"payments"}`),
	"updateDeal":         updateDoors("deals", "deal", `{"forecast_category":"commit"}`),
	"updateLead":         updateDoors("leads", "lead", `{"status":"working"}`),
	"updateActivity":     updateDoors("activities", "activity", `{"subject":"Renewal call"}`),
	"updateProject":      updateDoors("projects", "project", `{"description":"Rollout"}`),
	"updateRelationship": updateDoors("relationships", "relationship", `{"role":"champion"}`),

	"mergePerson":       mergeDoors("people", "person"),
	"mergeOrganization": mergeDoors("organizations", "organization"),

	"scrapeCompany":   enrichDoors("enrich", agents.EnrichDepthPage),
	"deepReadCompany": enrichDoors("deep-read", agents.EnrichDepthSite),

	"promoteLead": {
		rest: func(primary, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodPost, "/v1/leads/"+primary.String()+"/promote", primary,
				`{"trigger":"inbound_reply"}`)
		},
		args: func(primary, _ ids.UUID) string {
			return `{"lead_id":"` + primary.String() + `","trigger":"inbound_reply"}`
		},
	},
	"disqualifyLead": {
		rest: func(primary, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodDelete, "/v1/leads/"+primary.String(), primary, "")
		},
		args: func(primary, _ ids.UUID) string { return `{"lead_id":"` + primary.String() + `"}` },
	},
	"advanceProjectPhase": {
		rest: func(primary, _ ids.UUID) (*http.Request, []byte) {
			return doorRequest(http.MethodPost, "/v1/projects/"+primary.String()+"/advance", primary,
				`{"to_phase":"pursuing"}`)
		},
		args: func(primary, _ ids.UUID) string {
			return `{"project_id":"` + primary.String() + `","to_phase":"pursuing"}`
		},
	},
}

// twinnedOperations is the subject set, derived: an operation whose declared
// verb is a REGISTERED tool that can stage, and whose own operationId that
// tool's spec names as one of the operations it twins (ToolSpec.OpenAPIOp, the
// `/`-separated list a verb serving several operations carries).
//
// The OpenAPIOp reading is what keeps this set honest in both directions. Most
// of the surface shares a verb with operations the verb cannot express —
// confirmOrganizationFact is an `update_record` operation no update_record call
// can spell — and a set built from the verb alone would demand a tool fixture
// for an act the tool door has no way to ask for. A composed entry (`getPerson
// + listActivities`) matches no operationId and drops out on its own.
func twinnedOperations(served *agents.Registry) map[string]string {
	twins := map[string]string{}
	for op, route := range agentReachableMutations() {
		pol := agentPolicies[route]
		spec, registered := served.Spec(pol.Tool)
		if !registered || !served.Stageable(pol.Tool) {
			continue
		}
		for _, named := range strings.Split(spec.OpenAPIOp, "/") {
			if named == op {
				twins[op] = pol.Tool
			}
		}
	}
	return twins
}

// bothDoorsRegistry is the tool door under test: the production registrations,
// over the record seam every one of these resolvers reads through and nothing
// else. The executor seams are nil on purpose — a call that EXECUTED instead of
// staging would fault here rather than quietly pass.
//
// The floor tightens every (verb, record type) it is asked about, which is how
// a 🟢 verb comes to stage at all. It is the contract's own mechanism (#982),
// injected with a test's answer rather than the generated table's: the question
// this gate asks — do the two doors resolve one command — has the same answer
// at either tier, and keying the subject set on today's floor would quietly
// drop an operation the day the contract loosened one.
func bothDoorsRegistry(staging agents.Approvals) *agents.Registry {
	reg := agents.NewRegistry(staging, auth.NewGate(fullSeat{}),
		agents.WithTierFloor(func(string, string) (mcp.RiskTier, bool) {
			return mcp.TierConfirmationRequired, true
		}))
	agents.RegisterCoreTools(reg, seamRecord{}, nil, nil, nil)
	agents.RegisterEnrichTool(reg, seamRecord{}, nil)
	agents.RegisterLifecycleTools(reg, seamRecord{}, nil, nil, nil)
	return reg
}

// agentDoorCtx is a passport principal holding every cap, so admission turns on
// the call's tier alone — which is the only thing this gate wants it to turn on.
func agentDoorCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:both-doors", SeatType: principal.SeatFull,
		OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite,
			principal.ScopeSend, principal.ScopeEnrich, principal.ScopeDraft),
	})
}

// outOfLaneVerbs are the twinned verbs whose tool cannot be built without a
// live comms or scheduling seam — both of which need a pool, which is a
// database, which is not this lane. The claim is CHECKED rather than trusted:
// the walk asserts each is registered on the production surface and absent from
// the one built here, so a verb that becomes buildable stops being excused.
//
// Their two doors are compared where a database exists:
// TestBothDoorsStageOneRowForOneOperation (agentcommandstagedrow_integration_test.go)
// takes the send through the real registry and the real approvals engine.
var outOfLaneVerbs = gatekit.Waive(map[string]string{
	"send_email": "a reply send is built over the comms seam, which needs a pool — so this lane compares " +
		"nothing about the two doors of POST /v1/activities/{id}/send-email",
	"send_message": "a channel reply is built over the comms seam, which needs a pool — so this lane " +
		"compares nothing about the two doors of POST /v1/activities/{id}/send-message",
	"send_account_email": "an account-started send is built over the comms seam, which needs a pool — so " +
		"this lane compares nothing about the two doors of POST /v1/emails",
	"book_meeting": "a booking is built over the scheduling seam, which needs a pool — so this lane " +
		"compares nothing about the two doors of POST /v1/bookings",
})

func TestBothDoorsResolveOneOperationToOneCommand(t *testing.T) {
	defer outOfLaneVerbs.AssertAllMatched(t)
	served := NewRegistry(nil, SendPath{})
	twins := twinnedOperations(served)
	if len(twins) == 0 {
		t.Fatal("no operation has a stageable tool twin — this gate compared nothing")
	}
	for op, tool := range twins {
		fixture, written := bothDoorsFixtures[op]
		if !written {
			assertVerbIsOutOfLane(t, served, op, tool)
			continue
		}
		t.Run(op, func(t *testing.T) {
			compareDoors(t, op, tool, fixture)
		})
	}
	for op := range bothDoorsFixtures {
		if _, twinned := twins[op]; !twinned {
			t.Errorf("%s has a two-door fixture and no stageable tool twin — the fixture describes a call the "+
				"tool door cannot be asked to make, and the comparison below never runs for it", op)
		}
	}
}

// assertVerbIsOutOfLane holds an unwritten fixture to its stated reason. A
// dynamic-tier verb resolves its tier by READING the record, so whether it
// stages at all is a fact about workspace state rather than about the call, and
// there is no staged row for a fixture to compare; anything else must be a verb
// this lane cannot build a tool for.
func assertVerbIsOutOfLane(t *testing.T, served *agents.Registry, op, tool string) {
	t.Helper()
	if spec, _ := served.Spec(tool); spec.Tier == mcp.TierDynamic {
		return
	}
	if !outOfLaneVerbs.Waived(t, tool) {
		t.Errorf("%s is twinned by the stageable verb %s and has no two-door fixture, so nothing checks that "+
			"the route and the tool call resolve to one command", op, tool)
		return
	}
	if _, buildable := bothDoorsRegistry(&capturingApprovals{}).Spec(tool); buildable {
		t.Errorf("%s is excused as unbuildable in this lane, and this lane registers it after all — write "+
			"%s's fixture instead", tool, op)
	}
}

// compareDoors resolves one operation through each door and holds the two
// answers to being the same one.
//
// The REST side is compared at the COMMAND, not at the staged row: this door
// writes its own inbox line (restSummary names the concrete method and path a
// REST diff hash binds), so the resolver's sentence never reaches the approval
// it stages. The sentence is still the only handle either door has on what the
// erased command carries — the enrich depth lives nowhere else — so it is the
// resolved StageInfo that is held against the tool door's staged row.
func compareDoors(t *testing.T, op, tool string, fixture bothDoorsFixture) {
	t.Helper()
	primary, secondary := ids.NewV7(), ids.NewV7()
	pol := agentPolicies[agentReachableMutations()[op]]

	req, body := fixture.rest(primary, secondary)
	call, err := restCommands[op](pol, restCommandDeps{records: seamRecord{}}, req, body)
	if err != nil {
		t.Fatalf("the REST door refused the request its own route declares: %v", err)
	}
	rest, err := agents.StageSubject(req.Context(), call)
	if err != nil {
		t.Fatalf("the REST door's command refused to name a subject: %v", err)
	}

	staging := &capturingApprovals{}
	_, err = bothDoorsRegistry(staging).Invoke(agentDoorCtx(), tool,
		json.RawMessage(fixture.args(primary, secondary)))
	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("the tool door answered %v rather than staging the call its own schema declares", err)
	}

	if staging.last.TargetType != rest.TargetType || staging.last.TargetID != rest.TargetID {
		t.Errorf("the doors bind one operation to two records: REST (%s,%s), tool (%s,%s) — one of them asks "+
			"a human about a row the other would not have written",
			rest.TargetType, rest.TargetID, staging.last.TargetType, staging.last.TargetID)
	}
	if staging.last.Summary != rest.Summary {
		t.Errorf("the doors describe one operation two ways:\n  REST: %q\n  tool: %q\nA human releasing one "+
			"reads the other's act", rest.Summary, staging.last.Summary)
	}
	if (staging.last.TargetVersion == nil) != (rest.TargetVersion == nil) {
		t.Errorf("one door pins a version the other does not (REST %v, tool %v) — the same call would be held "+
			"to a record's version through one door and not through the other",
			rest.TargetVersion, staging.last.TargetVersion)
	}
	if staging.last.Tool != tool {
		t.Errorf("the tool door staged kind %q for verb %s", staging.last.Tool, tool)
	}
}
