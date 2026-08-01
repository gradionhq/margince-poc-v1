// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The domain-triage lane over a real Postgres and a fake site: what each
// verdict does to the database, and that the early exit really is early.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// triageBrainFake answers the classification call with one canned verdict and
// counts how many times it was asked. Non-triage calls fall through to the
// extraction fake, so one worker serves both lanes.
type triageBrainFake struct {
	verdict string
	calls   int
}

func (f *triageBrainFake) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	f.calls++
	return model.Response{Text: f.verdict}, nil
}

// newTriageTestWorker builds the deep-read worker with a separate triage lane,
// exactly as compose wires it: the classification never rides the profile
// lane's brain.
func newTriageTestWorker(e *integration.Env, site *fakeSite, extractBrain completer, triage completer) *siteDeepReadWorker {
	svc := approvals.NewService(e.Pool)
	svc.WithEffect(siteLeadProposalKind, siteLeadAcceptEffect(svc, newCaptureSink(e.Pool, CaptureConfig{})))
	return &siteDeepReadWorker{
		people:      e.People,
		crawler:     testSiteCrawler(site),
		extract:     evidenceExtractor{brain: extractBrain, factBrain: extractBrain},
		triageBrain: triage,
		approvals:   svc,
		authority:   identity.NewService(e.Pool),
		autoEnrich:  capture.NewAutoEnrichStore(e.Pool),
		settings:    capture.NewSettings(e.Pool),
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// openTriageQuestion puts a domain in the state the ensure ladder leaves it and
// starts its dossier, returning the job args the worker would receive.
func openTriageQuestion(t *testing.T, e *integration.Env, domain, email, display string) SiteDeepReadArgs {
	t.Helper()
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	activityID := ids.New[ids.ActivityKind]()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, direction, source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'email', 'hi', 'inbound', 'gmail', $3, 'gmail:seed', 'connector:gmail')`,
			activityID, e.WS, activityID.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.People.EnsureCounterparty(ctx, people.EnsureCounterpartyInput{
		Email: email, DisplayName: display, Domain: domain,
		OwnerID: e.Rep1, ActivityID: activityID,
		Source: "gmail:" + activityID.String(), CapturedBy: "connector:gmail",
	})
	if err != nil {
		t.Fatalf("ensure %s: %v", email, err)
	}
	if !res.TriagePending {
		t.Fatalf("ensure %s did not open the triage question: %+v", email, res)
	}

	read, _, err := e.People.StartDomainTriageSiteRead(ctx, domain, systemDomainTriageActor, nil)
	if err != nil {
		t.Fatalf("starting the triage read: %v", err)
	}
	return SiteDeepReadArgs{WorkspaceID: e.WS, SiteReadID: read.ID, SeedURL: read.SeedURL, RequestedBy: systemDomainTriageActor}
}

// triageState reads back what the run decided.
func triageState(t *testing.T, e *integration.Env, domain string) (status, readStatus string, orgs int) {
	t.Helper()
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT status FROM organization_domain_disposition WHERE domain = $1`, domain).Scan(&status); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT status FROM site_read WHERE target_kind = 'domain_triage' AND seed_url = $1`,
			people.TriageSeedURL(domain)).Scan(&readStatus); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM organization_domain WHERE domain = $1`, domain).Scan(&orgs)
	}); err != nil {
		t.Fatal(err)
	}
	return status, readStatus, orgs
}

