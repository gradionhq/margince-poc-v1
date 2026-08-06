// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What these tools decide BEFORE the seam is the part that is theirs: the
// admission a bad argument meets, and — for the two confirm-first ones — the
// refusal a human must never be asked to approve. Everything past the seam is
// the owning module's own behaviour, tested there.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// errLifecycleSeamReached fails a test whose tool passed arguments through that
// it should have refused: a refusal that happens AFTER the write is not one.
var errLifecycleSeamReached = errors.New("the seam was reached")

// stubRecordProvider answers one record, so the staging paths run without a
// database. It embeds the probe provider, so any method beyond Read fails
// loudly instead of answering a zero value.
type stubRecordProvider struct {
	seamProbeProvider
	rec datasource.Record
}

func (p stubRecordProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return p.rec, nil
}

func stagedRecord(entity datasource.EntityType, id ids.UUID, authoritative bool) datasource.Record {
	return datasource.Record{
		Ref:       datasource.EntityRef{Type: entity, ID: id},
		Fields:    json.RawMessage(`{"name":"Acme"}`),
		Version:   11,
		Freshness: datasource.FreshnessInfo{Authoritative: authoritative},
	}
}

// A staged row is what a human decides from, so it must pin the version the
// decision was made against and name the record in words.
func TestConfirmFirstLifecycleToolsStageAgainstTheRecordTheyRead(t *testing.T) {
	leadID, projectID := ids.NewV7(), ids.NewV7()
	cases := []struct {
		name       string
		info       func() (StageInfo, error)
		wantTarget string
		wantID     ids.UUID
	}{
		{
			name: "disqualify_lead",
			info: func() (StageInfo, error) {
				tool := disqualifyLead{p: stubRecordProvider{rec: stagedRecord(datasource.EntityLead, leadID, true)}}
				return tool.StageInfo(context.Background(), json.RawMessage(`{"lead_id":"`+leadID.String()+`"}`))
			},
			wantTarget: string(datasource.EntityLead), wantID: leadID,
		},
		{
			name: "advance_project_phase",
			info: func() (StageInfo, error) {
				tool := advanceProjectPhase{p: stubRecordProvider{rec: stagedRecord(datasource.EntityProject, projectID, true)}}
				return tool.StageInfo(context.Background(),
					json.RawMessage(`{"project_id":"`+projectID.String()+`","to_phase":"delivering"}`))
			},
			wantTarget: string(datasource.EntityProject), wantID: projectID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := tc.info()
			if err != nil {
				t.Fatal(err)
			}
			if info.TargetType != tc.wantTarget || info.TargetID != tc.wantID {
				t.Fatalf("staged against %s/%s, want %s/%s", info.TargetType, info.TargetID, tc.wantTarget, tc.wantID)
			}
			if info.TargetVersion == nil || *info.TargetVersion != 11 {
				t.Fatalf("target version = %v, want the version the read returned", info.TargetVersion)
			}
			if info.Summary == "" {
				t.Fatal("an empty summary asks a human to decide an unnamed thing")
			}
		})
	}
}

// An approval against a mirror-held record could never be released, so staging
// one mints an authority object nobody can act on.
func TestConfirmFirstLifecycleToolsRefuseToStageAMirrorHeldRecord(t *testing.T) {
	leadID, projectID := ids.NewV7(), ids.NewV7()

	lead := disqualifyLead{p: stubRecordProvider{rec: stagedRecord(datasource.EntityLead, leadID, false)}}
	if _, err := lead.StageInfo(context.Background(),
		json.RawMessage(`{"lead_id":"`+leadID.String()+`"}`)); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("disqualify_lead err = %v, want ErrUnsupportedBySoR", err)
	}

	project := advanceProjectPhase{p: stubRecordProvider{rec: stagedRecord(datasource.EntityProject, projectID, false)}}
	if _, err := project.StageInfo(context.Background(),
		json.RawMessage(`{"project_id":"`+projectID.String()+`","to_phase":"closed","reason":"done"}`)); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("advance_project_phase err = %v, want ErrUnsupportedBySoR", err)
	}
}

type recordingRelinker struct{ entityType string }

func (r *recordingRelinker) RelinkActivity(
	_ context.Context, _ ids.UUID, entityType string, _ ids.UUID, _ bool,
) (json.RawMessage, error) {
	r.entityType = entityType
	return json.RawMessage(`{}`), nil
}

type unreachableAdvancer struct{}

func (unreachableAdvancer) AdvanceProjectPhase(
	context.Context, ids.UUID, string, *string, *int64,
) (json.RawMessage, error) {
	return nil, errLifecycleSeamReached
}

