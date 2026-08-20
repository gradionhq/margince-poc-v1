// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A field mask meeting the report engine: a rep whose role masks another
// team's deal amount must not read those amounts back out of a SUM, and the
// drill-through that explains the sum must show exactly the rows inside it.
// Masked rows are EXCLUDED — from the aggregate and the explanation alike —
// and the envelope carries the withheld count as excluded_by_permission, so
// a smaller total reads as governed rather than as missing data.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// maskedDealReader mints a rep on team1 whose role masks deal.amount_minor
// under the given condition — deal update is granted because the write arm is
// what lifts an outside_write_authority mask on the rep's own rows.
func (e *forecastEnv) maskedDealReader(cond principal.MaskCondition) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		TeamIDs: []ids.UUID{e.Team1},
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"deal":                  {Read: true, Update: true},
				"installation_settings": {Read: true},
			},
			RowScope:   principal.RowScopeTeam,
			FieldMasks: []principal.FieldMask{{Object: "deal", Field: "amount_minor", Condition: cond}},
		},
	})
}

func sumAmountBody() string {
	return `{"group_by":["currency"],"aggregates":[{"fn":"sum","field":"amount_minor","as":"total"}]}`
}

//craft:ignore naked-any the wire rows decode to map[string]any (decodeWire's UseNumber shape); this is the one coercion seam
func asInt(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("value %v (%T) is not a number", v, v)
	}
	i, err := n.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestAMaskedAmountNeverReachesAReportAggregate(t *testing.T) {
	e := setupForecast(t)
	own := int64(100_000)
	other := int64(250_000)
	mine := e.seedOpenDeal(t, "Mine", 20, &e.Rep1, &own, nil)
	theirs := e.seedOpenDeal(t, "Theirs", 20, &e.Rep3, &other, nil)

	rep := e.maskedDealReader(principal.MaskOutsideWriteAuthority)
	result := e.runReport(rep, t, "open-deals-per-company", sumAmountBody())
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %v, want the one EUR group", result.Rows)
	}
	if got := asInt(t, result.Rows[0]["total"]); got != own {
		t.Errorf("masked rep's sum = %d, want only their own %d — the masked amount leaked into the aggregate", got, own)
	}
	if result.ExcludedByPermission == nil || *result.ExcludedByPermission != 1 {
		t.Errorf("excluded_by_permission = %v, want 1 — the withheld row is counted, never silently dropped", result.ExcludedByPermission)
	}

	// The drill-through explains the SAME row set: the masked deal is absent
	// and the recomputed aggregate equals the number it explains.
	derivation := e.explainReport(rep, t, "open-deals-per-company", result.DerivationURL)
	if got := asInt(t, derivation.Aggregates["total"]); got != own {
		t.Errorf("derivation total = %d, want %d — the explanation out-saw the report", got, own)
	}
	if derivation.ExcludedByPermission == nil || *derivation.ExcludedByPermission != 1 {
		t.Errorf("derivation excluded_by_permission = %v, want 1", derivation.ExcludedByPermission)
	}
	for _, row := range derivation.Rows {
		if row["id"] == theirs.String() {
			t.Errorf("the drill-through printed the masked deal %s", theirs)
		}
	}
	if len(derivation.Rows) != 1 || derivation.Rows[0]["id"] != mine.String() {
		t.Errorf("drill-through rows = %v, want exactly the rep's own deal %s", derivation.Rows, mine)
	}

	// An unmasked admin reads the whole workspace, and the envelope says no
	// mask applied (null, not 0).
	all := e.runReport(e.Admin(), t, "open-deals-per-company", sumAmountBody())
	if got := asInt(t, all.Rows[0]["total"]); got != own+other {
		t.Errorf("admin sum = %d, want %d", got, own+other)
	}
	if all.ExcludedByPermission != nil {
		t.Errorf("admin excluded_by_permission = %v, want absent — no mask applied", *all.ExcludedByPermission)
	}
}

func TestAnAlwaysMaskEmptiesTheAggregateAndSaysSo(t *testing.T) {
	e := setupForecast(t)
	own := int64(100_000)
	other := int64(250_000)
	e.seedOpenDeal(t, "Mine", 20, &e.Rep1, &own, nil)
	e.seedOpenDeal(t, "Theirs", 20, &e.Rep3, &other, nil)

	rep := e.maskedDealReader(principal.MaskAlways)
	result := e.runReport(rep, t, "open-deals-per-company", sumAmountBody())
	if len(result.Rows) != 0 {
		t.Errorf("rows = %v, want none — every row's amount is withheld", result.Rows)
	}
	if result.ExcludedByPermission == nil || *result.ExcludedByPermission != 2 {
		t.Errorf("excluded_by_permission = %v, want 2 — both visible rows withheld", result.ExcludedByPermission)
	}
}

// Write authority is the object's update verb AND the row arm. A rep whose
// role lost deal.update still OWNS rows; the mask must not lift on them —
// otherwise revoking the verb would WIDEN what the rep reads out of a sum.
func TestARevokedUpdateVerbKeepsTheMaskOnTheRepsOwnRows(t *testing.T) {
	e := setupForecast(t)
	own := int64(100_000)
	e.seedOpenDeal(t, "Mine", 20, &e.Rep1, &own, nil)

	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	rep := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		TeamIDs: []ids.UUID{e.Team1},
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"deal":                  {Read: true},
				"installation_settings": {Read: true},
			},
			RowScope:   principal.RowScopeTeam,
			FieldMasks: []principal.FieldMask{{Object: "deal", Field: "amount_minor", Condition: principal.MaskOutsideWriteAuthority}},
		},
	})
	result := e.runReport(rep, t, "open-deals-per-company", sumAmountBody())
	if len(result.Rows) != 0 {
		t.Errorf("rows = %v, want none — without the update verb the rep holds write authority over no row", result.Rows)
	}
	if result.ExcludedByPermission == nil || *result.ExcludedByPermission != 1 {
		t.Errorf("excluded_by_permission = %v, want 1", result.ExcludedByPermission)
	}
}
