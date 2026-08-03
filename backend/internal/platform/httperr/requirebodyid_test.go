// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// The SHAPE of a missing-required-id refusal, proven once.
//
// This is the half that used to be re-proven in every module: that the refusal
// is a 422, that it names the wire field, that its code is stable, and that it
// classifies — which is what decides whether the MCP tool surface reads a
// sentence an agent can act on or "the tool failed for an internal reason".
// There is one implementation now, so there is one place to assert it, and each
// module's own probe is left with the question only it can answer: is the guard
// actually called for my body.

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestRequireBodyIDNamesTheFieldAndClassifiesAsTheCallersMistake(t *testing.T) {
	err := RequireBodyID("to_stage_id", ids.UUID{})
	if err == nil {
		t.Fatal("a zero id was accepted — an absent required key decodes to exactly this value")
	}

	fault, ok := Classify(err)
	if !ok {
		t.Fatalf("err = %v is outside the taxonomy, so a surface reporting it would call the caller's "+
			"own omission an internal server fault", err)
	}
	if fault.Status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 — the body is well-formed and one required value is missing",
			fault.Status)
	}
	if len(fault.Fields) != 1 {
		t.Fatalf("fields = %+v, want exactly one naming the omitted id", fault.Fields)
	}
	if got := fault.Fields[0].Field; got != "to_stage_id" {
		t.Errorf("field = %q, want the wire path to_stage_id — a caller branches on this", got)
	}
	if got := fault.Fields[0].Code; got != "required" {
		t.Errorf("code = %q, want the stable machine code `required`", got)
	}
}

// The refusal must not be a not-found, because that is the answer it exists to
// replace: a caller told "not found" for an id they never sent goes looking for a
// record instead of at their own request.
func TestRequireBodyIDIsNotANotFound(t *testing.T) {
	fault, _ := Classify(RequireBodyID("purpose_id", ids.UUID{}))
	if fault.Status == http.StatusNotFound {
		t.Error("an omitted required id answered 404 — the defect, not the fix")
	}
}

func TestRequireBodyIDPassesAnIDThatIsPresent(t *testing.T) {
	// The control. A guard that refused everything would satisfy the assertions
	// above and break every write on the surface.
	if err := RequireBodyID("record_id", ids.NewV7()); err != nil {
		t.Errorf("a supplied id was refused: %v", err)
	}
}

// Existence-hiding is the thing this class of fix must not break: making a
// refusal clearer must not turn it into a probe for whether a record exists. A
// SUPPLIED id that names nothing visible still has to reach the lookup and come
// back as a 404, so the guard must be silent about any id it is given.
func TestRequireBodyIDSaysNothingAboutWhetherAnIDExists(t *testing.T) {
	for _, id := range []ids.UUID{ids.NewV7(), ids.NewV7()} {
		if err := RequireBodyID("record_id", id); err != nil {
			t.Fatalf("the guard refused a well-formed id %s (%v) — it would then be answering a "+
				"question about existence, which belongs to the row-scoped lookup", id, err)
		}
	}
}

// The sentence a caller reads, and it has to name the field: both surfaces
// render Detail, and the MCP one renders ONLY that — a structured fields array
// an agent never sees is not an explanation.
func TestRequireBodyIDSaysWhichFieldInTheDetailBothSurfacesRender(t *testing.T) {
	fault, ok := Classify(RequireBodyID("subject_id", ids.UUID{}))
	if !ok {
		t.Fatal("the refusal does not classify")
	}
	if want := "subject_id is required"; fault.Detail != want {
		t.Errorf("detail = %q, want %q", fault.Detail, want)
	}
	// And the verdict survives WRAPPING, because a module that adds context with
	// %w must not have to know which concrete type carries it.
	wrapped := fmt.Errorf("mapping the advance body: %w", RequireBodyID("to_stage_id", ids.UUID{}))
	var detailed *DetailedError
	if !errors.As(wrapped, &detailed) {
		t.Fatal("the refusal does not survive wrapping, so a module that adds context loses the verdict")
	}
	if wrappedFault, ok := Classify(wrapped); !ok || len(wrappedFault.Fields) != 1 {
		t.Errorf("a wrapped refusal classified as (%+v, %v), want the field list intact", wrappedFault, ok)
	}
}
