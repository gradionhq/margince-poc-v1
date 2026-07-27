// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project surface over the real wire: the create-read-update-advance-archive
// round trip, the refusals the contract names (a taken key, a close without a
// reason, a phase that only advanceProjectPhase may move), and the stakeholder
// roster's idempotent attach and archiving detach.
//
// The store's own write shape is proved in project_integration_test.go; this
// suite owns the transport — the decode, the input mapping, and the mapping of
// the store's typed errors onto contract codes.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type projectDTO struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Key            *string `json:"key"`
	OrganizationID string  `json:"organization_id"`
	Phase          string  `json:"phase"`
	Description    *string `json:"description"`
	ClosedReason   *string `json:"closed_reason"`
	ArchivedAt     *string `json:"archived_at"`
	Version        int     `json:"version"`
}

type projectListDTO struct {
	Data []projectDTO `json:"data"`
}

type relationshipDTO struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	PersonID  *string `json:"person_id"`
	ProjectID *string `json:"project_id"`
	Role      *string `json:"role"`
}

type relationshipListDTO struct {
	Data []relationshipDTO `json:"data"`
}

// projectProblem is the RFC 7807 body this surface answers on a refusal.
type projectProblem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		ExistingID string `json:"existing_id"`
	} `json:"details"`
}

// anchorOrg creates the company a project hangs from. A project has exactly
// one, and the contract requires it at create time.
func anchorOrg(t *testing.T, e *env, name string) string {
	t.Helper()
	var org struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{
		"display_name": name, "source": "manual",
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("POST /organizations → %d, want 201", status)
	}
	return org.ID
}

// anchorPerson creates someone to put on a project's roster.
func anchorPerson(t *testing.T, e *env, full string) string {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": full, "source": "manual",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("POST /people → %d, want 201", status)
	}
	return person.ID
}

// A project's whole life over HTTP: it opens in `initiative`, answers a read,
// appears in the list, takes a PATCH, moves phase only through /advance, and
// archives.
func TestProjectLifecycleOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Northwind")

	var created projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Warehouse rollout", "key": "WHR", "organization_id": org, "source": "manual",
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}
	if created.Phase != "initiative" {
		t.Fatalf("a new project opens in phase %q, want initiative", created.Phase)
	}
	if created.Key == nil || *created.Key != "WHR" {
		t.Fatalf("key = %v, want WHR", created.Key)
	}

	var read projectDTO
	if status := e.call(t, "GET", "/v1/projects/"+created.ID, nil, nil, &read); status != http.StatusOK {
		t.Fatalf("GET /projects/{id} → %d, want 200", status)
	}
	if read.ID != created.ID || read.Name != "Warehouse rollout" {
		t.Fatalf("read back %+v, want the project just created", read)
	}

	var listed projectListDTO
	if status := e.call(t, "GET", "/v1/projects?organization_id="+org, nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("GET /projects → %d, want 200", status)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != created.ID {
		t.Fatalf("list returned %d projects, want just the one on this company", len(listed.Data))
	}

	var patched projectDTO
	if status := e.call(t, "PATCH", "/v1/projects/"+created.ID, anyMap{
		"description": "Two sites, one cutover.",
	}, nil, &patched); status != http.StatusOK {
		t.Fatalf("PATCH /projects/{id} → %d, want 200", status)
	}
	if patched.Description == nil || *patched.Description != "Two sites, one cutover." {
		t.Fatalf("description = %v, want the patched text", patched.Description)
	}
	if patched.Phase != "initiative" {
		t.Fatalf("a PATCH moved the phase to %q — only /advance may do that", patched.Phase)
	}

	var advanced projectDTO
	if status := e.call(t, "POST", "/v1/projects/"+created.ID+"/advance", anyMap{
		"to_phase": "pursuing",
	}, nil, &advanced); status != http.StatusOK {
		t.Fatalf("POST /projects/{id}/advance → %d, want 200", status)
	}
	if advanced.Phase != "pursuing" {
		t.Fatalf("phase = %q after advancing, want pursuing", advanced.Phase)
	}

	if status := e.call(t, "DELETE", "/v1/projects/"+created.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE /projects/{id} → %d, want 204", status)
	}
	// Archiving is not deleting: the project stays readable by id, stamped
	// with when it was retired, so everything that pointed at it still
	// resolves. What it leaves is the live LIST.
	var archived projectDTO
	if status := e.call(t, "GET", "/v1/projects/"+created.ID, nil, nil, &archived); status != http.StatusOK {
		t.Fatalf("GET on an archived project → %d, want 200 — archive is not delete", status)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("an archived project came back with no archived_at")
	}
	var live projectListDTO
	if status := e.call(t, "GET", "/v1/projects?organization_id="+org, nil, nil, &live); status != http.StatusOK {
		t.Fatalf("GET /projects → %d, want 200", status)
	}
	if len(live.Data) != 0 {
		t.Fatalf("the live list still shows %d projects after the only one was archived", len(live.Data))
	}
}

