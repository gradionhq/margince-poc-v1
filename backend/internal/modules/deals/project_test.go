// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"errors"
	"fmt"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func projectStr(s string) *string { return &s }

// A phase move must announce itself as a phase move. Emitting the generic
// update envelope instead would leave every consumer reconstructing a
// transition from a diff — and a consumer that guesses wrong about the
// prior phase is indistinguishable from one that read it.
func TestProjectPhaseChangedPayloadCarriesBothEnds(t *testing.T) {
	payload := projectPhaseChangedPayload("pursuing", AdvanceProjectPhaseInput{
		ToPhase: PhaseClosed,
		Reason:  projectStr("Delivered and handed over to support."),
	})

	if payload.EventType() != "project.phase_changed" {
		t.Errorf("event type = %q, want project.phase_changed", payload.EventType())
	}
	if payload.FromPhase == nil || *payload.FromPhase != "pursuing" {
		t.Errorf("from_phase = %v, want pursuing", payload.FromPhase)
	}
	if payload.ToPhase != PhaseClosed {
		t.Errorf("to_phase = %q, want %q", payload.ToPhase, PhaseClosed)
	}
	if payload.Reason == nil || *payload.Reason != "Delivered and handed over to support." {
		t.Errorf("reason = %v, want the supplied reason", payload.Reason)
	}
}

// An empty reason is the same as no reason: carrying "" onto the wire
// would let a consumer render a blank explanation as though one was given.
func TestProjectPhaseChangedPayloadDropsAnEmptyReason(t *testing.T) {
	payload := projectPhaseChangedPayload("initiative", AdvanceProjectPhaseInput{
		ToPhase: "pursuing",
		Reason:  projectStr(""),
	})
	if payload.Reason != nil {
		t.Errorf("reason = %v, want nil for an empty string", *payload.Reason)
	}
}

// The patch must carry only what the caller actually sent: a field the
// request omitted is not a field set to its zero value.
func TestProjectUpdatePatchOnlyCarriesSuppliedFields(t *testing.T) {
	current := crmcontracts.Project{Name: "ERP replacement", Key: projectStr("ERP-27")}

	empty := projectUpdatePatch(current, UpdateProjectInput{})
	if !empty.Empty() {
		t.Errorf("an empty input produced a patch: %v", empty.After())
	}

	named := projectUpdatePatch(current, UpdateProjectInput{Name: projectStr("ERP replacement 2027")})
	after := named.After()
	if len(after) != 1 {
		t.Fatalf("patch touched %d columns, want 1: %v", len(after), after)
	}
	if after["name"] != "ERP replacement 2027" {
		t.Errorf("patch name = %v, want the new name", after["name"])
	}
	if named.Before()["name"] != "ERP replacement" {
		t.Errorf("before-image name = %v, want the pre-update value", named.Before()["name"])
	}
}

// Phase is not in UpdateProjectInput at all — the type is the enforcement.
// This test states the rule so a future field addition has to argue with
// it rather than slip past: a phase move writes a history row, and update
// has no path that writes one.
func TestProjectUpdatePatchNeverSetsPhase(t *testing.T) {
	current := crmcontracts.Project{Name: "ERP replacement"}
	p := projectUpdatePatch(current, UpdateProjectInput{
		Name:        projectStr("renamed"),
		Description: projectStr("still the same body of work"),
	})
	if _, touched := p.After()["phase"]; touched {
		t.Error("the update patch set phase — a transition must go through AdvanceProjectPhase")
	}
	if _, touched := p.After()["closed_reason"]; touched {
		t.Error("the update patch set closed_reason — it belongs to the phase transition")
	}
}

// Each schema rule a caller can break must answer as itself, so the client
// is told which rule it broke rather than being handed a constraint name
// through the generic fallback.
func TestProjectCheckErrorNamesEachRule(t *testing.T) {
	for _, tc := range []struct {
		constraint string
		want       any
	}{
		{"project_key_shape", &ProjectKeyShapeError{}},
		{"project_closed_reason", &ClosedReasonRequiredError{}},
		{"project_dates", &ProjectDateRangeError{}},
	} {
		got := projectCheckError(tc.constraint, "")
		if got == nil {
			t.Fatalf("%s produced no error", tc.constraint)
		}
		if gotType, wantType := fmt.Sprintf("%T", got), fmt.Sprintf("%T", tc.want); gotType != wantType {
			t.Errorf("%s produced %s, want %s", tc.constraint, gotType, wantType)
		}
	}

	// An unmapped CHECK is still a business-rule breach, so it stays a
	// typed error naming the rule — never an opaque server fault.
	fallback := projectCheckError("project_some_future_rule", "")
	var unnamed *ProjectConstraintError
	if !errors.As(fallback, &unnamed) {
		t.Fatalf("an unmapped constraint produced %T, want *ProjectConstraintError", fallback)
	}
	if unnamed.Constraint != "project_some_future_rule" {
		t.Errorf("fallback named %q, want the constraint it was given", unnamed.Constraint)
	}
}

// A create request must not be able to smuggle a phase or a captured_by:
// both are the server's to decide, and the input type is where that is
// enforced.
func TestProjectCreateInputRequiresAName(t *testing.T) {
	_, err := projectCreateInput(crmcontracts.CreateProjectRequest{
		OrganizationId: openapi_types.UUID{},
		Source:         "ui",
	})
	var missing *RequiredFieldError
	if !errors.As(err, &missing) {
		t.Fatalf("a nameless project produced %v, want a required-field error", err)
	}
	if missing.Field != "name" {
		t.Errorf("required field = %q, want name", missing.Field)
	}
}
