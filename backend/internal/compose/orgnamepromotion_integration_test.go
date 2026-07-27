// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The org-name promotion sweep over a real Postgres (PO-F-2a): two agreeing
// signatures rename a provisionally-named organization, one signature alone
// only asks, and a name a human or a dossier already set is untouchable.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedProvisionalOrg plants one organization named from its domain, as the
// capture path leaves it.
func seedProvisionalOrg(t *testing.T, e *integration.Env, name, nameSource string) ids.UUID {
	t.Helper()
	org := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, display_name, name_source, source, captured_by)
			VALUES ($1, $2, $3, $4, 'gmail:seed', 'connector:gmail')`, org, e.WS, name, nameSource)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return org
}

// seedSigningEmployee plants one person employed by org whose accepted
// signature evidence names signedName as their company.
func seedSigningEmployee(t *testing.T, e *integration.Env, org ids.UUID, fullName, signedName string) {
	t.Helper()
	person := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'gmail:seed', 'connector:gmail')`, person, e.WS, fullName); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO relationship (workspace_id, kind, person_id, organization_id, is_current_primary, source, captured_by)
			VALUES ($1, 'employment', $2, $3, true, 'gmail:seed', 'connector:gmail')`,
			e.WS, person, org); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO person_profile_field (workspace_id, person_id, field, value, evidence_snippet, source_ref, confidence, source, captured_by)
			VALUES ($1, $2, 'org_name', $3, $3, 'activity:seed', 0.9, 'capture_enrich', 'agent:enrich')`,
			e.WS, person, signedName)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func orgNameAndSource(t *testing.T, e *integration.Env, org ids.UUID) (string, string) {
	t.Helper()
	var name, source string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT display_name, name_source FROM organization WHERE id = $1`, org).Scan(&name, &source)
	})
	if err != nil {
		t.Fatal(err)
	}
	return name, source
}

func TestOrgNamePromotionCorroboratedBySecondSignature(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")
	seedSigningEmployee(t, e, org, "Bob Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	name, source := orgNameAndSource(t, e, org)
	if name != "Gitex Global" {
		t.Fatalf("display_name = %q, want the corroborated signature name", name)
	}
	if source != "signature" {
		t.Fatalf("name_source = %q, want 'signature' — the provenance must record who named it", source)
	}
	if got := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE entity_type = 'organization' AND entity_id = $1`, org); got == 0 {
		t.Fatal("a rename with no audit row is a rename nobody can explain")
	}
	if got := e.WsCount(t, `SELECT count(*) FROM approval WHERE target_entity_id = $1`, org); got != 0 {
		t.Fatalf("%d approvals staged — a corroborated name is applied, not asked about", got)
	}

	t.Run("a second pass changes nothing", func(t *testing.T) {
		if err := promoter.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		name, source := orgNameAndSource(t, e, org)
		if name != "Gitex Global" || source != "signature" {
			t.Fatalf("second pass moved the name to %q/%q", name, source)
		}
	})
}

func TestOrgNamePromotionAsksAboutASingleSignature(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	name, source := orgNameAndSource(t, e, org)
	if name != "Gitex" || source != "domain" {
		t.Fatalf("organization became %q/%q — one signature must not rename anything", name, source)
	}
	staged := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org)
	if staged != 1 {
		t.Fatalf("%d staged proposals, want exactly one question for a human", staged)
	}

	t.Run("a re-run joins the pending offer instead of stacking another", func(t *testing.T) {
		if err := promoter.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		staged := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org)
		if staged != 1 {
			t.Fatalf("%d staged proposals after a second pass, want the same one", staged)
		}
	})
}

func TestOrgNamePromotionNeverOverwritesAStrongerSource(t *testing.T) {
	e := integration.Setup(t)
	for _, source := range []string{"human", "dossier", "signature"} {
		t.Run("name_source "+source+" is untouchable", func(t *testing.T) {
			org := seedProvisionalOrg(t, e, "Acme The Human Typed", source)
			seedSigningEmployee(t, e, org, "Alice Signer", "Acme Signature GmbH")
			seedSigningEmployee(t, e, org, "Bob Signer", "Acme Signature GmbH")

			promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
			if err := promoter.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			name, got := orgNameAndSource(t, e, org)
			if name != "Acme The Human Typed" || got != source {
				t.Fatalf("organization became %q/%q — a %s name must outrank a signature", name, got, source)
			}
			if staged := e.WsCount(t, `SELECT count(*) FROM approval WHERE target_entity_id = $1`, org); staged != 0 {
				t.Fatalf("%d staged proposals — a settled name is not a question", staged)
			}
		})
	}
}

func TestOrgNamePromotionCorroboratedByTheDossier(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")
	e.WsExec(t, `
		INSERT INTO organization_profile_field (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
		VALUES ($1, $2, 'legal_name', 'Gitex Global GmbH', 'Gitex Global GmbH', 'https://gitex.example', 0.9, 'site_read', 'agent:siteread')`,
		e.WS, org)

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	name, source := orgNameAndSource(t, e, org)
	if name != "Gitex Global" || source != "signature" {
		t.Fatalf("organization = %q/%q, want the site-corroborated signature name applied", name, source)
	}
}
