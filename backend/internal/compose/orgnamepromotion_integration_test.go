// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The org-name promotion sweep over a real Postgres (PO-F-2a): the site dossier
// renames a provisionally-named organization, signatures only ever ask —
// however many agree, they are one sender-chosen mail domain — and a name a
// human or a dossier already set is untouchable.

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

// seedDossierName plants the company's own site-stated name — the one source an
// external sender cannot author, and therefore the only one that renames
// without asking.
func seedDossierName(t *testing.T, e *integration.Env, org ids.UUID, legalName string) {
	t.Helper()
	e.WsExec(t, `
		INSERT INTO organization_profile_field (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by)
		VALUES ($1, $2, 'legal_name', $3, $3, 'https://gitex.example', 0.9, 'site_read', 'agent:siteread')`,
		e.WS, org, legalName)
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

// Agreeing signatures are NOT an independent second source: everyone at one
// organization shares one mail domain, the capture path authenticates no From
// header, and the signature block is the sender's own text. So an actor who can
// forge two addresses at the target's domain must not be able to write the name
// — the sweep asks a human instead.
func TestOrgNamePromotionAgreeingSignaturesStillAskAHuman(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")
	seedSigningEmployee(t, e, org, "Bob Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	name, source := orgNameAndSource(t, e, org)
	if name != "Gitex" || source != "domain" {
		t.Fatalf("organization became %q/%q — two signatures on one mail domain must not rename anything unattended", name, source)
	}
	staged := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org)
	if staged != 1 {
		t.Fatalf("%d staged proposals, want exactly one question for a human", staged)
	}

	t.Run("a second pass joins the pending offer instead of stacking another", func(t *testing.T) {
		if err := promoter.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		staged := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org)
		if staged != 1 {
			t.Fatalf("%d staged proposals after a second pass, want the same one", staged)
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
	seedDossierName(t, e, org, "Gitex Global GmbH")

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
	seedDossierName(t, e, ready, "Ready Global")

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
	// Twice, because the refusal has to hold on every later pass rather than
	// only the one that immediately follows the decline.
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

// declineTheStagedRename runs one sweep, declines the offer it stages, and
// returns — the setup both tests below start from.
func declineTheStagedRename(t *testing.T, e *integration.Env, promoter *OrgNamePromoter, org ids.UUID) {
	t.Helper()
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
	if _, err := approvalsServiceWithEffects(e.Pool).Decide(e.As(e.Rep1, nil, integration.AdminPerms),
		ids.From[ids.ApprovalKind](approvalID), false, nil); err != nil {
		t.Fatalf("declining the rename: %v", err)
	}
}

// The refusal must survive the EVIDENCE moving. The offer's payload carries the
// corroborating persons and the record's current name, so a memory keyed on a
// hash of that payload is forgotten the moment a second person signs — and the
// rename a human refused is offered again, every night, until someone clicks
// approve. The identity of the decision is the record and the proposed name;
// nothing else.
func TestOrgNamePromotionRemembersADeclineAfterTheEvidenceMoves(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	declineTheStagedRename(t, e, promoter, org)

	// A second sender signs with the same company name: a different persons
	// list, so a different payload and a different diff hash — the same
	// question.
	seedSigningEmployee(t, e, org, "Bob Signer", "Gitex Global")
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if n := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org); n != 1 {
		t.Fatalf("%d offers after a decline plus a new signer, want only the declined one — a refusal keyed on the payload forgets itself whenever the evidence moves", n)
	}
	if name, source := orgNameAndSource(t, e, org); name != "Gitex" || source != "domain" {
		t.Fatalf("organization = %q/%q — a declined rename must change nothing", name, source)
	}
}

// Evidence moving while an offer is STILL PENDING must refresh the question,
// not duplicate it. The offer's payload carries the corroborating persons, so a
// new signer changes the diff hash and JoinPending — which joins on that hash —
// finds nothing to join. Only the staging Identity collapses the two: the
// fresher offer supersedes the stale one instead of competing with it in the
// inbox. Without it a human is asked the same question once per signer.
func TestOrgNamePromotionSupersedesAStalePendingOffer(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A second signer for the same claim: same normalized name, different
	// evidence, so a different payload and a different diff hash.
	seedSigningEmployee(t, e, org, "Bob Signer", "Gitex Global")
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	live := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'org_name_promotion' AND target_entity_id = $1
		   AND status = 'pending' AND expires_at > now()`, org)
	if live != 1 {
		t.Fatalf("%d live offers after the evidence moved, want exactly 1 — a human must be asked this question once, not once per signer", live)
	}
	// The survivor is the FRESH one: it cites both signers.
	var persons int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT jsonb_array_length(proposed_change -> 'persons') FROM approval
			 WHERE kind = 'org_name_promotion' AND target_entity_id = $1
			   AND status = 'pending' AND expires_at > now()`, org).Scan(&persons)
	}); err != nil {
		t.Fatalf("reading the surviving offer: %v", err)
	}
	if persons != 2 {
		t.Errorf("the surviving offer cites %d signer(s), want the fresher 2 — the stale offer outlived the fresh one", persons)
	}
}

