// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// computedFieldsVisible is a pure in-memory check (no DB round trip):
// it reads the acting principal's already-merged Permissions, the
// divergence from poc-1's per-call role reload the plan calls out.
func TestComputedFieldsVisible_GrantedRole(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{"computed_field": {Read: true}},
		},
	})
	if !computedFieldsVisible(ctx) {
		t.Fatal("want visible for a role granting computed_field:read")
	}
}

func TestComputedFieldsVisible_UngatedRoleDenied(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman,
		Permissions: principal.Permissions{
			// A role missing the computed_field grant entirely — the
			// zero-value ObjectGrant denies, matching Permissions.Allows.
			Objects: map[string]principal.ObjectGrant{"organization": {Read: true}},
		},
	})
	if computedFieldsVisible(ctx) {
		t.Fatal("want NOT visible when the role's policy carries no computed_field grant")
	}
}

func TestComputedFieldsVisible_NoActorBoundDenied(t *testing.T) {
	if computedFieldsVisible(context.Background()) {
		t.Fatal("want NOT visible with no actor bound (fail-closed)")
	}
}

func TestComputedFieldsVisible_SystemPrincipalTrusted(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{Type: principal.PrincipalSystem})
	if !computedFieldsVisible(ctx) {
		t.Fatal("want the system principal trusted by construction, matching auth.Require's own carve-out")
	}
}

// organizationComputedFields is the pure 5-row assembler: exactly one
// computable row (open_pipeline, fed by the view read), four floors —
// weighted_pipeline honestly named as served by the hierarchy-rollup
// read (poc-v1 HAS that read, unlike poc-1), the other three genuinely
// not_yet_built.
func TestOrganizationComputedFields_FiveRowsExactShape(t *testing.T) {
	minor := int64(125000)
	rows := organizationComputedFields(&minor, 1)
	if len(rows) != 5 {
		t.Fatalf("want exactly 5 display rows, got %d", len(rows))
	}

	open := rows[0]
	if open.Key != "open_pipeline" || open.Kind != crmcontracts.ComputedFieldKindCurrencyMinor {
		t.Fatalf("row[0] = %+v, want open_pipeline/currency_minor", open)
	}
	if !open.Computable || open.Reason != nil {
		t.Fatalf("open_pipeline must be computable with no floor reason, got %+v", open)
	}
	if open.ValueMinor == nil || *open.ValueMinor != minor {
		t.Fatalf("open_pipeline.value_minor = %v, want %d", open.ValueMinor, minor)
	}
	if open.FormulaSql == "" {
		t.Fatal("open_pipeline must carry a non-empty formula_sql (the view definition)")
	}

	byKey := map[string]crmcontracts.ComputedField{}
	for _, r := range rows[1:] {
		byKey[r.Key] = r
	}
	wantFloors := map[string]string{
		"weighted_pipeline":     "served_by_hierarchy_rollup",
		"customer_age":          "not_yet_built",
		"net_revenue_retention": "not_yet_built",
		"blended_gross_margin":  "not_yet_built",
	}
	for key, wantReason := range wantFloors {
		row, ok := byKey[key]
		if !ok {
			t.Fatalf("missing floor row %q", key)
		}
		if row.Computable {
			t.Fatalf("%s must be computable=false", key)
		}
		if row.Reason == nil || *row.Reason != wantReason {
			t.Fatalf("%s.reason = %v, want %q", key, row.Reason, wantReason)
		}
		if row.ValueMinor != nil || row.Value != nil {
			t.Fatalf("%s must carry no value while computable=false", key)
		}
		if row.Dependencies == nil {
			t.Fatalf("%s.dependencies must be a real (possibly empty) array, never nil", key)
		}
	}
}

// The no-open-deals case (nil minorBase and dealCount==0, meaning "no
// view row at all") floors open_pipeline to a real 0 — the
// poc-1-tested behaviour: a record-page tile has no way to render
// "unknown", so 0 is the honest lower bound of what CAN be priced.
func TestOrganizationComputedFields_NoOpenDeals_FloorsToZero(t *testing.T) {
	rows := organizationComputedFields(nil, 0)
	if rows[0].ValueMinor == nil || *rows[0].ValueMinor != 0 {
		t.Fatalf("open_pipeline.value_minor = %v, want 0", rows[0].ValueMinor)
	}
	if !rows[0].Computable {
		t.Fatal("the zero floor is still computable=true — a real (zero) sum, not a missing one")
	}
	if rows[0].Reason != nil {
		t.Fatalf("open_pipeline.reason = %v, want nil for the genuine-zero case", rows[0].Reason)
	}
}

// The OTHER honest "not computable yet" state 0065 documents: the view
// row EXISTS (open deals reference this org, dealCount > 0) but its
// aggregate is itself NULL because every one of those deals is still
// missing fx_rate_to_base. Flooring this to 0 would be dishonest — a
// non-zero weighted_pipeline sitting beside a fabricated zero — so it
// must floor to computable:false, reason:"awaiting_fx" instead, with no
// value_minor on the wire.
func TestOrganizationComputedFields_NullAggregateWithOpenDeals_AwaitingFX(t *testing.T) {
	rows := organizationComputedFields(nil, 2)
	open := rows[0]
	if open.Computable {
		t.Fatalf("open_pipeline must be computable=false when the aggregate is NULL but open deals exist, got %+v", open)
	}
	if open.Reason == nil || *open.Reason != awaitingFXReason {
		t.Fatalf("open_pipeline.reason = %v, want %q", open.Reason, awaitingFXReason)
	}
	if open.ValueMinor != nil {
		t.Fatalf("open_pipeline.value_minor = %v, want nil (awaiting_fx carries no value)", open.ValueMinor)
	}
	if open.FormulaSql == "" {
		t.Fatal("open_pipeline.formula_sql must stay populated: the formula exists, only its FX input doesn't yet")
	}
}
