// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A relationship edge as a first-class resource on the tool surface: created,
// read, patched and archived through compose.NewRegistry, the same constructor
// the api role uses.
//
// A new entity reaching records through the datasource seam is where visibility
// goes wrong, so that is what most of this file is about. An edge's visibility
// derives from its ENDPOINTS — every non-null one must be visible to the caller
// — and the failure mode is specific: an edge that answered would name two
// records, so an edge readable by someone who cannot read its organization
// leaks that organization's existence and its link to a person. Unit tests
// cannot reach that rule; it is rendered as SQL against the caller's row scope.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// edgeFields is the read-back shape a caller sees. Only what the contract
// declares is decoded, so a field the wire mapping drops fails here — which is
// the whole point for project_id, which it used to drop.
type edgeFields struct {
	Kind             string    `json:"kind"`
	PersonID         *ids.UUID `json:"person_id"`
	OrganizationID   *ids.UUID `json:"organization_id"`
	DealID           *ids.UUID `json:"deal_id"`
	ProjectID        *ids.UUID `json:"project_id"`
	Role             *string   `json:"role"`
	IsCurrentPrimary *bool     `json:"is_current_primary"`
	StartedAt        *string   `json:"started_at"`
}

// wireEdge decodes a create/update/read answer into the record envelope plus
// the edge's own fields.
func wireEdge(t *testing.T, raw json.RawMessage) (ids.UUID, edgeFields) {
	t.Helper()
	var record struct {
		RecordType string          `json:"record_type"`
		ID         ids.UUID        `json:"id"`
		Fields     json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(ToolPayload(t, raw), &record); err != nil {
		t.Fatalf("unreadable answer %s: %v", raw, err)
	}
	if record.RecordType != "relationship" {
		t.Fatalf("record_type = %q, want relationship (answer %s)", record.RecordType, raw)
	}
	var fields edgeFields
	if err := json.Unmarshal(record.Fields, &fields); err != nil {
		t.Fatalf("unreadable edge fields %s: %v", record.Fields, err)
	}
	return record.ID, fields
}

// relationshipReaderPerms is a row-scoped caller: they may read and write edges
// and their endpoints, but only over rows they own. RowScopeOwn is what makes
// the endpoint-conjunction clause render at all — an unbounded caller carries no
// clause, so a suite that only used AdminPerms would assert nothing about it.
func relationshipReaderPerms() principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"person":       {Create: true, Read: true, Update: true, Delete: true},
			"organization": {Create: true, Read: true, Update: true, Delete: true},
			"relationship": {Create: true, Read: true, Update: true, Delete: true},
		},
		RowScope: principal.RowScopeOwn,
	}
}

