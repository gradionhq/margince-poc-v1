// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Correcting and confirming the two evidence sidecars (ADR-0085 / A130).
//
// The claim under test is that a correction changes the COMPANY RECORD and not
// only its receipt: where a profile field has a canonical organization column,
// both move in one transaction, and the machine's original proposal survives in
// the audit trail rather than being overwritten by the human's answer.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// evidenceOrg seeds one organization carrying the sidecar rows these tests
// correct: a display_name profile field (which HAS a canonical column), an icp
// profile field (which has none), two single-value company facts that both key
// on the empty value_key, and two named_customer facts that share a field and
// differ only by value_key.
func evidenceOrg(ctx context.Context, t *testing.T, e *dedupeEnv) ids.OrganizationID {
	t.Helper()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Voltaq Systems GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "voltaq.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization_profile_field
			  (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, source, captured_by, retrieved_at)
			VALUES ($1,$2,'display_name','Voltaq Systems','"Voltaq Systems"','https://voltaq.test/',0.7,'site_read','agent:deepread',now()),
			       ($1,$2,'icp','Energy-intensive manufacturers','"…for energy-intensive manufacturers"','https://voltaq.test/about',0.9,'site_read','agent:deepread',now())`,
			e.ws, orgID); err != nil {
			return err
		}
		// Both company facts carry value_key '' — the cardinality check requires
		// it — so field alone is what tells them apart.
		_, err := tx.Exec(ctx, `
			INSERT INTO organization_fact
			  (workspace_id, organization_id, category, field, value, value_key, evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1,$2,'company','phone','+49 30 1234','','"+49 30 1234"','https://voltaq.test/impressum',0.95,'site_read','agent:deepread'),
			       ($1,$2,'company','founded_year','1998','','"gegründet 1998"','https://voltaq.test/about',0.8,'site_read','agent:deepread'),
			       ($1,$2,'signal','named_customer','Acme Inc','acme-inc','"trusted by Acme Inc"','https://voltaq.test/customers',0.6,'site_read','agent:deepread'),
			       ($1,$2,'signal','named_customer','Brandt AG','brandt-ag','"and Brandt AG"','https://voltaq.test/customers',0.6,'site_read','agent:deepread')`,
			e.ws, orgID)
		return err
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	return orgID
}

// orgColumn reads one column off the organization row, so a test can assert on
// the record itself rather than on what a read model chose to report.
func orgColumn[T any](ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID, column string) T {
	t.Helper()
	var v T
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT `+column+` FROM organization WHERE id = $1`, orgID).Scan(&v)
	}); err != nil {
		t.Fatalf("read organization.%s: %v", column, err)
	}
	return v
}

func factValue(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID, field, valueKey string) string {
	t.Helper()
	var v string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT value FROM organization_fact
			 WHERE organization_id = $1 AND field = $2 AND value_key = $3`,
			orgID, field, valueKey).Scan(&v)
	}); err != nil {
		t.Fatalf("read fact %s:%s: %v", field, valueKey, err)
	}
	return v
}

func TestCorrectingAProfileFieldMovesTheCompanyRecordNotOnlyItsReceipt(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "Voltaq Systems GmbH & Co. KG"
	out, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "display_name",
		ProfileFieldWriteInput{Value: &corrected})
	if err != nil {
		t.Fatalf("correct display_name: %v", err)
	}

	if out.Value != corrected {
		t.Errorf("returned value = %q, want %q", out.Value, corrected)
	}
	if string(out.Source) != "human" {
		t.Errorf("source = %q, want human — the value is no longer the machine's claim", out.Source)
	}
	if out.VerifiedAt == nil || out.VerifiedBy == nil {
		t.Errorf("a correction records who agreed and when, got verified_at=%v verified_by=%v",
			out.VerifiedAt, out.VerifiedBy)
	}
	// The half that makes the correction real: without it the header keeps
	// showing the value the user just corrected.
	if got := orgColumn[string](ctx, t, e, orgID, "display_name"); got != corrected {
		t.Errorf("organization.display_name = %q, want %q — the sidecar moved and the record did not", got, corrected)
	}
}

func TestCorrectingAProfileFieldWithNoCanonicalColumnTouchesNoOrganizationColumn(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)
	nameBefore := orgColumn[string](ctx, t, e, orgID, "display_name")

	corrected := "Mid-market industrial manufacturers"
	if _, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "icp",
		ProfileFieldWriteInput{Value: &corrected}); err != nil {
		t.Fatalf("correct icp: %v", err)
	}

	if got := orgColumn[string](ctx, t, e, orgID, "display_name"); got != nameBefore {
		t.Errorf("display_name moved to %q while correcting icp — a field with no column must write only its sidecar", got)
	}
}

func TestConfirmingAProfileFieldKeepsTheValueAndTheMachinesEvidence(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	out, err := e.store.ConfirmOrganizationProfileField(ctx, orgID, "icp", ProfileFieldWriteInput{})
	if err != nil {
		t.Fatalf("confirm icp: %v", err)
	}

	if out.Value != "Energy-intensive manufacturers" {
		t.Errorf("value = %q, want it unchanged — a confirmation agrees, it does not edit", out.Value)
	}
	if string(out.Source) != "human" {
		t.Errorf("source = %q, want human", out.Source)
	}
	// The proposal is not overwritten by the agreement: a reader must still be
	// able to see what the machine read and how sure it was.
	if out.EvidenceSnippet == nil || *out.EvidenceSnippet == "" {
		t.Error("the extraction's snippet was dropped by the confirmation")
	}
	if out.Confidence == nil {
		t.Error("the extraction's confidence was dropped by the confirmation")
	}
	if out.VerifiedAt == nil || out.VerifiedBy == nil {
		t.Fatal("a confirmation that records nobody is not a confirmation")
	}
	if *out.VerifiedBy != e.rep.String() {
		t.Errorf("verified_by = %v, want the calling rep %v", *out.VerifiedBy, e.rep)
	}
	if time.Since(*out.VerifiedAt) > time.Hour {
		t.Errorf("verified_at = %v, want the transaction's own clock", *out.VerifiedAt)
	}
}

func TestAProfileFieldCorrectionWithoutAValueIsRefused(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	_, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "icp", ProfileFieldWriteInput{})
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("got %v, want a refusal pointing at the confirm operation", err)
	}
}

func TestAStaleProfileFieldVersionIsRefusedRatherThanOverwriting(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	first := "Mid-market industrial manufacturers"
	if _, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "icp",
		ProfileFieldWriteInput{Value: &first}); err != nil {
		t.Fatalf("first correction: %v", err)
	}

	// Version 1 was current before that write; a second editor still holding it
	// must be told, not silently allowed to win.
	stale := int64(1)
	second := "Anything at all"
	_, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "icp",
		ProfileFieldWriteInput{Value: &second, IfVersion: &stale})
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("got %v, want version skew", err)
	}
	if got := readProfileFieldValue(ctx, t, e, orgID, "icp"); got != first {
		t.Errorf("value = %q, want %q — the refused write must not have landed", got, first)
	}
}

func readProfileFieldValue(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID, field string) string {
	t.Helper()
	var v string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT value FROM organization_profile_field
			 WHERE organization_id = $1 AND field = $2`, orgID, field).Scan(&v)
	}); err != nil {
		t.Fatalf("read profile field %s: %v", field, err)
	}
	return v
}

