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
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
		// A validation refusal names the field and the rule that fired, so a
		// client can point at the input rather than re-reading prose.
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	} `json:"details"`
}

// fieldCode returns the rule a validation refusal names, so a test asserts on
// the code a client switches on rather than on the sentence.
func (p projectProblem) fieldCode() string {
	if len(p.Details.Errors) == 0 {
		return ""
	}
	return p.Details.Errors[0].Code
}

// fieldName returns the input a validation refusal points at.
func (p projectProblem) fieldName() string {
	if len(p.Details.Errors) == 0 {
		return ""
	}
	return p.Details.Errors[0].Field
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
	// a missing or blank field is the 422 that names it, while an anchor company
	// that was NAMED and cannot be seen is a 404 because existence is hidden,
	// not reported.
	//
	// The two organization cases are the distinction that matters, and it is not
	// cosmetic: existence-hiding protects a row the caller POINTED AT, and there is
	// no row to protect when no id was supplied. Answering 404 to an omitted id
	// sends the caller looking for a company it never named.
	for name, tc := range map[string]struct {
		body anyMap
		want int
	}{
		"no name":    {anyMap{"organization_id": org, "source": "manual"}, http.StatusUnprocessableEntity},
		"blank name": {anyMap{"name": "   ", "organization_id": org, "source": "manual"}, http.StatusUnprocessableEntity},
		"organization_id omitted": {
			anyMap{"name": "Orphan", "source": "manual"},
			http.StatusUnprocessableEntity,
		},
		"organization_id names nothing visible": {
			anyMap{"name": "Orphan", "organization_id": ids.NewV7().String(), "source": "manual"},
			http.StatusNotFound,
		},
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

// Every refusal this surface can answer is a promise in the contract: a code
// the client switches on and a field it can point at. A rule that fires as an
// untyped 500, or under the wrong code, is a promise broken — so each arm is
// provoked here through the real schema rule that raises it.
func TestEachProjectRefusalAnswersItsOwnCode(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Refusal GmbH")

	var project projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Baseline", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}

	t.Run("a key that is not key-shaped", func(t *testing.T) {
		// Letter-led on purpose: a bare number would match dates, amounts and
		// order numbers in an inbound subject line.
		var problem projectProblem
		if status := e.call(t, "POST", "/v1/projects", anyMap{
			"name": "Numeric", "key": "2026", "organization_id": org, "source": "manual",
		}, nil, &problem); status != http.StatusUnprocessableEntity {
			t.Fatalf("a numeric key → %d, want 422", status)
		}
		if problem.fieldCode() != "invalid_key" {
			t.Errorf("rule = %q, want invalid_key", problem.fieldCode())
		}
		if problem.fieldName() != "key" {
			t.Errorf("refusal points at %q, want the key input", problem.fieldName())
		}
	})

	t.Run("an end date before the start", func(t *testing.T) {
		var problem projectProblem
		if status := e.call(t, "PATCH", "/v1/projects/"+project.ID, anyMap{
			"started_at": "2026-06-01", "ended_at": "2026-01-01",
		}, nil, &problem); status != http.StatusUnprocessableEntity {
			t.Fatalf("an inverted date range → %d, want 422", status)
		}
		if problem.fieldCode() != "invalid_date_range" {
			t.Errorf("rule = %q, want invalid_date_range", problem.fieldCode())
		}
		if problem.fieldName() != "ended_at" {
			t.Errorf("refusal points at %q, want the ended_at input", problem.fieldName())
		}
	})

	t.Run("closing without a reason", func(t *testing.T) {
		var problem projectProblem
		if status := e.call(t, "POST", "/v1/projects/"+project.ID+"/advance", anyMap{
			"to_phase": "closed",
		}, nil, &problem); status != http.StatusUnprocessableEntity {
			t.Fatalf("closing with no reason → %d, want 422", status)
		}
		if problem.fieldCode() != "closed_reason_required" {
			t.Errorf("rule = %q, want closed_reason_required", problem.fieldCode())
		}
		if problem.fieldName() != "reason" {
			t.Errorf("refusal points at %q, want the reason input", problem.fieldName())
		}
	})

	t.Run("a deal anchored to another company's project", func(t *testing.T) {
		// The rule spans two rows, so it lives in a constraint trigger — the
		// only place a cross-row rule can be enforced — and must still read as
		// a 422 about the rule rather than a server fault.
		other := anchorOrg(t, e, "Elsewhere AG")
		var elsewhere projectDTO
		if status := e.call(t, "POST", "/v1/projects", anyMap{
			"name": "Their work", "organization_id": other, "source": "manual",
		}, nil, &elsewhere); status != http.StatusCreated {
			t.Fatalf("POST /projects → %d, want 201", status)
		}
		var pipelines struct {
			Data []struct {
				ID     string `json:"id"`
				Stages []struct {
					ID string `json:"id"`
				} `json:"stages"`
			} `json:"data"`
		}
		// Not a skip: bootstrapping seeds the default pipeline, so an empty
		// list means the fixture broke, and skipping here would retire the
		// only check on a rule that lives in a constraint trigger.
		if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
			t.Fatalf("GET /pipelines → %d, want 200", status)
		}
		if len(pipelines.Data) == 0 || len(pipelines.Data[0].Stages) == 0 {
			t.Fatal("the bootstrapped workspace has no pipeline with stages — the fixture no longer seeds one")
		}
		var problem projectProblem
		status := e.call(t, "POST", "/v1/deals", anyMap{
			"name": "Mismatched", "organization_id": org, "project_id": elsewhere.ID,
			"pipeline_id": pipelines.Data[0].ID, "stage_id": pipelines.Data[0].Stages[0].ID,
			"source": "manual",
		}, nil, &problem)
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("a deal and project on different companies → %d, want 422", status)
		}
		if problem.fieldCode() != "project_organization_mismatch" {
			t.Errorf("rule = %q, want project_organization_mismatch", problem.fieldCode())
		}
		if problem.fieldName() != "project_id" {
			t.Errorf("refusal points at %q, want the project_id input", problem.fieldName())
		}
	})
}

