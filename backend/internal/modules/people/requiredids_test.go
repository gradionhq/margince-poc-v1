// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// A required body id the caller omitted must be named as a missing argument, not
// discovered as a missing row.
//
// oapi-codegen renders a REQUIRED body id as a non-pointer UUID and
// encoding/json leaves an absent key at the zero value with no error, so the
// zero UUID used to travel to a lookup, match nothing, and answer a bare
// not-found — telling the caller a record they never mentioned does not exist.
//
// The guard sits at the store entry point, which is the door every transport
// comes through, and it runs BEFORE any authority check or query — which is why
// these probes need no database and no actor: a store over a nil pool never
// reaches one.
//
// The refusal's SHAPE (422, the field named, the stable `required` code, and that
// it classifies on both surfaces) is proven once in
// platform/httperr/requirebodyid_test.go. What is left here is the only question
// this package can answer: is the guard actually called for my body.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// assertNamesTheOmittedID asserts the observable property: the caller's mistake,
// with the wire field they omitted named.
func assertNamesTheOmittedID(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("an absent %s was accepted, so the caller is sent looking for a record it never named", field)
	}
	fault, ok := httperr.Classify(err)
	if !ok {
		t.Fatalf("an absent %s answered %v, which is outside the taxonomy — a surface would report the "+
			"caller's own omission as an internal server fault", field, err)
	}
	if fault.Status != http.StatusUnprocessableEntity {
		t.Errorf("an absent %s answered status %d, want 422 (404 is the defect this closes)", field, fault.Status)
	}
	for _, refusal := range fault.Fields {
		if refusal.Field == field {
			return
		}
	}
	t.Errorf("the refusal for an absent %s names %+v, want the wire field %q", field, fault.Fields, field)
}

func TestAnOmittedMergeTargetOrStakeholderIsNamed(t *testing.T) {
	// MergePersonJSONBody.target_id, MergeOrganizationJSONBody.target_id and
	// SetProjectStakeholderRequest.person_id.
	//
	// The merge pair is the sharp case: the self-merge guard next to it does NOT
	// catch an omitted target, because a real source id never equals the zero
	// one — so the zero id reached the pair lock and answered not-found for a
	// survivor nobody named.
	store := NewStore(nil)
	ctx := context.Background()

	_, err := store.MergePerson(ctx, ids.New[ids.PersonKind](), ids.PersonID{})
	assertNamesTheOmittedID(t, err, "target_id")

	_, err = store.MergeOrganization(ctx, ids.New[ids.OrganizationKind](), ids.OrganizationID{})
	assertNamesTheOmittedID(t, err, "target_id")

	_, err = store.SetProjectStakeholder(ctx, SetProjectStakeholderInput{
		ProjectID: ids.New[ids.ProjectKind](), Role: "sponsor",
	})
	assertNamesTheOmittedID(t, err, "person_id")
}
