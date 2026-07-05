// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// scrapeCompany (EP05): the enrich verb on a KNOWN org. The shared no-guess
// gate keeps only evidence-grounded fields, the surviving fields stage a 🟡
// approval BOUND to the org (nothing touches the record), an org the caller
// cannot see is existence-hidden (404), and acceptance fills only the org's
// empty fields as agent:scrape — exactly once.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

var scrapePerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

// acmeExtraction is the model reply used across the scrape tests: two fields
// whose evidence is verbatim in acmePage survive; a hallucinated snippet, an
// out-of-range confidence and an unknown field name are dropped.
const acmeExtraction = `{"fields":[
	{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9},
	{"field":"icp","value":"RevOps at SaaS scale-ups","evidence_snippet":"Built for RevOps leaders at scaling SaaS companies","confidence":0.7},
	{"field":"legal_name","value":"Acme GmbH","evidence_snippet":"this text is NOT on the page","confidence":0.9},
	{"field":"industry","value":"Software","evidence_snippet":"Acme GmbH","confidence":1.7},
	{"field":"made_up_field","value":"x","evidence_snippet":"Acme GmbH","confidence":0.5}]}`

// insertOrg creates an org owned by owner, optionally with a domain and a
// human-set industry, and returns its id.
func insertOrg(t *testing.T, e *authzEnv, owner ids.UUID, domain, industry string) ids.UUID {
	t.Helper()
	orgID := ids.NewV7()
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, owner_id, display_name, industry, source, captured_by)
			VALUES ($1, $2, $3, 'Acme', NULLIF($4,''), 'manual', 'human:owner')`,
			orgID, e.ws, owner, industry); err != nil {
			return err
		}
		if domain == "" {
			return nil
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, $3, true, 'manual', 'human:owner')`, e.ws, orgID, domain)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return orgID
}

func TestScrapeStagesEnrichmentBoundToOrg(t *testing.T) {
	e := setupAuthz(t)
	orgID := insertOrg(t, e, e.rep1, "acme.example", "")
	brain := ai.NewFakeClient().Script(acmeExtraction)
	engine := &scrapeEngine{extract: evidenceExtractor{fetch: acmePage, brain: brain}, people: e.people, approvals: approvals.NewService(e.pool)}

	proposal, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	if ids.UUID(proposal.OrganizationId) != orgID {
		t.Fatalf("proposal bound to %s, want the target org %s", ids.UUID(proposal.OrganizationId), orgID)
	}
	if proposal.SourceUrl != "https://acme.example" {
		t.Fatalf("source url = %q, want the org's own domain", proposal.SourceUrl)
	}
	if len(proposal.Fields) != 2 {
		t.Fatalf("gate let %d fields through, want 2 (hallucinated evidence, bad confidence, unknown name drop): %+v", len(proposal.Fields), proposal.Fields)
	}
	if proposal.Status != "staged" || proposal.ProposalId.String() == ids.Nil.String() {
		t.Fatalf("proposal not staged: %+v", proposal)
	}

	// The staged row is bound to the org and emitted the enrichment event.
	var kind, status, targetType string
	var targetID ids.UUID
	var eventCount int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT kind, status, coalesce(target_entity_type,''), coalesce(target_entity_id, $2)
			 FROM approval WHERE id = $1`, ids.UUID(proposal.ProposalId), ids.Nil).Scan(&kind, &status, &targetType, &targetID); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'approval.requested'`).Scan(&eventCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "enrich" || status != "pending" || eventCount != 1 {
		t.Fatalf("staging landed kind=%s status=%s approval.requested=%d, want enrich/pending/1", kind, status, eventCount)
	}
	if targetType != "organization" || targetID != orgID {
		t.Fatalf("approval not bound to the org (target %s/%s)", targetType, targetID)
	}
}

