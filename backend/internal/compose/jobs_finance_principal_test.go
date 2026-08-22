// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The finance sweep's principal must pass the gates the sweep actually takes.
//
// This exists because it did not, and nothing caught it. PR #1936 changed the
// sweep's principal from PrincipalSystem to PrincipalConnector so the audit row
// would stamp actor_type=connector — correct on its own terms, and it silently
// cost the sweep auth.Require's unconditional pass for a system principal. The
// zero Permissions that came with it then denied the base-currency read, so
// every finance_sync pass WITH WORK TO DO was discarded after three attempts
// and no invoice was ever mirrored.
//
// The integration suite stayed green throughout, because its fixture stubs both
// halves of the failure: it injects a literal base currency instead of
// identity.BaseCurrencyOf, and it binds a principal carrying admin grants that
// production has never had. A test at that level cannot see this class of bug.
//
// So this test asserts the one thing those cannot: that the principal the
// WORKER builds satisfies the gate the REAL read takes.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestTheFinanceSweepsPrincipalMayReadTheBaseCurrency(t *testing.T) {
	ctx := principal.WithActor(context.Background(), financeSweepPrincipal())

	if err := auth.Require(ctx, identity.BaseCurrency.Object(),
		principal.ActionRead); err != nil {
		t.Fatalf("the finance sweep may not read %s: %v\n"+
			"the mirror converts every ledger into the base currency as it writes, "+
			"so a sweep refused here is a sweep that mirrors nothing — and it fails "+
			"as three discarded jobs and an empty invoice list, not as anything a "+
			"reader would connect to a permission",
			identity.BaseCurrency.Object(), err)
	}
}

// The sweep stays a CONNECTOR, because the audit row it writes says so.
//
// finance's own TestTheMirrorsAuditRowsNameNoGrantTheSweepDoesNotHold pins
// actor_type=connector on every finance audit row, and audit_log is
// append-only. Answering the permission failure by going back to
// PrincipalSystem would trade one silent wrong for another: rows that read
// `system` beside a `connector:` actor_id, which cannot be corrected later.
func TestTheFinanceSweepStaysAConnector(t *testing.T) {
	p := financeSweepPrincipal()
	if p.Type != principal.PrincipalConnector {
		t.Errorf("the finance sweep binds %s, want connector — "+
			"its audit rows stamp actor_type from this and are append-only", p.Type)
	}
	if p.OnBehalfOf != (principal.Principal{}).OnBehalfOf {
		t.Errorf("the finance sweep names a granting human (%v); finance has no "+
			"connect flow, and a bare connector is what leaves the mirrored row "+
			"ownerless as storekit.OwnerOrActor expects", p.OnBehalfOf)
	}
}

// The grant stays MINIMAL. A connector on a schedule holds no role a human
// granted, so every object on it is one somebody has to justify. The sweep
// reads one setting; it writes finance rows through paths that take no gate.
func TestTheFinanceSweepGrantsOnlyWhatItReads(t *testing.T) {
	objects := financeSweepPrincipal().Permissions.Objects
	if len(objects) != 1 {
		t.Fatalf("the finance sweep holds %d object grants, want exactly 1: %v",
			len(objects), objects)
	}
	grant, ok := objects[identity.BaseCurrency.Object()]
	if !ok {
		t.Fatalf("the finance sweep's one grant is not on %s: %v",
			identity.BaseCurrency.Object(), objects)
	}
	if grant.Create || grant.Update || grant.Delete {
		t.Errorf("the finance sweep holds a WRITE grant (%+v) on a settings "+
			"object it only reads", grant)
	}
}