// recordingAdvancer captures what the tool decided to pass on.
type recordingAdvancer struct{ ifVersion *int64 }

func (a *recordingAdvancer) AdvanceProjectPhase(
	_ context.Context, _ ids.UUID, _ string, _ *string, ifVersion *int64,
) (json.RawMessage, error) {
	a.ifVersion = ifVersion
	return json.RawMessage(`{}`), nil
}

// recordingDisqualifier captures the id Handle passed to the store.
type recordingDisqualifier struct{ id ids.UUID }

func (d *recordingDisqualifier) DisqualifyLead(_ context.Context, id ids.UUID) (json.RawMessage, error) {
	d.id = id
	return json.RawMessage(`{}`), nil
}

// The tool decodes and hands off — the lead's own gates are the store's. What
// is worth pinning is that it hands off the id it was given and refuses a body
// that names no lead, rather than passing a zero id to a delete.
func TestDisqualifyLeadHandsTheNamedLeadToTheStore(t *testing.T) {
	seam := &recordingDisqualifier{}
	tool := disqualifyLead{disqualifier: seam}
	id := ids.NewV7()

	if _, err := tool.Handle(context.Background(), json.RawMessage(`{"lead_id":"`+id.String()+`"}`)); err != nil {
		t.Fatal(err)
	}
	if seam.id != id {
		t.Fatalf("store saw lead %s, want %s", seam.id, id)
	}

	if _, err := tool.Handle(context.Background(), json.RawMessage(`{"lead_id":"not-a-uuid"}`)); err == nil {
		t.Fatal("a malformed lead_id was accepted")
	}
}

func TestRelinkActivityRefusesATargetTypeTheStoreWouldNotAccept(t *testing.T) {
	seam := &recordingRelinker{}
	tool := relinkActivity{relinker: seam}

	args := func(entityType string) json.RawMessage {
		return json.RawMessage(`{"activity_id":"` + ids.NewV7().String() +
			`","entity_type":"` + entityType + `","entity_id":"` + ids.NewV7().String() + `"}`)
	}

	if _, err := tool.Handle(context.Background(), args("activity")); err == nil {
		t.Fatal("linking an activity to an activity was accepted; the store's own enum refuses it")
	} else if seam.entityType != "" {
		t.Fatalf("the refusal reached the seam first (entity_type=%q)", seam.entityType)
	}

	if _, err := tool.Handle(context.Background(), args("person")); err != nil {
		t.Fatalf("a person is a link target: %v", err)
	}
	if seam.entityType != "person" {
		t.Fatalf("seam saw entity_type %q, want person", seam.entityType)
	}
}

// Closing a project without a reason is a 422 the contract states, so a call
// that stages, waits for a human, and THEN fails is a person asked to decide
// something that could never have applied.
func TestAdvanceProjectPhaseRefusesAClosureWithNoReasonBeforeStaging(t *testing.T) {
	tool := advanceProjectPhase{advancer: unreachableAdvancer{}}
	closing := json.RawMessage(`{"project_id":"` + ids.NewV7().String() + `","to_phase":"closed"}`)

	if _, err := tool.StageInfo(context.Background(), closing); err == nil {
		t.Fatal("a reasonless closure was staged; a human would approve a call the store then refuses")
	} else if !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("err = %v, want the reason requirement named", err)
	}

	if _, err := tool.Handle(context.Background(), closing); err == nil {
		t.Fatal("a reasonless closure reached the seam")
	}
}

func TestAdvanceProjectPhaseRefusesAPhaseOutsideTheLadder(t *testing.T) {
	tool := advanceProjectPhase{advancer: unreachableAdvancer{}}
	_, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+ids.NewV7().String()+`","to_phase":"archived"}`))
	if err == nil || !strings.Contains(err.Error(), "not a project phase") {
		t.Fatalf("err = %v, want the phase refused by name", err)
	}
}

// The pin is the CALLER's or it is nothing: a version the tool read itself
// would be compared against itself and could never detect skew.
func TestAdvanceProjectPhasePassesTheCallersVersionThrough(t *testing.T) {
	seam := &recordingAdvancer{}
	tool := advanceProjectPhase{advancer: seam}
	id := ids.NewV7().String()

	if _, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+id+`","to_phase":"delivering","if_version":7}`)); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion == nil || *seam.ifVersion != 7 {
		t.Fatalf("if_version reached the store as %v, want 7", seam.ifVersion)
	}

	seam.ifVersion = nil
	if _, err := tool.Handle(context.Background(),
		json.RawMessage(`{"project_id":"`+id+`","to_phase":"delivering"}`)); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion != nil {
		t.Fatalf("an omitted if_version became %v — the tool invented a pin", *seam.ifVersion)
	}
}