// If-Match is the caller's claim about what they are editing. A stale version
// must not silently win: the write is refused so the client re-reads rather
// than overwriting someone else's change.
func TestAStaleVersionCannotOverwriteAProject(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org := anchorOrg(t, e, "Skew GmbH")

	var project projectDTO
	if status := e.call(t, "POST", "/v1/projects", anyMap{
		"name": "Contended", "organization_id": org, "source": "manual",
	}, nil, &project); status != http.StatusCreated {
		t.Fatalf("POST /projects → %d, want 201", status)
	}
	stale := strconv.Itoa(project.Version)

	// Someone else moves first.
	if status := e.call(t, "PATCH", "/v1/projects/"+project.ID, anyMap{
		"description": "first writer",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("first PATCH → %d, want 200", status)
	}
	// The second writer still holds the version from before that.
	// 409 with code version_skew is what the contract names for this — a
	// different 4xx would be a different promise, so accepting either would
	// let the surface change contracts without anything noticing.
	var problem projectProblem
	if status := e.call(t, "PATCH", "/v1/projects/"+project.ID, anyMap{
		"description": "second writer",
	}, map[string]string{"If-Match": stale}, &problem); status != http.StatusConflict {
		t.Fatalf("a stale If-Match → %d, want 409", status)
	}
	if problem.Code != "version_skew" {
		t.Errorf("refusal code = %q, want version_skew", problem.Code)
	}
	if n := e.callDescription(t, project.ID); n != "first writer" {
		t.Fatalf("description = %q — the stale write landed anyway", n)
	}
}

// callDescription reads one project's description back over the wire.
func (e *env) callDescription(t *testing.T, id string) string {
	t.Helper()
	var out projectDTO
	if status := e.call(t, "GET", "/v1/projects/"+id, nil, nil, &out); status != http.StatusOK {
		t.Fatalf("GET /projects/{id} → %d, want 200", status)
	}
	if out.Description == nil {
		return ""
	}
	return *out.Description
}
