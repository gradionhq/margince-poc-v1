// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The installation-settings surface and the mechanism under it, end to end
// (ADR-0090/A135, ADR-0085 §7).
//
// Three things need a real database to prove, and none of them can be shown
// with a unit test:
//
//   - an UNSET setting resolves to its registered default, which is the state
//     every installation is in before anyone touches it;
//   - a change commits the row AND its audit entry together, while an
//     idempotent re-assert writes neither;
//   - the base currency stops being changeable once a deal has frozen a
//     conversion rate against it, and says how many did.
//
// The last one is the one that fails open: an unwired freeze probe simply
// leaves the currency editable, so only a test that CREATES the converted deal
// can tell a working guard from an absent one.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// installationSettingsCtx builds a human principal in the env workspace with a
// specific installation_settings grant.
func (e *SearchEnv) installationSettingsCtx(grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"installation_settings": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

func installationAuditCount(t *testing.T, e *SearchEnv) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE entity_type = 'installation_settings'`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInstallationSettingsReadWriteAndGate(t *testing.T) {
	e := SetupSearch(t)
	store := identity.NewInstallationSettings(e.Pool, compose.NewSettingsStore(e.Pool))

	admin := e.installationSettingsCtx(principal.ObjectGrant{Read: true, Update: true})
	rep := e.installationSettingsCtx(principal.ObjectGrant{Read: true})
	none := e.installationSettingsCtx(principal.ObjectGrant{})

	// This harness inserts its workspace directly rather than bootstrapping,
	// so no setting row exists: the read must answer with the REGISTERED
	// DEFAULTS. That is the state every installation is in before anyone
	// writes a setting, and the path that lets a new setting ship without
	// backfilling anything. (That bootstrap seeds real values instead is
	// proven where bootstrap actually runs — installation_integration_test.go.)
	got, err := store.GetInstallation(rep)
	if err != nil {
		t.Fatalf("a read grant could not read the installation settings: %v", err)
	}
	if got.Timezone != "UTC" || got.BaseCurrency != "EUR" {
		t.Errorf("unset settings read as timezone=%q currency=%q, want the registered defaults UTC/EUR",
			got.Timezone, got.BaseCurrency)
	}
	if got.BaseCurrencyLocked {
		t.Error("the base currency is locked on a fixture with no converted deals")
	}

	// No grant reads nothing.
	if _, err := store.GetInstallation(none); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("read without a grant returned %v, want ErrPermissionDenied", err)
	}
	// A read grant is not a write grant.
	newName := "Renamed GmbH"
	if _, err := store.UpdateInstallation(rep, &newName, nil, nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("write with only a read grant returned %v, want ErrPermissionDenied", err)
	}

	before := installationAuditCount(t, e)

	// A real change commits the row and its audit entry together.
	updated, err := store.UpdateInstallation(admin, &newName, nil, nil)
	if err != nil {
		t.Fatalf("renaming the installation: %v", err)
	}
	if updated.Name != newName {
		t.Errorf("name = %q after the write, want %q", updated.Name, newName)
	}
	if n := installationAuditCount(t, e); n != before+1 {
		t.Errorf("audit rows = %d, want %d — a settings change is audited", n, before+1)
	}

	// Re-asserting the same value is a no-op: no second audit row. An
	// idempotent PATCH must not litter the ledger.
	if _, err := store.UpdateInstallation(admin, &newName, nil, nil); err != nil {
		t.Fatalf("re-asserting the same name: %v", err)
	}
	if n := installationAuditCount(t, e); n != before+1 {
		t.Errorf("audit rows = %d after an unchanged write, want %d", n, before+1)
	}

	// A read reflects the write.
	reread, err := store.GetInstallation(rep)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Name != newName {
		t.Errorf("re-read name = %q, want %q", reread.Name, newName)
	}
}

func TestInstallationSettingsRefuseValuesTheOwningModuleRejects(t *testing.T) {
	e := SetupSearch(t)
	store := identity.NewInstallationSettings(e.Pool, compose.NewSettingsStore(e.Pool))
	admin := e.installationSettingsCtx(principal.ObjectGrant{Read: true, Update: true})

	blank := "   "
	if _, err := store.UpdateInstallation(admin, &blank, nil, nil); err == nil {
		t.Error("a whitespace-only organization name was accepted")
	}

	notAZone := "Mars/Olympus_Mons"
	if _, err := store.UpdateInstallation(admin, nil, &notAZone, nil); err == nil {
		t.Error("a zone name this server's tzdata does not know was accepted")
	}

	notACurrency := "EURO"
	_, err := store.UpdateInstallation(admin, nil, nil, &notACurrency)
	if err == nil {
		t.Error("a four-letter base currency was accepted")
	}
	// The refusal names the setting and says what to type instead, on every
	// surface — it implements FieldFault rather than being a bare error.
	var fault apperrors.FieldFault
	if !errors.As(err, &fault) {
		t.Fatalf("refusal does not classify as a field fault: %v", err)
	}
	field, _, message := fault.FieldFault()
	if field != "installation.base_currency" {
		t.Errorf("refusal names field %q, want the setting key", field)
	}
	if !strings.Contains(message, "ISO-4217") {
		t.Errorf("refusal message %q does not say what a base currency is", message)
	}
}

// The freeze is the one guard that fails OPEN: with the probe unwired the
// currency simply stays editable, and nothing else in the system notices. Only
// a test that creates a deal carrying a frozen conversion rate can tell a
// working guard from an absent one.
func TestBaseCurrencyFreezesOnceADealHasConvertedAgainstIt(t *testing.T) {
	e := SetupSearch(t)
	store := identity.NewInstallationSettings(e.Pool, compose.NewSettingsStore(e.Pool))
	admin := e.installationSettingsCtx(principal.ObjectGrant{Read: true, Update: true})

	// Changeable first — this is the case ADR-0085 §7 exists to serve: an
	// installation that chose wrong in configuration and noticed in week one.
	chf := "CHF"
	if _, err := store.UpdateInstallation(admin, nil, nil, &chf); err != nil {
		t.Fatalf("the base currency was refused before any deal converted: %v", err)
	}

	// A deal with a frozen rate against the base. Its pipeline and stage are
	// seeded explicitly rather than selected from the fixture: an
	// INSERT..SELECT that matches nothing SUCCEEDS, so a missing fixture would
	// leave this test asserting a freeze that never had a deal to fire on —
	// passing or failing for the wrong reason either way.
	pipeline := e.seed(t, `
		INSERT INTO pipeline (id, workspace_id, name, is_default) VALUES ($1, $2, 'Freeze fixture', false)`)
	stage := e.seed(t, `
		INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Won', 1, 'won', 100)`, pipeline)
	// Every CHECK on `deal` has to be satisfied for the row to land, and the
	// row landing is the whole point of this fixture:
	//   deal_amount_currency_pair — amount and currency are set together
	//   deal_closed_at            — a non-open deal carries closed_at
	//   deal_closed_fx            — a closed deal with an amount carries the rate
	//   deal_lost_reason          — not reached; 'won', not 'lost'
	// The frozen rate is the one this test actually needs; the rest are the
	// price of a valid closed deal.
	e.seed(t, `
		INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, source, captured_by,
		                  amount_minor, currency, fx_rate_to_base, status, closed_at)
		VALUES ($1, $2, 'Converted deal', $3, $4, 'seed', 'system:test',
		        100000, 'EUR', 1.0850000000, 'won', now())`,
		pipeline, stage)

	// Prove the fixture landed before trusting what the probe says about it.
	var converted int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM deal WHERE fx_rate_to_base IS NOT NULL`).Scan(&converted)
	}); err != nil {
		t.Fatal(err)
	}
	if converted != 1 {
		t.Fatalf("%d deals carry a frozen rate, want 1 — the fixture did not land, so the freeze below would prove nothing", converted)
	}

	// The read now reports it locked, WITH the reason — that is what lets the
	// client render the field read-only instead of discovering the refusal.
	got, err := store.GetInstallation(admin)
	if err != nil {
		t.Fatal(err)
	}
	if !got.BaseCurrencyLocked {
		t.Fatal("the base currency is still reported changeable after a deal froze a rate against it")
	}
	if !strings.Contains(got.BaseCurrencyLockedReason, "1 deal") {
		t.Errorf("lock reason = %q, want it to name how many deals converted", got.BaseCurrencyLockedReason)
	}

	// And the write is refused, as a field fault naming the setting.
	usd := "USD"
	_, err = store.UpdateInstallation(admin, nil, nil, &usd)
	if err == nil {
		t.Fatal("the base currency was changed after deals had converted against it")
	}
	var fault apperrors.FieldFault
	if !errors.As(err, &fault) {
		t.Fatalf("the freeze refusal does not classify as a field fault: %v", err)
	}
	field, code, _ := fault.FieldFault()
	if field != "installation.base_currency" || code != "setting_frozen" {
		t.Errorf("refusal = %s/%s, want installation.base_currency/setting_frozen", field, code)
	}

	// Everything else on the surface still changes — the freeze is scoped to
	// the one value whose history is at stake.
	name := "Still Renamable GmbH"
	if _, err := store.UpdateInstallation(admin, &name, nil, nil); err != nil {
		t.Errorf("the freeze on one setting blocked another: %v", err)
	}
}
