// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Retention-policy authoring end to end (UC-GDPR-09, GCS-WIRE-1..5).
//
// Four things need a real database, and none of them can be shown with a unit
// test:
//
//   - the one-row-per-scope rule is the CONSTRAINT's, not the handler's. The
//     test inserts directly, bypassing the store entirely, because a store-level
//     refusal would look identical while leaving every other writer — a future
//     surface, a migration, a psql session — free to break the bound the
//     scheduler's timeout is derived from. It also covers the NULL-category
//     scope specifically, which is the case the plain UNIQUE silently allowed.
//   - the retain-only posture leaves an over-age record standing while an
//     archive policy in the same pass still acts.
//   - authoring writes its audit row in the same transaction as the row.
//   - `deal/won` is authorable and acts, which is UC-GDPR-09's own
//     main-success example and had no selector before this work.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// retentionAdminCtx builds a human principal holding a specific
// retention_policy grant. The object is admin/ops-only on every verb including
// read, so a grant of {} is the posture manager/rep/read_only actually hold.
func retentionAdminCtx(ws ids.UUID, grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"retention_policy": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

// insertPolicyDirectly writes a policy row with no store, no gate and no
// validation — the raw INSERT a migration or a psql session would make. This is
// the only way to ask the DATABASE whether it refuses a duplicate scope.
func insertPolicyDirectly(e *Env, objectType string, category *string, retainDays int, action string) error {
	return database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4)`,
			objectType, category, retainDays, action)
		return err
	})
}

// retentionPolicyAudits counts the audit rows the authoring surface has written.
// Keyed on entity_type, so it never counts the ENGINE's per-record retention
// audits, which name the record's own type instead.
func retentionPolicyAudits(t *testing.T, e *Env) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE entity_type = 'retention_policy'`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestOneRetentionPolicyPerScopeIsTheDatabasesRule proves DESIGN §3.1: the
// uniqueness the scheduler's pass bound depends on is enforced by
// retention_policy_unique, for a named category AND for the NULL one.
func TestOneRetentionPolicyPerScopeIsTheDatabasesRule(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)

	transcript := "transcript"
	cases := []struct {
		name       string
		objectType string
		category   *string
	}{
		// The named-category scope. A plain UNIQUE already refused this one, so
		// it is the control: it proves the constraint is present and armed.
		{"named category", "activity", &transcript},
		// The NULL-category scope — the case a plain UNIQUE silently ALLOWED,
		// because Postgres counts NULLs as distinct. This is the assertion the
		// whole constraint change exists for.
		{"null category", "activity", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := insertPolicyDirectly(e, tc.objectType, tc.category, 30, "archive")
			if err == nil {
				t.Fatal("a second policy row for one scope was accepted — " +
					"retention_policy_unique is not NULLS NOT DISTINCT, and " +
					"privacy.MaxPassDuration's derivation is a fiction")
			}
			if !strings.Contains(err.Error(), "retention_policy_unique") {
				t.Fatalf("refused, but not by the scope constraint: %v", err)
			}
		})
	}
}