// Every company fact keys on the empty value_key, so a write that located a row
// by value_key alone would reach whichever of them the scan returned first —
// correcting the phone number by overwriting the founding year.
func TestCorrectingOneSingleValueFactLeavesItsSiblingsAlone(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "+49 30 9999"
	out, err := e.store.UpdateOrganizationFact(ctx, orgID, "phone:", FactWriteInput{Value: &corrected})
	if err != nil {
		t.Fatalf("correct phone: %v", err)
	}
	if string(out.Field) != "phone" || out.Value != corrected {
		t.Fatalf("the correction landed on %s = %q, want phone = %q", out.Field, out.Value, corrected)
	}
	if got := factValue(ctx, t, e, orgID, "founded_year", ""); got != "1998" {
		t.Errorf("founded_year = %q, want 1998 — correcting the phone rewrote a different fact", got)
	}
}

func TestAMultiValueFactIsAddressedByItsValueKey(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "Acme Incorporated"
	out, err := e.store.UpdateOrganizationFact(ctx, orgID, "named_customer:acme-inc",
		FactWriteInput{Value: &corrected})
	if err != nil {
		t.Fatalf("correct named_customer:acme-inc: %v", err)
	}
	if out.Value != corrected || out.ValueKey != "acme-inc" {
		t.Fatalf("landed on value_key %q = %q, want acme-inc = %q", out.ValueKey, out.Value, corrected)
	}
	if got := factValue(ctx, t, e, orgID, "named_customer", "brandt-ag"); got != "Brandt AG" {
		t.Errorf("the sibling named_customer became %q — one key must name one row", got)
	}
}

func TestAFactKeyMissingItsValueKeySeparatorIsRefusedAsMalformed(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "+49 30 9999"
	_, err := e.store.UpdateOrganizationFact(ctx, orgID, "phone", FactWriteInput{Value: &corrected})

	var parse *values.ParseError
	if !errors.As(err, &parse) {
		t.Fatalf("got %v, want a 422 naming the malformed key — a not-found would read as though the fact had been deleted", err)
	}
	if parse.Code != "fact_key_malformed" {
		t.Errorf("code = %q, want fact_key_malformed", parse.Code)
	}
}

func TestAFactKeyNamingNoRowIsNotFound(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "Nobody"
	_, err := e.store.UpdateOrganizationFact(ctx, orgID, "named_customer:never-existed",
		FactWriteInput{Value: &corrected})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("got %v, want not found", err)
	}
}

func TestConfirmingAFactKeepsTheExtractionsClaimInTheAuditTrail(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	if _, err := e.store.ConfirmOrganizationFact(ctx, orgID, "phone:", FactWriteInput{}); err != nil {
		t.Fatalf("confirm phone: %v", err)
	}

	var before map[string]any
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT before FROM audit_log
			 WHERE entity_type = 'organization_fact'
			 ORDER BY id DESC LIMIT 1`).Scan(&before)
	}); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	// The row now reads as human. What the machine claimed, and how sure it was,
	// has to survive somewhere or the confirmation erased its own reason.
	if before["source"] != "site_read" {
		t.Errorf("audited before-source = %v, want site_read", before["source"])
	}
	if before["evidence_snippet"] == nil {
		t.Error("the audit's before image dropped the extraction's snippet")
	}
	if before["confidence"] == nil {
		t.Error("the audit's before image dropped the extraction's confidence")
	}
}
