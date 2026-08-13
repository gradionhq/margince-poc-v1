// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// An approval a human edits may be corrected, never re-aimed — proved through
// Decide rather than by calling assertSameCallIdentity directly, because the
// assertion is only worth anything if the decide path actually calls it. The
// decide path is also where the row, the diff hash and the audit evidence are
// all rewritten together in one transaction, which the unit suite cannot show.

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestDecideRefusesAnEditThatRepointsARestStagedCall stages a REST-shaped
// approval — an operation/path/body object, the shape compose.canonicalRESTCall
// writes (this package cannot import compose to call it directly: compose sits
// above modules in the dependency graph, so the literal below is hand-built
// rather than bound to the producer) — against one organization, then asks
// Decide to release an edit whose PATH names a different organization while
// its body is untouched. The content stayed identical — only the record
// moved — which is exactly the shape entityRefs cannot see and
// assertSameCallIdentity exists to catch.
func TestDecideRefusesAnEditThatRepointsARestStagedCall(t *testing.T) {
	e := setupStaging(t)
	org := e.organization(t)
	ctx := e.asHumanWith(decidesEverything())

	staged := json.RawMessage(`{"operation":"org_name_promotion","path":"/v1/organizations/` +
		org.String() + `","body":{"proposed_name":"Acme GmbH"}}`)
	id, err := e.svc.Stage(ctx, StageInput{
		Kind:           "org_name_promotion",
		ProposedChange: staged,
		DiffHash:       "hash-for-the-call-identity-guard",
		TargetType:     tableOrganization,
		TargetID:       org,
		Summary:        "Rename Acme to Acme GmbH?",
	})
	if err != nil {
		t.Fatalf("staging the REST-shaped approval: %v", err)
	}
	before, err := e.svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("reading the staged row: %v", err)
	}

	// The edited payload names a DIFFERENT organization in its path; the body
	// — the content a human is meant to correct — is byte-identical.
	other := ids.NewV7()
	edited := json.RawMessage(`{"operation":"org_name_promotion","path":"/v1/organizations/` +
		other.String() + `","body":{"proposed_name":"Acme GmbH"}}`)

	_, decideErr := e.svc.DecideEdited(ctx, id, edited)
	var retargeted *RetargetedEditError
	if !errors.As(decideErr, &retargeted) {
		t.Fatalf("DecideEdited(a path-retargeting edit) = %v, want *RetargetedEditError", decideErr)
	}

	// Any two of the three checks below pass for the wrong reason on their
	// own: the error type alone does not show the row was left alone, and an
	// untouched row alone does not show WHICH error refused the edit.
	after, err := e.svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("reading the row after the refused edit: %v", err)
	}
	if after.Status != "pending" {
		t.Errorf("the refused edit left the row %q; nothing should have been decided", after.Status)
	}
	if !bytes.Equal(before.ProposedChange, after.ProposedChange) {
		t.Errorf("proposed_change changed despite the refused edit:\n before = %s\n after  = %s",
			before.ProposedChange, after.ProposedChange)
	}
	if before.DiffHash != after.DiffHash {
		t.Errorf("diff_hash changed despite the refused edit: before = %q, after = %q", before.DiffHash, after.DiffHash)
	}
}
