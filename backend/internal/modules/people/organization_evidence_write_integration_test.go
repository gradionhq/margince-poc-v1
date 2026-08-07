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
func orgColumn(ctx context.Context, t *testing.T, e *dedupeEnv, orgID ids.OrganizationID, column string) string {
	t.Helper()
	var v string
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
	if got := orgColumn(ctx, t, e, orgID, "display_name"); got != corrected {
		t.Errorf("organization.display_name = %q, want %q — the sidecar moved and the record did not", got, corrected)
	}
}

func TestCorrectingAProfileFieldWithNoCanonicalColumnTouchesNoOrganizationColumn(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)
	nameBefore := orgColumn(ctx, t, e, orgID, "display_name")

	corrected := "Mid-market industrial manufacturers"
	if _, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "icp",
		ProfileFieldWriteInput{Value: &corrected}); err != nil {
		t.Fatalf("correct icp: %v", err)
	}

	if got := orgColumn(ctx, t, e, orgID, "display_name"); got != nameBefore {
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
	// Not "verified_at is close to now" — a container clock an hour off the host would
	// fail that for a reason unrelated to the confirmation. What the field must
	// say is that the human looked at the claim AFTER the machine retrieved it.
	if out.RetrievedAt != nil && out.VerifiedAt.Before(*out.RetrievedAt) {
		t.Errorf("verified_at %v precedes retrieved_at %v — a human cannot have confirmed a claim before it was read",
			*out.VerifiedAt, *out.RetrievedAt)
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

// The refusal's shape is a unit test (organization_fact_write_test.go). What
// needs a database is that it happens before anything is written.
func TestAMalformedFactKeyIsRefusedBeforeTheWrite(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "+49 30 9999"
	var parse *values.ParseError
	if _, err := e.store.UpdateOrganizationFact(ctx, orgID, "phone",
		FactWriteInput{Value: &corrected}); !errors.As(err, &parse) {
		t.Fatalf("got %v, want a 422 naming the malformed key — a not-found would read as though the fact had been deleted", err)
	}
	if got := factValue(ctx, t, e, orgID, "phone", ""); got != "+49 30 1234" {
		t.Errorf("phone = %q — the refused correction landed anyway", got)
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

	// Filtered by the fact's own id, not "the newest organization_fact audit
	// row": the seed writes none today, so an unfiltered read passes by luck,
	// and would start asserting against a neighbour's audit the moment the
	// fixture seeds through a real writer.
	var before map[string]any
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT a.before FROM audit_log a
			  JOIN organization_fact f ON f.id = a.entity_id
			 WHERE a.entity_type = 'organization_fact'
			   AND f.organization_id = $1 AND f.field = 'phone'`, orgID).Scan(&before)
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

// Every assertion above holds even if the two sidecar reads dropped their
// workspace_id clause, because a single-workspace fixture cannot tell the
// difference. This one can: it corrects through a store bound to workspace A
// while the fact lives in workspace B.
func TestEvidenceOfAnotherWorkspacesOrganizationIsNotReachable(t *testing.T) {
	owner := setupDedupe(t)
	orgID := evidenceOrg(owner.as(), t, owner)

	stranger := setupDedupe(t)
	corrected := "+49 30 9999"
	_, err := stranger.store.UpdateOrganizationFact(stranger.as(), orgID, "phone:",
		FactWriteInput{Value: &corrected})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("got %v, want not found — another workspace's fact must be existence-hidden, never corrected", err)
	}
	if _, err := stranger.store.UpdateOrganizationProfileField(stranger.as(), orgID, "icp",
		ProfileFieldWriteInput{Value: &corrected}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("got %v, want not found", err)
	}
	if got := factValue(owner.as(), t, owner, orgID, "phone", ""); got != "+49 30 1234" {
		t.Errorf("phone = %q — a foreign correction reached the row", got)
	}
}

// PATCH /organizations/{id} refuses an archived record, so a correction routed
// through its receipt must refuse it too — otherwise the sidecar is a way to
// edit a company the ordinary path says is gone. The refusal does not depend on
// whether the field has a canonical column behind it.
func TestAnArchivedOrganizationsEvidenceIsReadableButNotCorrectable(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)
	// Archived by column rather than through ArchiveOrganization: the rep this
	// suite runs as holds no organization delete grant, and widening it would
	// change what every other test in the package is proving. The state under
	// test is the archived row, not how it got there.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE organization SET archived_at = now() WHERE id = $1`, orgID)
		return err
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Still readable: this is the record of what was known when it was retired.
	if _, err := e.store.ListOrganizationProfileFields(ctx, orgID); err != nil {
		t.Fatalf("an archived company's evidence must stay readable, got %v", err)
	}

	corrected := "Voltaq Systems GmbH & Co. KG"
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"a field with a canonical column", func() error {
			_, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "display_name",
				ProfileFieldWriteInput{Value: &corrected})
			return err
		}},
		{"a field with none", func() error {
			_, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "icp",
				ProfileFieldWriteInput{Value: &corrected})
			return err
		}},
		{"a fact", func() error {
			_, err := e.store.UpdateOrganizationFact(ctx, orgID, "phone:",
				FactWriteInput{Value: &corrected})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, apperrors.ErrNotFound) {
				t.Fatalf("got %v, want not found", err)
			}
		})
	}
	if got := orgColumn(ctx, t, e, orgID, "display_name"); got != "Voltaq Systems GmbH" {
		t.Errorf("display_name = %q — a refused correction rewrote an archived record", got)
	}
}

// A corrected display name is a rename, and a rename owes the record the same
// two things a rename through the edit form owes it: the provenance stamp that
// stops the next enrichment run overwriting the human, and the duplicate
// recheck that notices the new name colliding with an existing company.
func TestCorrectingTheDisplayNameIsTreatedAsAHumanRename(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := evidenceOrg(ctx, t, e)

	corrected := "Brandt Industrieservice GmbH"
	if _, err := e.store.UpdateOrganizationProfileField(ctx, orgID, "display_name",
		ProfileFieldWriteInput{Value: &corrected}); err != nil {
		t.Fatalf("correct display_name: %v", err)
	}
	if got := orgColumn(ctx, t, e, orgID, "name_source"); got != "human" {
		t.Errorf("name_source = %q, want human — an unstamped correction is overwritten by the next enrichment run", got)
	}
}