// seedEndpointPair creates a person and an organization under the caller's own
// context, so both are visible to it whatever row scope it carries.
func seedEndpointPair(ctx context.Context, t *testing.T, e *Env, who, where string) (person, org ids.UUID) {
	t.Helper()
	p, err := e.People.CreatePerson(ctx, people.CreatePersonInput{FullName: who, Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the person %q: %v", who, err)
	}
	o, err := e.People.CreateOrganization(ctx, people.CreateOrganizationInput{DisplayName: where, Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the organization %q: %v", where, err)
	}
	return ids.UUID(p.Id), ids.UUID(o.Id)
}

// createEmployment writes one employment edge through create_record and returns
// the edge as the caller reads it back.
func createEmployment(ctx context.Context, t *testing.T, r *agents.Registry, person, org ids.UUID, extra string) (ids.UUID, edgeFields) {
	t.Helper()
	created, err := r.Invoke(ctx, "create_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","fields":{"kind":"employment","person_id":%q,"organization_id":%q,"source":"ui"%s}}`,
		person, org, extra)))
	if err != nil {
		t.Fatalf("create_record relationship: %v", err)
	}
	return wireEdge(t, created)
}

func TestAnEmploymentEdgeLivesItsWholeLifeThroughTheToolSurface(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	person, org := seedEndpointPair(ctx, t, e, "Ada Employed", "Employer GmbH")

	// CREATE. The read-back is what proves the write landed: create_record
	// answers with the record it made, and for an edge that means the seam had
	// to serve a relationship Read too — without it the row would commit and the
	// tool would report a read-back failure.
	edgeID, fields := createEmployment(ctx, t, registry, person, org, `,"role":"cto","is_current_primary":true`)
	if fields.Kind != "employment" || fields.PersonID == nil || *fields.PersonID != person ||
		fields.OrganizationID == nil || *fields.OrganizationID != org {
		t.Fatalf("the edge read back as %+v, want an employment between the seeded pair", fields)
	}
	if fields.Role == nil || *fields.Role != "cto" {
		t.Errorf("role = %v, want cto", fields.Role)
	}

	// UPDATE. A patch reaches the dates and role, never the endpoints. Filling
	// a field the edge does not hold yet stays auto-execute — there is no human
	// value to undo — which is what makes this the case that proves the patch
	// path serves an edge at all.
	updated, err := registry.Invoke(ctx, "update_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","id":%q,"fields":{"started_at":"2026-02-01"}}`, edgeID)))
	if err != nil {
		t.Fatalf("update_record relationship: %v", err)
	}
	_, patched := wireEdge(t, updated)
	if patched.StartedAt == nil || *patched.StartedAt != "2026-02-01" {
		t.Errorf("started_at after patch = %v, want 2026-02-01", patched.StartedAt)
	}
	// The endpoints survived the patch: coalesce-style updates that lost a
	// nullable column would silently detach the edge from one of its ends.
	if patched.PersonID == nil || patched.OrganizationID == nil {
		t.Errorf("the patch dropped an endpoint: %+v", patched)
	}
	if patched.Role == nil || *patched.Role != "cto" {
		t.Errorf("role = %v after an unrelated patch, want the human's own cto", patched.Role)
	}

	// And per-field human-edit precedence reaches the new entity too: `role` was
	// written by a human, so overwriting it is STAGED rather than applied. This
	// is the rule that stops a tool call quietly undoing someone's edit, and a
	// new record type inherits it or it does not hold.
	_, err = registry.Invoke(ctx, "update_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","id":%q,"fields":{"role":"vp_sales"}}`, edgeID)))
	var overwrite *workflow.StagedApprovalError
	if !errors.As(err, &overwrite) {
		t.Errorf("overwriting a human-written role answered %v, want a staged approval", err)
	}

	// ARCHIVE. The caller here is a HUMAN in their own seat, so the 🟡 tier does
	// not stage — that gate is for agent principals, and the agent half is
	// proven over REST with a real passport
	// (TestArchivingAnEdgeStagesForAnAgentAndPinsItsVersion).
	if _, err := registry.Invoke(ctx, "archive_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","id":%q}`, edgeID))); err != nil {
		t.Fatalf("archive_record relationship: %v", err)
	}
	// And it is GONE from the read, like every other archived record: a Read
	// that went on serving it would report an employment that had been ended.
	if _, err := e.People.GetRelationship(ctx, edgeID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("reading the archived edge = %v, want ErrNotFound", err)
	}
	if _, err := registry.Invoke(ctx, "update_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","id":%q,"fields":{"started_at":"2026-04-01"}}`, edgeID))); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("patching the archived edge = %v, want ErrNotFound", err)
	}
}

func TestAnEdgeIsInvisibleWhenEitherEndpointIsOutOfTheCallersRowScope(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	admin := e.As(e.Rep1, nil, AdminPerms)

	// Both endpoints owned by Rep2. The edge is created by the admin, so its
	// existence owes nothing to Rep1.
	owner := ids.From[ids.UserKind](e.Rep2)
	person, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Rep2 Contact", OwnerID: &owner, Source: "manual",
	})
	if err != nil {
		t.Fatalf("seeding the person: %v", err)
	}
	org, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{
		DisplayName: "Rep2 Account", OwnerID: &owner, Source: "manual",
	})
	if err != nil {
		t.Fatalf("seeding the organization: %v", err)
	}
	created, err := registry.Invoke(admin, "create_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","fields":{"kind":"employment","person_id":%q,"organization_id":%q,"source":"ui"}}`,
		person.Id, org.Id)))
	if err != nil {
		t.Fatalf("create_record relationship as admin: %v", err)
	}
	edgeID, _ := wireEdge(t, created)

	// Rep3 owns neither endpoint and sits in the other team. Every verb that
	// returns or touches the row must answer NOT FOUND — not permission-denied,
	// which would confirm the edge exists.
	stranger := e.As(e.Rep3, []ids.UUID{e.Team2}, relationshipReaderPerms())
	for _, call := range []struct{ tool, args string }{
		{"update_record", fmt.Sprintf(`{"record_type":"relationship","id":%q,"fields":{"role":"cto"}}`, edgeID)},
		{"archive_record", fmt.Sprintf(`{"record_type":"relationship","id":%q}`, edgeID)},
	} {
		_, err := registry.Invoke(stranger, call.tool, json.RawMessage(call.args))
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Errorf("%s on an out-of-scope edge = %v, want ErrNotFound — an edge whose endpoints the "+
				"caller cannot read must not answer that it exists", call.tool, err)
		}
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("%s answered permission-denied, which confirms the edge exists", call.tool)
		}
	}

	// And the same caller CAN reach an edge over endpoints they do own, so the
	// refusals above are the scope rule and not a blanket denial. Without this
	// control the whole test would pass against a provider that refused every
	// relationship.
	ownPerson, ownOrg := seedEndpointPair(stranger, t, e, "Rep3 Own", "Rep3 Account")
	ownEdge, _ := createEmployment(stranger, t, registry, ownPerson, ownOrg, "")
	// started_at, not role: role carries a human's audited write from the create,
	// so patching it would stage for precedence and say nothing about row scope.
	if _, err := registry.Invoke(stranger, "update_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","id":%q,"fields":{"started_at":"2026-03-01"}}`, ownEdge))); err != nil {
		t.Errorf("Rep3 patching their own edge: %v — the refusals above are a blanket denial, not row scope", err)
	}
}

// An edge may not be created OVER an endpoint the caller cannot see either: a
// write that succeeded would tell the caller the record on the other end exists,
// and would attach one of their own records to it.
func TestAnEdgeCannotBeCreatedOverAnEndpointTheCallerCannotSee(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	admin := e.As(e.Rep1, nil, AdminPerms)

	owner := ids.From[ids.UserKind](e.Rep2)
	hidden, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{
		DisplayName: "Not Yours", OwnerID: &owner, Source: "manual",
	})
	if err != nil {
		t.Fatalf("seeding the hidden organization: %v", err)
	}
	stranger := e.As(e.Rep3, []ids.UUID{e.Team2}, relationshipReaderPerms())
	mine, err := e.People.CreatePerson(stranger, people.CreatePersonInput{FullName: "Rep3 Contact", Source: "manual"})
	if err != nil {
		t.Fatalf("Rep3 creating their own person: %v", err)
	}

	_, err = registry.Invoke(stranger, "create_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","fields":{"kind":"employment","person_id":%q,"organization_id":%q,"source":"ui"}}`,
		mine.Id, hidden.Id)))

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — an edge onto an invisible organization would disclose it", err)
	}
}