// A refusal recorded BEFORE proposed_name_key existed must still be
// remembered, INCLUDING when the spelling has since moved. Those payloads
// carry only the raw name, and the raw name is dominantSpelling's pick — it
// changes as signatures accumulate. Matching on it would forget the refusal
// exactly when a second signer arrives, which is also when the claim becomes
// corroborated enough to apply without asking.
func TestOrgNamePromotionRemembersADeclineRecordedBeforeTheIdentityField(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	declineTheStagedRename(t, e, promoter, org)

	// Age the refusal into a pre-upgrade one by dropping the field that
	// version of the code never wrote.
	e.WsExec(t, `
		UPDATE approval SET proposed_change = proposed_change - 'proposed_name_key'
		 WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org)

	// A second signer spells the SAME company differently, which moves the
	// dominant spelling off the one the refusal recorded, and the dossier now
	// agrees — the combination that normally writes without asking.
	seedSigningEmployee(t, e, org, "Bob Signer", "GITEX GLOBAL")
	seedDossierName(t, e, org, "Gitex Global")
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if name, source := orgNameAndSource(t, e, org); name != "Gitex" || source != "domain" {
		t.Fatalf("organization = %q/%q — a pre-upgrade refusal must bind even after the spelling moved", name, source)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM approval
		 WHERE kind = 'org_name_promotion' AND target_entity_id = $1`, org); n != 1 {
		t.Fatalf("%d offers, want only the declined one — the legacy refusal must also stop re-staging", n)
	}
}

// The refusal must bind the AUTO-APPLY path too. Declining leaves name_source
// at 'domain' by design, so the promotion CAS still admits the write: a later
// corroboration that never consults the approval simply performs the rename the
// human refused, with no offer, no inbox row and nothing to notice.
func TestOrgNamePromotionDoesNotAutoApplyADeclinedRename(t *testing.T) {
	e := integration.Setup(t)
	org := seedProvisionalOrg(t, e, "Gitex", "domain")
	seedSigningEmployee(t, e, org, "Alice Signer", "Gitex Global")

	promoter := NewOrgNamePromoter(e.Pool, slog.New(slog.DiscardHandler))
	declineTheStagedRename(t, e, promoter, org)

	// The dossier now agrees — the one corroboration that normally writes
	// without asking.
	seedDossierName(t, e, org, "Gitex Global")
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if name, source := orgNameAndSource(t, e, org); name != "Gitex" || source != "domain" {
		t.Fatalf("organization = %q/%q — corroboration arriving later must not execute a rename a human already refused", name, source)
	}

	// The positive control: the same corroboration on an organization nobody
	// refused IS applied, so this test cannot pass by promotion being broken.
	fresh := seedProvisionalOrg(t, e, "Acme", "domain")
	seedSigningEmployee(t, e, fresh, "Carol Signer", "Acme Global")
	seedDossierName(t, e, fresh, "Acme Global")
	if err := promoter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if name, source := orgNameAndSource(t, e, fresh); name != "Acme Global" || source != "signature" {
		t.Fatalf("un-refused organization = %q/%q, want the dossier-corroborated name applied", name, source)
	}
}
