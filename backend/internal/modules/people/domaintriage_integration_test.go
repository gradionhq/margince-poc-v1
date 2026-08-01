// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The triage verdict over a real Postgres: what a company answer creates, what
// a personal answer refuses, and that neither can be undone by the next message
// from the same domain.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// openTriage puts a domain in the state the ensure ladder leaves it: the person
// exists, the organization question is open, and no company row was invented.
func (e *dedupeEnv) openTriage(ctx context.Context, t *testing.T, email, display, domain string) EnsureCounterpartyResult {
	t.Helper()
	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, email, display, domain))
	if err != nil {
		t.Fatalf("ensure %s: %v", email, err)
	}
	if !res.TriagePending {
		t.Fatalf("ensure %s = %+v, want the triage question opened", email, res)
	}
	return res
}

// startTriageRead creates the dossier a verdict resolves against.
func (e *dedupeEnv) startTriageRead(ctx context.Context, t *testing.T, domain string) ids.UUID {
	t.Helper()
	read, _, err := e.store.StartDomainTriageSiteRead(ctx, domain, "system:domain_triage", nil)
	if err != nil {
		t.Fatalf("starting the triage read for %s: %v", domain, err)
	}
	return read.ID
}

func TestCompanyVerdictCreatesTheOrganizationAndWiresEveryoneWaitingOnIt(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// Two colleagues wrote in while the question was open. Both have people
	// rows and neither has an employer.
	first := e.openTriage(ctx, t, "manuel@basecom.test", "Manuel Wortmann", "basecom.test")
	second := e.openTriage(ctx, t, "petra@basecom.test", "Petra Klein", "basecom.test")

	readID := e.startTriageRead(ctx, t, "basecom.test")
	res, err := e.store.ResolveDomainTriage(ctx, ResolveDomainTriageInput{
		Domain: "basecom.test", Status: DomainCompany, Source: DomainSourceSiteRead,
		Evidence: "the site states a legal entity", ReadID: readID,
		DossierName: "basecom GmbH", SeedURL: "https://basecom.test",
		Fields: []DeepReadField{{
			Field: "display_name", Value: "basecom GmbH", EvidenceSnippet: "basecom GmbH",
			SourceURL: "https://basecom.test", Confidence: 0.9,
		}},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.OrgCreated || res.OrganizationID == nil {
		t.Fatalf("resolve = %+v, want the organization created", res)
	}
	// Both waiting people get their edge, not only the one that happened to
	// trigger the crawl.
	if res.EdgesPlanted != 2 {
		t.Fatalf("resolve planted %d employment edges, want 2", res.EdgesPlanted)
	}

	var name, nameSource, status string
	var employed int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT display_name, name_source FROM organization WHERE id = $1`,
			res.OrganizationID).Scan(&name, &nameSource); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT status FROM organization_domain_disposition WHERE domain = 'basecom.test'`).Scan(&status); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM relationship
			WHERE organization_id = $1 AND kind = 'employment' AND is_current_primary
			  AND person_id = ANY($2)`,
			res.OrganizationID, []ids.PersonID{first.PersonID, second.PersonID}).Scan(&employed)
	}); err != nil {
		t.Fatal(err)
	}
	// The site stated the name, so the row is born with it rather than with a
	// title-cased domain label — and says so, which is what stops a later
	// dossier overwriting it.
	if name != "basecom GmbH" || nameSource != nameSourceDossier {
		t.Fatalf("organization = %q/%s, want the dossier-stated name", name, nameSource)
	}
	if status != DomainCompany {
		t.Fatalf("disposition = %q, want %q", status, DomainCompany)
	}
	if employed != 2 {
		t.Fatalf("%d of the waiting people were employed, want 2", employed)
	}

	// The next message from the domain attaches to the organization the verdict
	// made, and asks nothing further.
	third, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "rolf@basecom.test", "Rolf Adam", "basecom.test"))
	if err != nil {
		t.Fatalf("ensure after the verdict: %v", err)
	}
	if third.TriagePending {
		t.Fatal("a settled domain must not re-open its question")
	}
	if third.OrganizationID == nil || third.OrganizationID.UUID != res.OrganizationID.UUID {
		t.Fatalf("ensure after the verdict = %+v, want the verdict's organization", third)
	}
}

func TestPersonalVerdictRefusesTheCompanyForGood(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// The case that started this: a man's own domain, carrying his name.
	e.openTriage(ctx, t, "sebastian@herpertz.test", "Sebastian Herpertz", "herpertz.test")
	readID := e.startTriageRead(ctx, t, "herpertz.test")

	if _, err := e.store.ResolveDomainTriage(ctx, ResolveDomainTriageInput{
		Domain: "herpertz.test", Status: DomainPersonal, Source: DomainSourceSiteRead,
		Evidence: "the site is a personal page naming the domain's owner", ReadID: readID,
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var orgs int
	var status string
	var nextAttempt *string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM organization_domain WHERE domain = 'herpertz.test'`).Scan(&orgs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT status, next_attempt_at::text FROM organization_domain_disposition
			WHERE domain = 'herpertz.test'`).Scan(&status, &nextAttempt)
	}); err != nil {
		t.Fatal(err)
	}
	if orgs != 0 {
		t.Fatalf("%d organizations on a personal domain, want 0", orgs)
	}
	if status != DomainPersonal {
		t.Fatalf("disposition = %q, want %q", status, DomainPersonal)
	}
	// A settled verdict leaves the sweep's due scan for good; otherwise the
	// refusal would be re-crawled every week forever.
	if nextAttempt != nil {
		t.Fatalf("a settled verdict is still due at %v, want never", *nextAttempt)
	}

	// The refusal has to survive the next message, or it buys nothing.
	again, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "post@herpertz.test", "Sebastian Herpertz", "herpertz.test"))
	if err != nil {
		t.Fatalf("ensure after the verdict: %v", err)
	}
	if again.TriagePending || again.OrgCreated || again.OrganizationID != nil {
		t.Fatalf("ensure after a personal verdict = %+v, want person only, no company, no new question", again)
	}
	if !again.PersonCreated {
		t.Fatal("refusing the company must not refuse the person")
	}
}

func TestCompanyVerdictAdoptsAnOrganizationAHumanCreatedMidTriage(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	e.openTriage(ctx, t, "ceo@midtriage.test", "Some One", "midtriage.test")
	readID := e.startTriageRead(ctx, t, "midtriage.test")

	// While the crawl ran, a human typed the company in.
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Mid Triage AG", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "midtriage.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.store.ResolveDomainTriage(ctx, ResolveDomainTriageInput{
		Domain: "midtriage.test", Status: DomainCompany, Source: DomainSourceSiteRead,
		ReadID: readID, DossierName: "Mid Triage Aktiengesellschaft", SeedURL: "https://midtriage.test",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.OrgCreated {
		t.Fatal("the verdict created a second organization for a domain a human had already claimed")
	}
	if res.OrganizationID == nil || res.OrganizationID.UUID != ids.UUID(org.Id) {
		t.Fatalf("resolve = %+v, want the human's organization %s adopted", res, org.Id)
	}

	var name string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT display_name FROM organization WHERE id = $1`, org.Id).Scan(&name)
	}); err != nil {
		t.Fatal(err)
	}
	if name != "Mid Triage AG" {
		t.Fatalf("organization = %q — a verdict must never rename what a human typed", name)
	}
}

func TestResolvingADomainNobodyAskedAboutIsRefused(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// Nothing opened a question for this domain, so there is no verdict to
	// settle. Creating rows anyway would be a company nobody's mail justified.
	if _, err := e.store.ResolveDomainTriage(ctx, ResolveDomainTriageInput{
		Domain: "unasked.test", Status: DomainCompany, Source: DomainSourceSiteRead,
		DossierName: "Unasked Ltd", SeedURL: "https://unasked.test",
	}); err == nil {
		t.Fatal("resolving a domain with no open question must refuse, not create")
	}
}