// The refusals a caller can act on. The shape and date rules carry their own
// fault verdicts, and this is the check that those verdicts reach the TOOL
// surface — which never runs the HTTP mapper, so a verdict that lived only in a
// transport branch would arrive as an internal fault with advice to retry.
func TestAMisshapenEdgeIsRefusedWithSomethingTheCallerCanAct(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, nil, AdminPerms)

	person, err := e.People.CreatePerson(ctx, people.CreatePersonInput{FullName: "Shape Probe", Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the person: %v", err)
	}
	org, err := e.People.CreateOrganization(ctx, people.CreateOrganizationInput{DisplayName: "Shape Org", Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the organization: %v", err)
	}

	for _, tc := range []struct {
		name, args, wants string
	}{
		{
			// The kind/endpoint mismatch: employment wants person + organization,
			// so a counterparty org is the wrong pair. The refusal is a
			// MessageFault, because the fault is in the PAIR — no single
			// argument is wrong on its own.
			name: "wrong endpoint pair for the kind",
			args: fmt.Sprintf(`{"record_type":"relationship","fields":{"kind":"employment","person_id":%q,`+
				`"counterparty_org_id":%q,"source":"ui"}}`, person.Id, org.Id),
			wants: "employment",
		},
		{
			name: "a kind the vocabulary does not have",
			args: fmt.Sprintf(`{"record_type":"relationship","fields":{"kind":"drinking_buddy","person_id":%q,`+
				`"organization_id":%q,"source":"ui"}}`, person.Id, org.Id),
			wants: "kind",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.Invoke(ctx, "create_record", json.RawMessage(tc.args))
			if err == nil {
				t.Fatal("the write was accepted")
			}
			// Classified, which is what decides whether the agent reads a
			// sentence it can act on or "the tool failed for an internal reason".
			fault, ok := httperr.Classify(err)
			if !ok {
				t.Fatalf("err = %v is outside the taxonomy, so the tool surface reports it as an "+
					"internal fault with advice to retry a call the server has already settled", err)
			}
			if fault.Status < 400 || fault.Status >= 500 {
				t.Errorf("status = %d, want a 4xx — this is the caller's mistake", fault.Status)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wants) {
				t.Errorf("refusal %q names nothing the caller can change; expected it to mention %q",
					err.Error(), tc.wants)
			}
		})
	}
}

