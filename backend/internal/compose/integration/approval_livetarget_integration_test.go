// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A staged approval is a decision someone still owes about a record. When that
// record is not there — never was, or archived since — nothing is owed: the
// release would refuse, and the row's summary and proposed change describe a
// record no surface serves any more. So the target probe never skips existence,
// for any seat.
//
// The seat that makes this a gate rather than a happy path is the UNBOUNDED one.
// The row-scope clause is what carries the target id into a query at all, and for
// a table with no capture privacy an all-scope human's clause renders EMPTY — so
// a probe that reads an empty clause as "admitted" asks nothing, and the target's
// absence stops being observable for precisely the seats that can act on the
// most. Art. 17 is the same hole with teeth: erasure anonymizes a record IN
// PLACE, stamping archived_at while leaving owner_id alone, so a scope-only probe
// answers "still yours" for a tombstone.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// everyGrantAllScope is one seat holding all four CRUD grants on each named
// object at the widest row scope — the seat every decision grant admits and no
// row scope narrows, so nothing but the target probe can refuse it.
func everyGrantAllScope(objects ...string) principal.Permissions {
	grants := make(map[string]principal.ObjectGrant, len(objects))
	for _, object := range objects {
		grants[object] = principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
	}
	return principal.Permissions{RoleKeys: []string{"custom"}, Objects: grants, RowScope: principal.RowScopeAll}
}

// A target id no row carries is fail-closed for EVERY classified target type, by
// one of two mechanisms — and the gate accepts either, because both are the same
// answer to a human: the staging is refused before a row is minted (the version
// pin has no row to read), or the row is minted and no seat can decide it.
//
// Derived over the classification rather than over the arms someone listed: every
// arm answers this identically, so a target type enrolled later inherits the case
// instead of waiting for the list to be extended.
func TestNoStagedApprovalIsDecidableAgainstATargetThatIsNotThere(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)

	classified := approvals.ClassifiedTargetTypes()
	if len(classified) == 0 {
		t.Fatal("the approvals module classifies no target type, so this gate covers nothing")
	}
	seat := e.As(e.Rep1, []ids.UUID{e.Team1}, everyGrantAllScope(classified...))

	for _, targetType := range classified {
		t.Run(targetType, func(t *testing.T) {
			approvalID, err := svc.Stage(e.AgentCtx(), approvals.StageInput{
				Kind: "archive_record", ProposedChange: json.RawMessage(`{}`),
				DiffHash: "h-" + ids.NewV7().String(), TargetType: targetType,
				TargetID: ids.NewV7(), Summary: "staged against nothing",
			})
			if errors.Is(err, apperrors.ErrNotFound) {
				// Refused a step earlier: this type is version-pinned, so staging
				// reads the target's version and finds no row to read it from.
				return
			}
			if err != nil {
				t.Fatalf("stage against a missing %s → %v, want either a not-found refusal or an undecidable row",
					targetType, err)
			}
			assertCannotDecideStagedApproval(seat, t, svc,
				"an all-scope seat holding every "+targetType+" grant, against an id no row carries", approvalID)
		})
	}
}

// The reachable half of the same rule: a target that WAS there when the change
// was staged and is gone by the time a human opens their inbox. Archive is the
// delete on these tables, so the staged effect could never land and the row's
// detail describes a record the read path now refuses.
//
// This is a BEHAVIOUR CHANGE, and the positive control is what makes it one: the
// same seat and the same approval, decidable while the target is live and absent
// once it is not. Without that first assertion the second could pass on a seat
// that was never admitted at all.
//
// Both probe classes the change touches are covered — a row-scoped target whose
// all-scope clause is empty (deal), and an inherited-scope target whose clause
// never filtered archived rows (a subject-less signal, workspace-shared like an
// unlinked note).
func TestAStagedApprovalStopsBeingDecidableOnceItsTargetIsArchived(t *testing.T) {
	e := Setup(t)
	svc := approvals.NewService(e.Pool)
	pipeline, open, _ := DealFixture(t, e)

	deal := e.SeedDeal(t, "Renewal", pipeline, open, &e.Rep1)
	signal := seedSubjectlessSignal(t, e, "Renewal has gone quiet")

	for _, c := range []struct {
		targetType string
		target     ids.UUID
		archive    string
	}{
		{"deal", deal, `UPDATE deal SET archived_at = now() WHERE id = $1`},
		{"signal", signal, `UPDATE signal SET archived_at = now() WHERE id = $1`},
	} {
		t.Run(c.targetType, func(t *testing.T) {
			approvalID := stageFor(t, svc, e, "archive_record", c.targetType, c.target)
			seat := e.As(e.Rep1, []ids.UUID{e.Team1}, everyGrantAllScope(c.targetType))

			if _, err := svc.Get(seat, approvalID); err != nil {
				t.Fatalf("Get while the target %s is LIVE → %v, want ok — without this the case below could "+
					"pass on a seat that was never admitted", c.targetType, err)
			}

			e.WsExec(t, c.archive, c.target)

			assertCannotDecideStagedApproval(seat, t, svc,
				"a seat whose staged "+c.targetType+" target was archived after staging", approvalID)
		})
	}
}

// seedSubjectlessSignal inserts a signal with no subject entity — the
// workspace-shared shape, visible to every row-scope tier, so the archive below
// is the only thing that can change the answer. It is inserted directly because
// a signal's own store mints them from resolution runs; the fixture needs one
// row, not a pipeline.
func seedSubjectlessSignal(t *testing.T, e *Env, summary string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	e.WsExec(t, `INSERT INTO signal (id, workspace_id, kind, resolution_state, summary, source, captured_by)
		VALUES ($1, $2, 'risk', 'unresolved', $3, 'manual', 'human:fixture')`, id, e.WS, summary)
	return id
}