func TestScrapeHidesAnInvisibleOrg(t *testing.T) {
	e := setupAuthz(t)
	// Owned by rep3 (team2) — invisible to rep1 (team1) under team row-scope.
	hidden := insertOrg(t, e, e.rep3, "hidden.example", "")
	brain := ai.NewFakeClient().Script(acmeExtraction)
	engine := &scrapeEngine{extract: evidenceExtractor{fetch: acmePage, brain: brain}, people: e.people, approvals: approvals.NewService(e.pool)}

	// Both the domain path and the override path must 404 an org the caller
	// cannot see — existence-hiding, before any egress on their behalf.
	if _, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), hidden, ""); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("enrich a hidden org (domain path) → %v, want ErrNotFound", err)
	}
	if _, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), hidden, "https://attacker.example"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("enrich a hidden org (override path) → %v, want ErrNotFound", err)
	}
	// A never-existed id is 404 too (same EnsureVisible path).
	if _, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), ids.NewV7(), ""); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("enrich a nonexistent org → %v, want ErrNotFound", err)
	}
}

func TestScrapeDegradesHonestly(t *testing.T) {
	e := setupAuthz(t)
	// (a) A visible org with a domain but nothing survives the gate → unreadable.
	orgID := insertOrg(t, e, e.rep1, "acme.example", "")
	allHallucinated := ai.NewFakeClient().Script(
		`{"fields":[{"field":"icp","value":"guessed","evidence_snippet":"nowhere on the page","confidence":0.9}]}`)
	engine := &scrapeEngine{extract: evidenceExtractor{fetch: acmePage, brain: allHallucinated}, people: e.people, approvals: approvals.NewService(e.pool)}
	var unreadable *unreadableError
	if _, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), orgID, ""); !errors.As(err, &unreadable) {
		t.Fatalf("all-hallucinated extraction → %v, want unreadable", err)
	}

	// (b) A visible org with NO domain and no override → no target to read.
	noDomain := insertOrg(t, e, e.rep1, "", "")
	if _, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), noDomain, ""); !errors.Is(err, people.ErrNoEnrichTarget) {
		t.Fatalf("org without a domain → %v, want ErrNoEnrichTarget", err)
	}
}

func TestScrapeAcceptFillsOnlyEmptyFields(t *testing.T) {
	e := setupAuthz(t)
	// Human already set the industry; legal_name is empty.
	orgID := insertOrg(t, e, e.rep1, "acme.example", "Handcrafted Industry")
	brain := ai.NewFakeClient().Script(acmeExtraction, acmeExtraction)

	svc := approvals.NewService(e.pool)
	svc.WithEffect("enrich", scrapeAcceptEffect(svc, e.people))
	engine := &scrapeEngine{extract: evidenceExtractor{fetch: acmePage, brain: brain}, people: e.people, approvals: svc}

	proposal, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(e.as(e.rep2, nil, adminPerms), ids.UUID(proposal.ProposalId), true, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}

	var industry, capturedBy string
	var profileRows, orgs int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT industry FROM organization WHERE id = $1`, orgID).Scan(&industry); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM organization`).Scan(&orgs); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*), max(captured_by) FROM organization_profile_field WHERE organization_id = $1`,
			orgID).Scan(&profileRows, &capturedBy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if orgs != 1 {
		t.Fatalf("enrichment created a duplicate org (%d rows) instead of targeting the named one", orgs)
	}
	if industry != "Handcrafted Industry" {
		t.Fatalf("accept OVERWROTE a human-set industry: %q", industry)
	}
	if profileRows != 2 || capturedBy != "agent:scrape" {
		t.Fatalf("evidence rows = %d captured_by=%q, want 2 as agent:scrape", profileRows, capturedBy)
	}

	// Exactly-once: the approval is consumed and a re-decide is refused.
	var already *approvals.AlreadyDecidedError
	if _, err := svc.Decide(e.as(e.rep2, nil, adminPerms), ids.UUID(proposal.ProposalId), true, nil); !errors.As(err, &already) {
		t.Fatalf("re-decide → %v, want AlreadyDecided", err)
	}

	// A REJECTED enrichment writes nothing.
	proposal2, err := engine.Propose(e.as(e.rep1, []ids.UUID{e.team1}, scrapePerms), orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(e.as(e.rep2, nil, adminPerms), ids.UUID(proposal2.ProposalId), false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	var rejectedRows int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM organization_profile_field WHERE organization_id = $1`, orgID).Scan(&rejectedRows)
	})
	if err != nil || rejectedRows != 2 {
		t.Fatalf("reject changed the profile rows to %d (err=%v), want the 2 from the accepted proposal", rejectedRows, err)
	}
}