// A date range that runs backwards names the field at fault, on this surface as
// on the other one.
func TestAnEdgeEndingBeforeItBeganNamesTheDateField(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, nil, AdminPerms)

	person, err := e.People.CreatePerson(ctx, people.CreatePersonInput{FullName: "Date Probe", Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the person: %v", err)
	}
	org, err := e.People.CreateOrganization(ctx, people.CreateOrganizationInput{DisplayName: "Date Org", Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the organization: %v", err)
	}

	_, err = registry.Invoke(ctx, "create_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","fields":{"kind":"employment","person_id":%q,"organization_id":%q,`+
			`"started_at":"2026-06-01","ended_at":"2026-01-01","source":"ui"}}`, person.Id, org.Id)))
	if err == nil {
		t.Fatal("an edge that ended before it began was accepted")
	}
	fault, ok := httperr.Classify(err)
	if !ok {
		t.Fatalf("err = %v is outside the taxonomy", err)
	}
	named := false
	for _, field := range fault.Fields {
		if field.Field == "ended_at" {
			named = true
		}
	}
	if !named {
		t.Errorf("the refusal's fields are %+v, want one naming ended_at — the field a caller changes",
			fault.Fields)
	}
}

// project_id, which the create mapping used to drop and the wire mapping never
// rendered. A project_stakeholder edge is the kind that cannot exist without it:
// unguarded, its project endpoint vanished on the way in and the database
// refused the edge for a pair the caller HAD supplied.
func TestAProjectStakeholderEdgeKeepsTheProjectItNames(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	person, err := e.People.CreatePerson(ctx, people.CreatePersonInput{FullName: "Stakeholder", Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the person: %v", err)
	}
	org := e.SeedOrg(t, "Project Owner GmbH", nil)
	project := seedProject(ctx, t, e, "Edge project", nil, org, nil)
	projectID := project.ID.UUID

	created, err := registry.Invoke(ctx, "create_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","fields":{"kind":"project_stakeholder","project_id":%q,"person_id":%q,`+
			`"role":"sponsor","source":"ui"}}`, projectID, person.Id)))
	if err != nil {
		t.Fatalf("create_record project_stakeholder: %v — this is the shape that fails when project_id "+
			"is dropped between the body and the store", err)
	}
	_, fields := wireEdge(t, created)
	if fields.ProjectID == nil || *fields.ProjectID != projectID {
		t.Errorf("project_id read back as %v, want %s — the endpoint that defines this kind",
			fields.ProjectID, projectID)
	}
}

