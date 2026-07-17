// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// PO-F-1/PO-F-2 over a real migrated Postgres: the exact tier finds a
// claimed email or org domain deterministically; the fuzzy tier scores
// the spec's own worked examples to the decimal and lands them on the
// right side of DEDUPE_REVIEW_THRESHOLD; a nameless candidate never
// fuzzy-matches; and the trigram candidate restriction still admits the
// pairs the formula must see.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type dedupeEnv struct {
	store *Store
	ws    ids.UUID
	rep   ids.UUID
}

func setupDedupe(t *testing.T) *dedupeEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	e := &dedupeEnv{ws: ids.NewV7(), rep: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Dedupe', $2, 'EUR')`,
		e.ws, "dd-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		e.rep, e.ws, "rep-"+e.rep.String()+"@dd.test"); err != nil {
		t.Fatal(err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.store = NewStore(pool)
	return e
}

func (e *dedupeEnv) as() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"person":       {Create: true, Read: true, Update: true},
				"organization": {Create: true, Read: true, Update: true},
				"relationship": {Create: true, Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// seedEmployedPerson creates a person with the given email employed at a
// fresh org that owns domain — the incumbent every case probes against.
func (e *dedupeEnv) seedEmployedPerson(t *testing.T, ctx context.Context, name, email, orgName, domain string) (ids.PersonID, ids.OrganizationID) {
	t.Helper()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: orgName, Source: "manual",
		Domains: []OrgDomainInput{{Domain: domain, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seed org %s: %v", orgName, err)
	}
	person, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: name, Source: "manual",
		Emails: []PersonEmailInput{{Email: email, EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seed person %s: %v", name, err)
	}
	personID := ids.From[ids.PersonKind](ids.UUID(person.Id))
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))
	if _, err := e.store.CreateRelationship(ctx, CreateRelationshipInput{
		Kind: "employment", PersonID: &personID, OrganizationID: &orgID,
		IsCurrentPrimary: true, Source: "manual",
	}); err != nil {
		t.Fatalf("seed employment: %v", err)
	}
	return personID, orgID
}

// dedupeInTx runs the resolver inside one workspace transaction, the way
// every create-path caller will.
func (e *dedupeEnv) dedupeInTx(t *testing.T, ctx context.Context, c PersonCandidate) PersonMatch {
	t.Helper()
	var m PersonMatch
	err := e.store.tx(ctx, func(tx pgx.Tx) (err error) {
		m, err = DedupePerson(ctx, tx, c)
		return err
	})
	if err != nil {
		t.Fatalf("DedupePerson: %v", err)
	}
	return m
}

func (e *dedupeEnv) dedupeOrgInTx(t *testing.T, ctx context.Context, c OrganizationCandidate) OrganizationMatch {
	t.Helper()
	var m OrganizationMatch
	err := e.store.tx(ctx, func(tx pgx.Tx) (err error) {
		m, err = DedupeOrganization(ctx, tx, c)
		return err
	})
	if err != nil {
		t.Fatalf("DedupeOrganization: %v", err)
	}
	return m
}

func TestDedupePersonExactTierFindsAClaimedEmail(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	janeID, _ := e.seedEmployedPerson(t, ctx, "Jane Doe", "jane.doe@acme.test", "Acme GmbH", "acme.test")

	// The spec's first worked example: a live email row already holds the
	// address → EXACT_COLLISION + the existing id. No scoring runs, so the
	// name being wildly different must not matter.
	m := e.dedupeInTx(t, ctx, PersonCandidate{
		FullName: "Completely Different", Emails: []string{"JANE.DOE@ACME.TEST"},
	})
	if m.Decision != DecisionExactCollision {
		t.Fatalf("decision = %s, want exact_collision", m.Decision)
	}
	if m.PersonID != janeID {
		t.Fatalf("matched %s, want the incumbent %s", m.PersonID, janeID)
	}
}

func TestDedupePersonFuzzyTierReproducesTheSpecWorkedExamples(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	johnID, acmeID := e.seedEmployedPerson(t, ctx, "John Doe", "john.doe@acme.test", "Acme GmbH", "acme.test")

	t.Run("same employer queues for review at 0.982", func(t *testing.T) {
		m := e.dedupeInTx(t, ctx, PersonCandidate{
			FullName: "Jon Doe", Emails: []string{"j.doe@other.test"},
			CurrentPrimaryOrgID: &acmeID,
		})
		if m.Decision != DecisionFuzzyReview {
			t.Fatalf("decision = %s (confidence %.4f), want fuzzy_review", m.Decision, m.Confidence)
		}
		if m.PersonID != johnID {
			t.Fatalf("matched %s, want %s", m.PersonID, johnID)
		}
		if !closeToPrinted(m.Confidence, 0.982) {
			t.Fatalf("confidence = %.4f, spec pins 0.982", m.Confidence)
		}
	})

	t.Run("shared email domain scores the 0.8 tier and still queues", func(t *testing.T) {
		// No employer known for the candidate, but the address sits on
		// acme.test, which the incumbent's org owns:
		// 0.55·0.9667 + 0.45·0.8 = 0.8917 ≥ 0.72.
		m := e.dedupeInTx(t, ctx, PersonCandidate{
			FullName: "Jon Doe", Emails: []string{"jon@acme.test"},
		})
		if m.Decision != DecisionFuzzyReview {
			t.Fatalf("decision = %s (confidence %.4f), want fuzzy_review", m.Decision, m.Confidence)
		}
		if !closeToPrinted(m.Confidence, 0.8917) {
			t.Fatalf("confidence = %.4f, want 0.8917 (name 0.9667 × 0.55 + domain 0.8 × 0.45)", m.Confidence)
		}
	})

	t.Run("different employer creates at 0.532", func(t *testing.T) {
		// The spec's example: Jon Doe at Globex vs John Doe at Acme, and
		// nobody else at Globex — an employee there would join the
		// candidate set with the org_match = 1.0 boost and change the case.
		globex, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
			DisplayName: "Globex AG", Source: "manual",
			Domains: []OrgDomainInput{{Domain: "globex.test", IsPrimary: true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		globexID := ids.From[ids.OrganizationKind](ids.UUID(globex.Id))
		m := e.dedupeInTx(t, ctx, PersonCandidate{
			FullName: "Jon Doe", Emails: []string{"jon@nowhere.test"},
			CurrentPrimaryOrgID: &globexID,
		})
		if m.Decision != DecisionNoMatch {
			t.Fatalf("decision = %s (confidence %.4f), want no_match — the spec creates here", m.Decision, m.Confidence)
		}
	})
}

func TestDedupePersonNamelessCandidateNeverFuzzyMatches(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	_, acmeID := e.seedEmployedPerson(t, ctx, "John Doe", "john@nameless.test", "Nameless GmbH", "nameless.test")

	// PO-F-1 edge case: empty name → fuzzy tier skipped, exact-email only.
	// Sharing the employer must not conjure a match from org_match alone.
	m := e.dedupeInTx(t, ctx, PersonCandidate{
		FullName: "  ", Emails: []string{"unknown@elsewhere.test"},
		CurrentPrimaryOrgID: &acmeID,
	})
	if m.Decision != DecisionNoMatch {
		t.Fatalf("decision = %s, want no_match — a nameless captured contact never fuzzy-matches", m.Decision)
	}
}

func TestDedupeOrganizationExactTierFindsAClaimedDomain(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Initech GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "initech.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The capture employer-inference path: a domain hit lands on the
	// existing org regardless of what the sender calls the company.
	m := e.dedupeOrgInTx(t, ctx, OrganizationCandidate{
		DisplayName: "Some Other Spelling", Domains: []string{"INITECH.TEST"},
	})
	if m.Decision != DecisionExactCollision {
		t.Fatalf("decision = %s, want exact_collision", m.Decision)
	}
	if ids.UUID(org.Id) != m.OrganizationID.UUID {
		t.Fatalf("matched %s, want %s", m.OrganizationID, org.Id)
	}
}

func TestDedupeOrganizationFuzzyTierMeetsAcrossLegalSuffixes(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Wayne Enterprises GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "wayne.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// PO-F-2's worked example shape: same name, different legal suffix,
	// no shared domain → normalize-equal → 1.0 → 🟡 review, never a merge.
	m := e.dedupeOrgInTx(t, ctx, OrganizationCandidate{
		DisplayName: "Wayne Enterprises Inc", Domains: []string{"wayne-us.test"},
	})
	if m.Decision != DecisionFuzzyReview {
		t.Fatalf("decision = %s (confidence %.4f), want fuzzy_review", m.Decision, m.Confidence)
	}
	if ids.UUID(org.Id) != m.OrganizationID.UUID {
		t.Fatalf("matched %s, want %s", m.OrganizationID, org.Id)
	}
	if m.Confidence != 1 {
		t.Fatalf("confidence = %.4f, want an exact 1.0 after suffix normalization", m.Confidence)
	}

	// And a genuinely unrelated name creates.
	unrelated := e.dedupeOrgInTx(t, ctx, OrganizationCandidate{
		DisplayName: "Zorbatron Heavy Industry", Domains: []string{"zorbatron.test"},
	})
	if unrelated.Decision != DecisionNoMatch {
		t.Fatalf("decision = %s, want no_match", unrelated.Decision)
	}
}