// TestAuthoringARetentionPolicyIsGatedAuditedAndScopeBounded walks the store's
// whole surface: the gate, the conflict, the unknown scope, and the audit row
// that has to commit with the policy.
func TestAuthoringARetentionPolicyIsGatedAuditedAndScopeBounded(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)
	store := privacy.NewPolicyStore(e.DB())

	admin := retentionAdminCtx(e.WS, principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true})
	// The grant every non-admin system role actually holds on this object.
	rep := retentionAdminCtx(e.WS, principal.ObjectGrant{})

	if _, err := store.List(rep); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a role with no retention_policy grant could read the ladder: %v", err)
	}
	if _, err := store.Create(rep, sevenYearWonPolicy(t)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a role with no retention_policy grant could author a policy: %v", err)
	}

	before := retentionPolicyAudits(t, e)
	created, err := store.Create(admin, sevenYearWonPolicy(t))
	if err != nil {
		t.Fatal(err)
	}
	if created.Scope.String() != "deal/won" || created.RetainDays != 2555 {
		t.Fatalf("stored policy is not what was authored: %+v", created)
	}
	if after := retentionPolicyAudits(t, e); after != before+1 {
		t.Errorf("retention_policy audit rows = %d, want %d — the row and its "+
			"audit entry commit together or neither does", after, before+1)
	}

	// Same scope again: the store surfaces the constraint as a conflict rather
	// than upserting, so one admin cannot silently overwrite another's window.
	if _, err := store.Create(admin, sevenYearWonPolicy(t)); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("a duplicate scope was not a conflict: %v", err)
	}

	// Disabling preserves the row (UC-GDPR-09 E2) — the audited way to pause a
	// rule without losing its window and lawful basis.
	disabled := false
	updated, err := store.Update(admin, created.ID, privacy.PolicyPatch{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.RetainDays != 2555 {
		t.Fatalf("disabling changed more than enabled: %+v", updated)
	}

	if err := store.Delete(admin, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(admin, created.ID, privacy.PolicyPatch{Enabled: &disabled}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("a deleted policy is still patchable: %v", err)
	}
}

// sevenYearWonPolicy is #695's actual customer requirement, expressed as a
// policy: keep won deals for seven years, then archive rather than destroy.
func sevenYearWonPolicy(t *testing.T) privacy.PolicyInput {
	t.Helper()
	scope, err := privacy.ParseRetentionScope("deal/won")
	if err != nil {
		t.Fatalf("deal/won is meant to be authorable: %v", err)
	}
	basis := "contractual retention obligation"
	return privacy.PolicyInput{
		Scope: scope, RetainDays: 2555, Action: "archive", LawfulBasis: &basis, Enabled: true,
	}
}

// TestAuthoringRefusesAScopeTheEvaluatorCannotAct proves the refusal names the
// alternatives: a closed vocabulary the caller cannot enumerate is unfixable
// from the error alone.
func TestAuthoringRefusesAScopeTheEvaluatorCannotAct(t *testing.T) {
	if _, err := privacy.ParseRetentionScope("deal/abandoned"); err == nil {
		t.Fatal("a scope with no selector was accepted — it would be stored and " +
			"then skipped, loudly, every pass forever")
	} else {
		var fault apperrors.FieldFault
		if !errors.As(err, &fault) {
			t.Fatalf("the refusal does not classify as a field fault: %v", err)
		}
		field, code, message := fault.FieldFault()
		if field != "scope" || code != "unknown_retention_scope" {
			t.Errorf("field/code = %q/%q, want scope/unknown_retention_scope", field, code)
		}
		for _, authorable := range privacy.AuthorableScopes() {
			if !strings.Contains(message, authorable) {
				t.Errorf("the refusal omits the authorable scope %q, so the caller "+
					"cannot fix the request from it: %q", authorable, message)
			}
		}
	}
}

// TestRetainOnlyPostureSuppressesDestructionButNotArchival is GCS-AC-11: the
// posture outranks every policy without editing one.
func TestRetainOnlyPostureSuppressesDestructionButNotArchival(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)
	staleLead, _, staleDeal, transcript := seedOverAgeRecords(t, e)

	// The posture is seeded the way bootstrap seeds it from margince.yaml, so
	// the pass reads exactly what a `default_policy: retain_only` install has.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return settings.SeedValue(context.Background(), tx, privacy.RetainOnly, true)
	}); err != nil {
		t.Fatal(err)
	}

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.EvaluateWorkspace(RetentionPassCtx(e.WS)); err != nil {
		t.Fatal(err)
	}

	var leadName string
	var transcriptBody *string
	var dealArchived bool
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT full_name FROM lead WHERE id = $1`, staleLead).Scan(&leadName); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT body FROM activity WHERE id = $1`, transcript).Scan(&transcriptBody); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM deal WHERE id = $1`, staleDeal).Scan(&dealArchived)
	}); err != nil {
		t.Fatal(err)
	}

	// anonymize is destruction: the 400-day lead keeps its name.
	if leadName != "Old Cold Lead" {
		t.Errorf("a retain-only installation anonymized an over-age lead: %q", leadName)
	}
	// erase is destruction: the 400-day transcript keeps its body.
	if transcriptBody == nil || *transcriptBody != "sensitive words" {
		t.Errorf("a retain-only installation erased an over-age transcript body: %v", transcriptBody)
	}
	// archive RETAINS, so it is the one action the posture leaves alone — the
	// assertion that keeps this from passing for the wrong reason (an engine
	// that simply did nothing at all would fail here).
	if !dealArchived {
		t.Error("the retain-only posture suppressed an ARCHIVE policy — archiving " +
			"retains the record, so it is not destruction and must still run")
	}

	// Lifting the posture resumes enforcement on rows nobody re-authored.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`DELETE FROM setting WHERE key = $1`, privacy.RetainOnly.Key())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.EvaluateWorkspace(RetentionPassCtx(e.WS)); err != nil {
		t.Fatal(err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT full_name FROM lead WHERE id = $1`, staleLead).Scan(&leadName)
	}); err != nil {
		t.Fatal(err)
	}
	if leadName != "Anonymized Lead" {
		t.Errorf("lifting the posture did not resume enforcement: %q", leadName)
	}
}