func TestTriageStopsAtTheLandingPageForAPersonalDomain(t *testing.T) {
	e := integration.Setup(t)
	site := &fakeSite{pages: map[string]fakeSitePage{
		seedURL:                {text: readable("Sebastian Herpertz.") + " Eigentuemer der Domain ist Sebastian Herpertz. Kontakt per E-Mail."},
		seedURL + "/impressum": {text: readable("Impressum.") + " Sebastian Herpertz, Privatperson."},
	}}
	// The extraction brain would happily invent a company from this page. It
	// must never be asked: the classification stops the read first.
	extract := &triageBrainFake{verdict: `{"fields":[{"f":"display_name","v":"Herpertz","e":"s0","c":0.9}]}`}
	triage := &triageBrainFake{verdict: `{"kind":"personal","confidence":0.95,"reason":"the page names only the domain's owner"}`}

	args := openTriageQuestion(t, e, triageTestDomain, "sebastian@"+triageTestDomain, "Sebastian Herpertz")
	worker := newTriageTestWorker(e, site, extract, triage)
	if err := worker.run(e.As(e.Rep1, nil, integration.AdminPerms), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	status, readStatus, orgs := triageState(t, e, triageTestDomain)
	if status != people.DomainPersonal {
		t.Errorf("disposition = %q, want %q", status, people.DomainPersonal)
	}
	if orgs != 0 {
		t.Errorf("%d organizations on a personal domain, want 0", orgs)
	}
	// The saving the classifier exists to buy: one page, no crawl, no
	// extraction. If this ever reads 'done' the early exit is gone.
	if readStatus != "cancelled" {
		t.Errorf("read status = %q, want cancelled — the crawl was supposed to stop", readStatus)
	}
	if triage.calls != 1 {
		t.Errorf("%d classification calls, want exactly 1", triage.calls)
	}
	if extract.calls != 0 {
		t.Errorf("%d extraction calls on an aborted read, want 0 — the abort bought nothing", extract.calls)
	}
}

func TestTriageReadsOnAndCreatesTheCompanyTheSiteNames(t *testing.T) {
	e := integration.Setup(t)
	triage := &triageBrainFake{verdict: `{"kind":"company","confidence":0.95,"reason":"the page sells a product"}`}

	args := openTriageQuestion(t, e, triageTestDomain, "manuel@"+triageTestDomain, "Manuel Wortmann")
	worker := newTriageTestWorker(e, acmeDeepSite(), acmeDeepBrain(), triage)
	if err := worker.run(e.As(e.Rep1, nil, integration.AdminPerms), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	status, readStatus, orgs := triageState(t, e, triageTestDomain)
	if status != people.DomainCompany {
		t.Fatalf("disposition = %q, want %q", status, people.DomainCompany)
	}
	if orgs != 1 {
		t.Fatalf("%d organizations for a company domain, want 1", orgs)
	}
	if readStatus != "done" && readStatus != "partial" {
		t.Errorf("read status = %q, want a completed read", readStatus)
	}

	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	var name, nameSource string
	var employments int
	var boundToOrg bool
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT o.display_name, o.name_source FROM organization o
			JOIN organization_domain d ON d.organization_id = o.id
			WHERE d.domain = $1`, triageTestDomain).Scan(&name, &nameSource); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM relationship r
			JOIN organization_domain d ON d.organization_id = r.organization_id
			WHERE d.domain = $1 AND r.kind = 'employment' AND r.is_current_primary`, triageTestDomain).Scan(&employments); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT organization_id IS NOT NULL AND confirmed_at IS NOT NULL FROM site_read
			WHERE target_kind = 'domain_triage' AND seed_url = $1`,
			people.TriageSeedURL(triageTestDomain)).Scan(&boundToOrg)
	}); err != nil {
		t.Fatal(err)
	}
	// The site's legal notice named the entity, so the organization is born
	// with that name rather than a title-cased domain label.
	if name != "Acme Robotics GmbH" || nameSource != "dossier" {
		t.Errorf("organization = %q/%s, want the site-stated name", name, nameSource)
	}
	if employments != 1 {
		t.Errorf("%d employment edges, want the waiting sender wired to their company", employments)
	}
	if !boundToOrg {
		t.Error("the triage dossier was never bound to the organization it produced")
	}
}

func TestTriageWithNoModelPathStillClosesTheQuestion(t *testing.T) {
	e := integration.Setup(t)

	// A worker role with no classification lane must not leave the domain
	// pending forever — every later message would re-ask a question nobody can
	// answer, and the sender would never get a company.
	args := openTriageQuestion(t, e, triageTestDomain, "info@"+triageTestDomain, "Acme Sales")
	worker := newTriageTestWorker(e, acmeDeepSite(), acmeDeepBrain(), nil)
	if err := worker.run(e.As(e.Rep1, nil, integration.AdminPerms), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	status, _, orgs := triageState(t, e, triageTestDomain)
	// Nobody's name explains "acme-triage", so the pre-triage behaviour stands:
	// the company is created and the ledger says nothing evidenced it.
	if status != people.DomainNoSite {
		t.Errorf("disposition = %q, want %q", status, people.DomainNoSite)
	}
	if orgs != 1 {
		t.Errorf("%d organizations, want the company created on the pre-triage terms", orgs)
	}
}

func TestTriageWithoutAModelRefusesADomainThatIsTheSendersName(t *testing.T) {
	e := integration.Setup(t)

	// Same no-model path, but the domain IS the sender's surname. The name test
	// is the last line before a company is created by default, and here it
	// holds.
	const domain = "wortmann.example"
	args := openTriageQuestion(t, e, domain, "manuel@"+domain, "Manuel Wortmann")
	worker := newTriageTestWorker(e, acmeDeepSite(), acmeDeepBrain(), nil)
	if err := worker.run(e.As(e.Rep1, nil, integration.AdminPerms), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	status, _, orgs := triageState(t, e, domain)
	if status != people.DomainPersonal {
		t.Errorf("disposition = %q, want %q", status, people.DomainPersonal)
	}
	if orgs != 0 {
		t.Errorf("%d organizations named after a person, want 0", orgs)
	}
}

// triageTestDomain is the domain the fake site's seed url belongs to, so a
// triage read of it reaches the fixture pages.
const triageTestDomain = "acme.example"