// Redemption commits its own transaction and the write opens a fresh one, so
// the approved version has to travel INTO the write or the window between them
// belongs to whoever is racing it — and on this surface that is the same agent,
// through its own auto-execute update_record. The REST gate closes the window by
// forwarding the pin as If-Match; this is that guarantee on the MCP transport.
func TestAReleasedApprovalPinsTheWriteItReleases(t *testing.T) {
	seam := &recordingAdvancer{}
	tool := advanceProjectPhase{advancer: seam}
	call := json.RawMessage(`{"project_id":"` + ids.NewV7().String() + `","to_phase":"delivering"}`)

	// Unredeemed and unpinned by the caller: nothing to condition on, and the
	// tool must not invent one.
	if _, err := tool.Handle(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion != nil {
		t.Fatalf("an unredeemed call carried if_version %v", *seam.ifVersion)
	}

	// Redeemed against version 9: the write is conditioned on 9.
	seam.ifVersion = nil
	released := withApprovalRedeemed(context.Background(), 9, true)
	if _, err := tool.Handle(released, call); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion == nil || *seam.ifVersion != 9 {
		t.Fatalf("released pin reached the store as %v, want 9 — the approved version has to be "+
			"re-checked inside the transaction that writes", seam.ifVersion)
	}

	// A caller that named its own version keeps it: that value is bound into
	// the diff_hash the redemption verified, so it cannot disagree with what
	// the human approved.
	seam.ifVersion = nil
	pinned := json.RawMessage(`{"project_id":"` + ids.NewV7().String() + `","to_phase":"delivering","if_version":4}`)
	if _, err := tool.Handle(released, pinned); err != nil {
		t.Fatal(err)
	}
	if seam.ifVersion == nil || *seam.ifVersion != 4 {
		t.Fatalf("caller pin reached the store as %v, want 4", seam.ifVersion)
	}
}

// The schema is what a client reads; the admission map is what actually decides.
// A value advertised and then refused is the same divergence a missing tool is,
// arriving one argument at a time — and the compose-side gate that binds the
// schema to the CONTRACT cannot see this layer, because it never calls the tool.
// So every advertised value is walked through the tool's own admission here.
func TestEveryAdvertisedEnumValueIsActuallyAdmitted(t *testing.T) {
	relink := relinkActivity{relinker: &recordingRelinker{}}
	advance := advanceProjectPhase{advancer: &recordingAdvancer{}}

	cases := []struct {
		tool  string
		arg   string
		spec  mcp.ToolSpec
		admit func(value string) error
	}{
		{
			tool: "relink_activity", arg: "entity_type", spec: relink.Spec(),
			admit: func(value string) error {
				_, err := relink.Handle(context.Background(), json.RawMessage(
					`{"activity_id":"`+ids.NewV7().String()+`","entity_type":"`+value+
						`","entity_id":"`+ids.NewV7().String()+`"}`))
				return err
			},
		},
		{
			tool: "advance_project_phase", arg: "to_phase", spec: advance.Spec(),
			admit: func(value string) error {
				// `closed` carries its own required reason; supplying it keeps
				// this about the vocabulary rather than the closure rule.
				_, err := advance.Handle(context.Background(), json.RawMessage(
					`{"project_id":"`+ids.NewV7().String()+`","to_phase":"`+value+`","reason":"a reason"}`))
				return err
			},
		},
	}

	for _, tc := range cases {
		values := advertisedEnum(t, tc.spec.InputSchema, tc.arg)
		for _, value := range values {
			t.Run(tc.tool+"/"+value, func(t *testing.T) {
				if err := tc.admit(value); err != nil {
					t.Errorf("%s advertises %q for %s and then refuses it: %v", tc.tool, value, tc.arg, err)
				}
			})
		}
	}
}

// advertisedEnum reads a property's declared vocabulary out of a tool's own
// input schema, which is the only copy a client ever sees.
func advertisedEnum(t *testing.T, inputSchema json.RawMessage, arg string) []string {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &doc); err != nil {
		t.Fatalf("input schema is not readable: %v", err)
	}
	values := doc.Properties[arg].Enum
	if len(values) == 0 {
		t.Fatalf("the schema advertises no %s vocabulary — this sweep would walk nothing", arg)
	}
	return values
}
