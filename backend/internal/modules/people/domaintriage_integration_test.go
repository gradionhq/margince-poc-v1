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
//
// Only the FIRST sender on a domain reports TriagePending — the question is
// opened once and one crawl answers it, however many colleagues write in.
func (e *dedupeEnv) openTriage(ctx context.Context, t *testing.T, email, display, domain string) EnsureCounterpartyResult {
	t.Helper()
	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, email, display, domain))
	if err != nil {
		t.Fatalf("ensure %s: %v", email, err)
	}
	if res.OrganizationID != nil {
		t.Fatalf("ensure %s = %+v, want NO company from an unjudged domain", email, res)
	}
	var open int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM organization_domain_disposition
			WHERE domain = $1 AND status = 'pending'`, domain).Scan(&open)
	}); err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Fatalf("%d open questions for %s after ensuring %s, want exactly 1", open, domain, email)
	}
	return res
}

// openTriageFirst is openTriage for the sender that OPENS the question, and
// additionally asserts that it was this ensure that opened it — the signal the
// trigger and the backfill counter both key on.
func (e *dedupeEnv) openTriageFirst(ctx context.Context, t *testing.T, email, display, domain string) EnsureCounterpartyResult {
	t.Helper()
	res := e.openTriage(ctx, t, email, display, domain)
	if !res.TriagePending || res.TriageDomain != domain {
		t.Fatalf("ensure %s = %+v, want the triage question reported as opened", email, res)
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
	first := e.openTriageFirst(ctx, t, "martin@basecom.test", "Martin Weiss", "basecom.test")
	second := e.openTriage(ctx, t, "petra@basecom.test", "Petra Klein", "basecom.test")
	if second.TriagePending {
		t.Fatal("the second sender on a domain must not re-open a question that is already open")
	}

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
	e.openTriageFirst(ctx, t, "sebastian@kestner.test", "Sebastian Kestner", "kestner.test")
	readID := e.startTriageRead(ctx, t, "kestner.test")

	if _, err := e.store.ResolveDomainTriage(ctx, ResolveDomainTriageInput{
		Domain: "kestner.test", Status: DomainPersonal, Source: DomainSourceSiteRead,
		Evidence: "the site is a personal page naming the domain's owner", ReadID: readID,
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var orgs int
	var status string
	var nextAttempt *string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM organization_domain WHERE domain = 'kestner.test'`).Scan(&orgs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT status, next_attempt_at::text FROM organization_domain_disposition
			WHERE domain = 'kestner.test'`).Scan(&status, &nextAttempt)
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
	again, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "post@kestner.test", "Sebastian Kestner", "kestner.test"))
	if err != nil {
		t.Fatalf("ensure after the verdict: %v", err)
	}
	if again.TriagePending || again.OrganizationID != nil {
		t.Fatalf("ensure after a personal verdict = %+v, want person only, no company, no new question", again)
	}
	if !again.PersonCreated {
		t.Fatal("refusing the company must not refuse the person")
	}
}

func TestCompanyVerdictAdoptsAnOrganizationAHumanCreatedMidTriage(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	e.openTriageFirst(ctx, t, "ceo@midtriage.test", "Some One", "midtriage.test")
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

func TestListDueDomainsOffersOnlyTheQuestionsWorthAsking(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// Four domains in four states. Only the first is worth a crawl.
	e.openTriageFirst(ctx, t, "a@waiting.test", "A Person", "waiting.test")
	e.openTriageFirst(ctx, t, "b@inflight.test", "B Person", "inflight.test")
	e.openTriageFirst(ctx, t, "c@settled.test", "C Person", "settled.test")
	e.openTriageFirst(ctx, t, "d@exhausted.test", "D Person", "exhausted.test")

	// inflight.test already has a read running: queueing a second would spend a
	// second slot of the day's budget on one crawl.
	e.startTriageRead(ctx, t, "inflight.test")
	// settled.test has its answer.
	settledRead := e.startTriageRead(ctx, t, "settled.test")
	if _, err := e.store.ResolveDomainTriage(ctx, ResolveDomainTriageInput{
		Domain: "settled.test", Status: DomainPersonal, Source: DomainSourceSiteRead,
		Evidence: "a personal page", ReadID: settledRead,
	}); err != nil {
		t.Fatal(err)
	}
	// exhausted.test used every attempt without an answer. A site that will not
	// load must not be re-crawled forever.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE organization_domain_disposition
			   SET attempts = $1, next_attempt_at = now() - interval '1 day'
			 WHERE domain = 'exhausted.test'`, domainTriageMaxAttempts)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	due, err := e.store.ListDueDomains(ctx, 50)
	if err != nil {
		t.Fatalf("ListDueDomains: %v", err)
	}
	if len(due) != 1 || due[0].Domain != "waiting.test" {
		t.Fatalf("due = %+v, want only waiting.test", due)
	}
}

func TestMarkTriageQueuedSpendsAnAttemptAndBacksOff(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	e.openTriageFirst(ctx, t, "a@backoff.test", "A Person", "backoff.test")

	if err := e.store.MarkTriageQueued(ctx, "backoff.test"); err != nil {
		t.Fatalf("MarkTriageQueued: %v", err)
	}

	var attempts int
	var due bool
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT attempts, next_attempt_at <= now() FROM organization_domain_disposition
			WHERE domain = 'backoff.test'`).Scan(&attempts, &due)
	}); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 spent", attempts)
	}
	// A worker that dies without answering costs a delay, never a hot loop.
	if due {
		t.Error("the domain is due again immediately — the backoff did not arm")
	}
}