// The key is the human handle, so a second live project may not take one that
// is already in use — and the refusal says which key, not a server fault.
func TestProjectKeyCollisionIsRefusedOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Contoso")

	body := anyMap{"name": "First", "key": "DUP", "organization_id": org, "source": "manual"}
	if status := e.call(t, "POST", "/v1/projects", body, nil, nil); status != http.StatusCreated {
		t.Fatalf("first POST /projects → %d, want 201", status)
	}

	var problem projectProblem
	status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Second", "key": "dup", "organization_id": org, "source": "manual",
	}, nil, &problem)
	if status != http.StatusConflict {
		t.Fatalf("a taken key → %d, want 409", status)
	}
	if problem.Code != "project_key_taken" {
		t.Fatalf("conflict code = %q, want project_key_taken", problem.Code)
	}
	// The holder is visible to this caller, so the refusal names it — that is
	// the whole point of probing before the write rather than after.
	if problem.Details.ExistingID == "" {
		t.Fatal("the conflict named no existing project, so a caller who collided cannot open it")
	}
}

// Closing is the one phase move that must say why: a closed project with no
// reason is a record nobody can interpret later.
func TestClosingAProjectOverHTTPRequiresAReason(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Initech")

	var project projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Migration", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}

	if status := e.call(t, "POST", "/v1/projects/"+project.ID+"/advance", anyMap{
		"to_phase": "closed",
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("closing with no reason → %d, want 422", status)
	}

	var closed projectDTO
	if status := e.call(t, "POST", "/v1/projects/"+project.ID+"/advance", anyMap{
		"to_phase": "closed", "reason": "delivered",
	}, nil, &closed); status != http.StatusOK {
		t.Fatalf("closing with a reason → %d, want 200", status)
	}
	if closed.Phase != "closed" {
		t.Fatalf("phase = %q, want closed", closed.Phase)
	}
}

// The contract requires a name and an anchor company; neither is something the
// server can invent, so both are refused at the edge rather than defaulted.
func TestCreateProjectRefusesAnIncompleteBodyOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Umbrella")

	// The shape of the refusal differs by cause and each is asserted exactly:
	// a missing or blank field is the 422 that names it, while an unknown
	// anchor company is a 404 because existence is hidden, not reported.
	for name, tc := range map[string]struct {
		body anyMap
		want int
	}{
		"no name":         {anyMap{"organization_id": org, "source": "manual"}, http.StatusUnprocessableEntity},
		"blank name":      {anyMap{"name": "   ", "organization_id": org, "source": "manual"}, http.StatusUnprocessableEntity},
		"no organization": {anyMap{"name": "Orphan", "source": "manual"}, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if status := e.call(t, "POST", "/v1/projects", tc.body, nil, nil); status != tc.want {
				t.Fatalf("%s → %d, want %d", name, status, tc.want)
			}
		})
	}

	// A blank name is refused on the way IN and on the way through: a rule
	// that only one verb carries is a rule the other verb erases.
	var project projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Named", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}
	if status := e.call(t, "PATCH", "/v1/projects/"+project.ID, anyMap{
		"name": "   ",
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH to a blank name → %d, want 422", status)
	}
}

// The roster is idempotent per person: attaching someone already attached is a
// role correction, never a second edge. That is the whole contract of a PUT
// here, and it is the property a uniqueness race must not break.
func TestProjectStakeholderRosterOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Stark Industries")
	person := anchorPerson(t, e, "Pepper Potts")

	var project projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Arc reactor", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}

	var edge relationshipDTO
	if status := e.call(t, "PUT", "/v1/projects/"+project.ID+"/stakeholders", anyMap{
		"person_id": person, "role": "sponsor",
	}, nil, &edge); status != http.StatusOK {
		t.Fatalf("PUT stakeholder → %d, want 200", status)
	}
	if edge.Role == nil || *edge.Role != "sponsor" {
		t.Fatalf("role = %v, want sponsor", edge.Role)
	}

	// The same person again: the role moves, the edge does not multiply.
	var recorrected relationshipDTO
	if status := e.call(t, "PUT", "/v1/projects/"+project.ID+"/stakeholders", anyMap{
		"person_id": person, "role": "project_lead",
	}, nil, &recorrected); status != http.StatusOK {
		t.Fatalf("re-attaching → %d, want 200", status)
	}
	if recorrected.ID != edge.ID {
		t.Fatalf("re-attaching created a second edge (%s then %s) — the PUT is not idempotent",
			edge.ID, recorrected.ID)
	}
	if recorrected.Role == nil || *recorrected.Role != "project_lead" {
		t.Fatalf("role = %v after correction, want project_lead", recorrected.Role)
	}

	var roster relationshipListDTO
	if status := e.call(t, "GET", "/v1/projects/"+project.ID+"/stakeholders", nil, nil, &roster); status != http.StatusOK {
		t.Fatalf("GET stakeholders → %d, want 200", status)
	}
	if len(roster.Data) != 1 {
		t.Fatalf("roster holds %d edges after two attaches of one person, want 1", len(roster.Data))
	}

	// Detaching archives the edge: the person's involvement stays on the
	// record, it simply stops being current.
	if status := e.call(t, "DELETE",
		fmt.Sprintf("/v1/projects/%s/stakeholders/%s", project.ID, person), nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE stakeholder → %d, want 204", status)
	}
	var afterDetach relationshipListDTO
	if status := e.call(t, "GET", "/v1/projects/"+project.ID+"/stakeholders", nil, nil, &afterDetach); status != http.StatusOK {
		t.Fatalf("GET stakeholders after detach → %d, want 200", status)
	}
	if len(afterDetach.Data) != 0 {
		t.Fatalf("roster still lists %d edges after a detach", len(afterDetach.Data))
	}
}