// TestRetainOnlyPostureIsReportedOnEveryPolicyItOverrides is the surface half of
// GCS-AC-11: an enabled policy that will not act has to say why, or an admin
// reads the screen as a promise the engine is not keeping.
func TestRetainOnlyPostureIsReportedOnEveryPolicyItOverrides(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)
	store := privacy.NewPolicyStore(e.DB())
	admin := retentionAdminCtx(e.WS, principal.ObjectGrant{Read: true, Update: true})

	postures := privacy.NewPostureStore(compose.NewSettingsStore(e.Pool))
	on := true
	if _, err := postures.SetPosture(admin, &on); err != nil {
		t.Fatal(err)
	}

	policies, err := store.List(admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) == 0 {
		t.Fatal("no seeded policies to judge")
	}
	for _, p := range policies {
		destructive := p.Action == "anonymize" || p.Action == "erase"
		if p.SuppressedByPosture != destructive {
			t.Errorf("%s/%s: suppressed_by_posture = %v, want %v",
				p.Scope, p.Action, p.SuppressedByPosture, destructive)
		}
	}

	// And the posture read answers what was written, through the settings row
	// rather than the in-memory value the write returned.
	held, err := postures.Posture(admin)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Error("the posture did not survive its own write")
	}
}

// bootstrapWithRetentionPosture runs a REAL bootstrap against an empty database
// with the given seeds.retention block, and reports the posture and policy count
// that installation ends up with. Going through EnsureInstallation is the point:
// a test that wrote the setting itself would prove nothing about what
// `margince.yaml` does on first boot.
func bootstrapWithRetentionPosture(t *testing.T, retention *deployconfig.RetentionSeed) (retainOnly bool, policies int) {
	t.Helper()
	e := apptest.SetupApp(t)
	pwFile := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(pwFile, []byte("correct-horse-battery"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := deployconfig.Config{
		Version:      1,
		Organization: deployconfig.Organization{Name: "Regulated Org", BaseCurrency: "EUR", Timezone: "Europe/Berlin"},
		BootstrapAdmin: &deployconfig.BootstrapAdmin{
			Email: "ops@regulated.test", DisplayName: "Ops", PasswordFile: pwFile,
		},
		Seeds: deployconfig.Seeds{
			Retention:          retention,
			StarterAutomations: boolPtr(false),
			BookingPage:        boolPtr(false),
		},
	}
	if err := compose.EnsureInstallation(context.Background(),
		e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	ctx := context.Background()
	// Read the setting row directly rather than through the gated reader: this
	// asks what bootstrap actually WROTE, with no default resolution in the way,
	// which is the difference between a seeded posture and an absent one.
	var stored []byte
	err := e.Owner.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = $1`, privacy.RetainOnly.Key()).Scan(&stored)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		retainOnly = false
	case err != nil:
		t.Fatal(err)
	default:
		retainOnly = string(stored) == "true"
	}
	if err := e.Owner.QueryRow(ctx, `SELECT count(*) FROM retention_policy`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	return retainOnly, policies
}

// TestBootstrapTakesItsRetentionPostureFromTheDeploymentConfiguration is
// GCS-AC-12 — the answer to issue #695's fourth ask. A deployment under a
// keep-everything obligation declares it before first boot, so the window
// between seeding the ladder and the first admin login is never governed by a
// destructive default.
func TestBootstrapTakesItsRetentionPostureFromTheDeploymentConfiguration(t *testing.T) {
	// The seeded ladder is the data-model's pins under BOTH postures, so this is
	// asserted per case rather than once: a posture that quietly planted fewer
	// rows would hide the ladder an admin is entitled to see.
	const seededPolicies = 6

	cases := []struct {
		name      string
		retention *deployconfig.RetentionSeed
		want      bool
	}{
		// The historical behaviour, byte for byte: an installation that never
		// heard of this key is unaffected by it.
		{"omitted block", nil, false},
		{"empty value", &deployconfig.RetentionSeed{}, false},
		{"standard", &deployconfig.RetentionSeed{DefaultPolicy: deployconfig.RetentionStandardPosture}, false},
		// The regulated installation: same six rows, nothing destroyed.
		{"retain_only", &deployconfig.RetentionSeed{DefaultPolicy: deployconfig.RetentionRetainOnlyPosture}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retainOnly, policies := bootstrapWithRetentionPosture(t, tc.retention)
			if retainOnly != tc.want {
				t.Errorf("retain-only posture after bootstrap = %v, want %v", retainOnly, tc.want)
			}
			if policies != seededPolicies {
				t.Errorf("seeded %d retention policies, want %d — the posture selects "+
					"what runs, never which rows exist", policies, seededPolicies)
			}
		})
	}
}
