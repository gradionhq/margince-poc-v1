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
	Code   string `json:"code"`
	Detail string `json:"detail"`
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
	if problem.Detail == "" {
		t.Fatal("the conflict says nothing about what to fix")
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

	for name, body := range map[string]anyMap{
		"no name":         {"organization_id": org, "source": "manual"},
		"blank name":      {"name": "   ", "organization_id": org, "source": "manual"},
		"no organization": {"name": "Orphan", "source": "manual"},
	} {
		t.Run(name, func(t *testing.T) {
			// The shape of the refusal differs by cause — a missing field is a 422,
			// an unknown anchor is a 404 that hides whether it exists — but no
			// incomplete body may produce a project.
			status := e.call(t, "POST", "/v1/projects", body, nil, nil)
			if status < 400 || status > 499 {
				t.Fatalf("%s → %d, want a 4xx refusal", name, status)
			}
		})
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
}
