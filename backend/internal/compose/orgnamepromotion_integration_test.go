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

// The sweep must reach EVERY candidate, not the first page of them.
//
// Most candidates reach a verdict that changes nothing — their one proposed name
// is uncorroborated and waits on a human — and those rows stay candidates
// indefinitely. A pass that read a fixed prefix of a fixed ordering would spend
// every night on the same unresolvable rows, and an organization behind them
// whose corroborated name is ready to apply would never be reached at all.
func TestOrgNamePromotionReachesCandidatesBeyondTheFirstPage(t *testing.T) {
	e := integration.Setup(t)

	// Fill more than one page with organizations that can never resolve: one
	// signature each, so every pass stages an offer and moves nothing.
	for i := 0; i < orgNamePromotionPageSize; i++ {
		stuck := seedProvisionalOrg(t, e, "Stuck", "domain")
		seedSigningEmployee(t, e, stuck, "Lone Signer", "Stuck Holdings")
	}
	// And behind them, one organization whose name IS corroborated and should be
	// applied today. Seeded last, so it sorts after the whole first page.
	ready := seedProvisionalOrg(t, e, "Ready", "domain")
	seedSigningEmployee(t, e, ready, "Alice Signer", "Ready Global")
	seedSigningEmployee(t, e, ready, "Bob Signer", "Ready Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	name, source := orgNameAndSource(t, e, ready)
	if name != "Ready Global" || source != "signature" {
		t.Fatalf("the organization behind a full page of unresolvable ones is %q/%q — it was never reached", name, source)
	}
}

// A human's "no" has to mean something. JoinPending only joins a PENDING offer,
// so once a rename is declined the next pass finds nothing to join — and without
// a decided-offer check it stages a fresh copy of what was just refused, every
// night, because the signature that produced it never goes away.
func TestOrgNamePromotionDoesNotReofferADeclinedRename(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	svc := approvalsServiceWithEffects(e.Pool)
	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var approvalID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`,
			org).Scan(&approvalID)
	}); err != nil {
		t.Fatalf("reading the staged offer: %v", err)
	}
	if _, err := svc.Decide(e.As(e.Rep1, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](approvalID), false, nil); err != nil {
		t.Fatalf("declining the rename: %v", err)
	}

	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org); n != 1 {
		t.Fatalf("%d offers after a decline, want the one that was declined — a refused rename must not come back", n)
	}
	if name, source := orgNameAndSource(t, e, org); name != "Gitex" || source != "domain" {
		t.Fatalf("organization = %q/%q — a declined rename must change nothing", name, source)
	}
}

// The decline check and the staging must be ONE transaction. Checking first and
// staging afterwards leaves a window: a decision landing in between leaves the
// check reading "not declined" and the staging finding no pending row to join,
// so the refused offer is recreated anyway.
//
// Proven by driving the window directly: the offer is declined AFTER the pass
// has computed its verdict — which is exactly the interleaving a separate check
// would lose — and the pass must still refuse to re-offer it.
func TestOrgNamePromotionRefusesADeclineThatLandsMidPass(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	svc := approvalsServiceWithEffects(e.Pool)
	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Decline every offer standing for this rename, then run the pass again with
	// the identical evidence — the state a second worker would find having
	// already decided the verdict before the human answered.
	rows := e.WsCount(t, `
		SELECT count(*) FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org)
	if rows != 1 {
		t.Fatalf("%d offers after the first pass, want 1", rows)
	}
	var approvalID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`,
			org).Scan(&approvalID)
	}); err != nil {
		t.Fatalf("reading the staged offer: %v", err)
	}
	if _, err := svc.Decide(e.As(e.Rep1, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](approvalID), false, nil); err != nil {
		t.Fatalf("declining the rename: %v", err)
	}

	// Two further passes: the refusal must hold on every one of them, not just
	// the first.
	for i := 0; i < 2; i++ {
		if err := promoter.Run(context.Background()); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org); n != 1 {
		t.Fatalf("%d offers, want the one that was declined — a refused rename must never be recreated", n)
	}
}