// A read seat may look at a project and may not change one. The ceiling is the
// seat, not the role, so it applies to every write on this surface.
func TestAReadSeatCannotWriteAProjectOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Cyberdyne")
	person := anchorPerson(t, e, "Miles Dyson")

	var project projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Skynet", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}

	e.setWorkspaceSeat(t, e.slug, "read")

	if status := e.call(t, "GET", "/v1/projects/"+project.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("a read seat reading a project → %d, want 200", status)
	}
	if status := e.call(t, "PATCH", "/v1/projects/"+project.ID, anyMap{
		"description": "not allowed",
	}, nil, nil); status != http.StatusForbidden {
		t.Fatalf("a read seat patching a project → %d, want 403", status)
	}
	if status := e.call(t, "POST", "/v1/projects/"+project.ID+"/advance", anyMap{
		"to_phase": "pursuing",
	}, nil, nil); status != http.StatusForbidden {
		t.Fatalf("a read seat advancing a phase → %d, want 403", status)
	}
	// The roster is part of the project's writable state, so both stakeholder
	// verbs sit behind the same ceiling. Detach is asserted because it is the
	// one that used to check only the relationship grant.
	if status := e.call(t, "PUT", "/v1/projects/"+project.ID+"/stakeholders", anyMap{
		"person_id": person, "role": "sponsor",
	}, nil, nil); status != http.StatusForbidden {
		t.Fatalf("a read seat attaching a stakeholder → %d, want 403", status)
	}
	if status := e.call(t, "DELETE",
		fmt.Sprintf("/v1/projects/%s/stakeholders/%s", project.ID, person), nil, nil, nil); status != http.StatusForbidden {
		t.Fatalf("a read seat detaching a stakeholder → %d, want 403", status)
	}
}

// An activity carries at most one project link, and the index enforcing that
// is PARTIAL — on activity_id alone — so relink's ON CONFLICT target cannot
// absorb it. Without this mapping the second project raises a raw uniqueness
// violation and the caller reads a 500 about nothing.
//
// This is also where the wording is decided. The same refusal answers a
// caller whose existing link is invisible to them, so it must name neither
// the project nor its id — only what happened and what to do instead.
func TestASecondProjectLinkIsRefusedWithoutNamingTheFirst(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Wayne Enterprises")

	var first, second projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Applied Sciences", "organization_id": org, "source": "manual",
	}, nil, &first); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Batcave retrofit", "organization_id": org, "source": "manual",
	}, nil, &second); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}

	var activity struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "note", "source": "manual",
		"links": []anyMap{{"entity_type": "project", "entity_id": first.ID}},
	}, nil, &activity); status != http.StatusCreated {
		t.Fatalf("POST /activities → %d, want 201", status)
	}

	// A second project without asking to replace: the index refuses, and the
	// caller must be told that in terms they can act on.
	var problem projectProblem
	status := e.call(t, "POST", "/v1/activities/"+activity.ID+"/relink", anyMap{
		"entity_type": "project", "entity_id": second.ID, "replace_existing_of_type": false,
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("a second project link → %d, want 422 — a raw uniqueness violation would be a 500", status)
	}
	if problem.Detail == "" {
		t.Fatal("the refusal says nothing about what to fix")
	}
	if !strings.Contains(problem.Detail, "replace_existing_of_type") {
		t.Errorf("the refusal does not say how to move the link instead: %q", problem.Detail)
	}
	// The same message answers a caller whose existing link is invisible, so
	// it may identify neither the project nor its id.
	for _, secret := range []string{"Applied Sciences", first.ID} {
		if strings.Contains(problem.Detail, secret) {
			t.Errorf("the refusal disclosed %q about the existing link: %q", secret, problem.Detail)
		}
	}

	// Asking to replace moves it, so the refusal really was about the rule
	// and not about the caller's authority.
	if status := e.call(t, "POST", "/v1/activities/"+activity.ID+"/relink", anyMap{
		"entity_type": "project", "entity_id": second.ID, "replace_existing_of_type": true,
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("relink with replace → %d, want 200", status)
	}
}