// ONE hidden endpoint is enough, which is the rule the conjunction exists for and
// the case the both-hidden test above cannot reach.
//
// This file's premise is that an edge readable by someone who cannot read its
// ORGANIZATION discloses that organization's existence and its link to a person
// they can read. With both ends hidden, an edge would also be refused by a
// provider that only ever checked the person — so the both-hidden case passes
// against a weaker rule than the one claimed. Only this case separates a
// CONJUNCTION from a check of whichever endpoint happens to be looked at first.
func TestOneHiddenEndpointIsEnoughToHideTheEdge(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	admin := e.As(e.Rep1, nil, AdminPerms)
	stranger := e.As(e.Rep3, []ids.UUID{e.Team2}, relationshipReaderPerms())

	// The person is the STRANGER's own, so their row scope admits it. The
	// organization belongs to Rep2, so it does not.
	mine, err := e.People.CreatePerson(stranger, people.CreatePersonInput{FullName: "Rep3 Visible", Source: "manual"})
	if err != nil {
		t.Fatalf("Rep3 creating their own person: %v", err)
	}
	owner := ids.From[ids.UserKind](e.Rep2)
	hidden, err := e.People.CreateOrganization(admin, people.CreateOrganizationInput{
		DisplayName: "Rep2 Only", OwnerID: &owner, Source: "manual",
	})
	if err != nil {
		t.Fatalf("seeding the hidden organization: %v", err)
	}

	// Created by the admin, so the edge's existence owes nothing to the stranger.
	created, err := registry.Invoke(admin, "create_record", json.RawMessage(fmt.Sprintf(
		`{"record_type":"relationship","fields":{"kind":"employment","person_id":%q,"organization_id":%q,"source":"ui"}}`,
		mine.Id, hidden.Id)))
	if err != nil {
		t.Fatalf("create_record as admin over a mixed pair: %v", err)
	}
	edgeID, _ := wireEdge(t, created)

	// The stranger can read the person. They must still not reach the edge — and
	// the answer must be NOT FOUND, because a permission-denied would confirm it.
	if _, err := e.People.GetPerson(stranger, ids.From[ids.PersonKind](ids.UUID(mine.Id)), storekit.LiveOnly); err != nil {
		t.Fatalf("the stranger cannot read their OWN person, so this case proves nothing: %v", err)
	}
	for _, call := range []struct{ tool, args string }{
		{"update_record", fmt.Sprintf(`{"record_type":"relationship","id":%q,"fields":{"started_at":"2026-05-01"}}`, edgeID)},
		{"archive_record", fmt.Sprintf(`{"record_type":"relationship","id":%q}`, edgeID)},
	} {
		_, err := registry.Invoke(stranger, call.tool, json.RawMessage(call.args))
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Errorf("%s on an edge with ONE hidden endpoint = %v, want ErrNotFound — the visible person "+
				"is not enough, or the rule is a check of one end rather than a conjunction of all of them",
				call.tool, err)
		}
	}

	// And the edge is absent from the person-filtered LIST, which is how a list
	// hides: by omission, not by refusing.
	edges, _, err := e.People.ListRelationships(stranger, people.ListRelationshipsInput{
		PersonID: idPtr(ids.From[ids.PersonKind](ids.UUID(mine.Id))),
	})
	if err != nil {
		t.Fatalf("listing the stranger's own person's edges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("the list returned %d edge(s) for a person whose only edge points at an organization the "+
			"caller cannot read — the organization's existence and its link to this person both leak",
			len(edges))
	}
}

// idPtr takes the address of a typed id for the optional filter fields.
func idPtr[K ids.EntityKind](id ids.ID[K]) *ids.ID[K] { return &id }
