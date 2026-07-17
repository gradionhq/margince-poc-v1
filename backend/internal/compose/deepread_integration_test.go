// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deep read end-to-end: the worker crawls the site, extracts every
// page through the shared evidence gate — company fields plus the
// per-page-kind category call — and stages ONE "deepread" proposal a
// human can accept. Acceptance lands both halves in one transaction:
// profile fields fill-empty, category facts into organization_fact under
// the human-precedence guard. The dossier records the honest outcome —
// done with findings, done with zero findings and NO proposal, partial
// when the model lane dies midway, failed when the crawl itself does.
// Retries ride BeginSiteRead's CAS: a second attempt after any terminal
// outcome no-ops.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// acmeDeepSite is a two-page site: the landing page and an Impressum the
// well-known probe finds. Every other probe 404s like a real site.
func acmeDeepSite() *fakeSite {
	return &fakeSite{pages: map[string]fakeSitePage{
		seedURL:                {text: readable("Acme home.") + " Onboard your team in minutes, not weeks. Built for RevOps leaders at scaling SaaS companies."},
		seedURL + "/impressum": {text: readable("Impressum.") + " Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart. Telefon: +49 711 555 0100."},
	}}
}

// The scripted model replies, two per crawled page in crawl order — the
// shared company-field pass, then the page kind's category pass: home
// (fields, then signal), then the Impressum probe (fields, then company).
// The home fields reply grounds a positioning fact and GUESSES the legal
// name off marketing copy; the Impressum states it. The signal reply
// grounds a market signal off the home page's own words, the company
// reply the Impressum's phone number.
const (
	deepHomeReply = `{"fields":[
		{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9},
		{"field":"legal_name","value":"Acme (guessed)","evidence_snippet":"Built for RevOps leaders","confidence":0.95}]}`
	deepHomeSignalReply = `{"fields":[
		{"field":"named_customer","value":"Scaling SaaS companies — who the site says it serves","evidence_snippet":"scaling SaaS companies","confidence":0.6}]}`
	deepImpressumReply = `{"fields":[
		{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","confidence":0.7}]}`
	deepImpressumCompanyReply = `{"fields":[
		{"field":"phone","value":"+49 711 555 0100","evidence_snippet":"Telefon: +49 711 555 0100","confidence":0.85}]}`
	// noCategoryFacts is a category pass with nothing to quote.
	noCategoryFacts = `{"fields":[]}`
)

// newDeepReadTestWorker builds the worker over the fake site with the
// real approvals service, the deepread and site_lead accept effects wired
// exactly as compose wires them in production.
func newDeepReadTestWorker(e *integration.Env, site *fakeSite, brain completer) (*siteDeepReadWorker, *approvals.Service) {
	svc := approvals.NewService(e.Pool)
	svc.WithEffect(deepReadProposalKind, deepReadAcceptEffect(svc, e.People))
	svc.WithEffect(siteLeadProposalKind, siteLeadAcceptEffect(svc, newCaptureSink(e.Pool)))
	return &siteDeepReadWorker{
		people:    e.People,
		crawler:   testSiteCrawler(site),
		extract:   evidenceExtractor{brain: brain},
		approvals: svc,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, svc
}

// startDeepRead creates the queued dossier as Rep1 and shapes the job
// args exactly as the start handler enqueues them.
func startDeepRead(t *testing.T, e *integration.Env, org ids.UUID) (people.SiteRead, SiteDeepReadArgs) {
	t.Helper()
	read, joined, err := e.People.StartSiteRead(
		e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), seedURL, "human:"+e.Rep1.String())
	if err != nil {
		t.Fatalf("StartSiteRead: %v", err)
	}
	if joined {
		t.Fatal("the first start joined — the fixture is not clean")
	}
	return read, SiteDeepReadArgs{
		WorkspaceID:    e.WS,
		OrganizationID: org,
		SiteReadID:     read.ID,
		SeedURL:        read.SeedURL,
		RequestedBy:    read.RequestedBy,
	}
}

// orgIDOf types a harness-seeded untyped org id for the people store.
func orgIDOf(u ids.UUID) ids.OrganizationID { return ids.From[ids.OrganizationKind](u) }

// deepReadApprovals counts staged "deepread" rows (workspace-scoped).
func deepReadApprovals(t *testing.T, e *integration.Env) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'deepread'`)
}

func TestDeepReadCrawlsExtractsStagesOneDeepReadProposalAndFinishesDone(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, svc := newDeepReadTestWorker(e, acmeDeepSite(),
		ai.NewFakeClient().Script(deepHomeReply, deepHomeSignalReply, deepImpressumReply, deepImpressumCompanyReply))
	read, args := startDeepRead(t, e, org)

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	done, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.FinishedAt == nil || done.StoppedReason != nil {
		t.Fatalf("dossier = %+v, want done with no stop reason (discovery exhausted)", done)
	}
	// 2 fields (value_proposition + legal_name, the Impressum's statement
	// beating the home page's higher-confidence guess) + 2 category facts
	// (the home page's signal, the Impressum's phone).
	if done.FactCount != 4 {
		t.Fatalf("fact_count = %d, want 4 (2 merged fields + 2 category facts)", done.FactCount)
	}
	if len(done.Pages) != 2 || done.Pages[0].Kind != "home" || done.Pages[1].Kind != "impressum" {
		t.Fatalf("pages = %+v, want [home, impressum] in crawl order", done.Pages)
	}
	if len(done.ProposalIDs) != 1 {
		t.Fatalf("proposal_ids = %v, want exactly the one staged bundle", done.ProposalIDs)
	}

	// The staged row is ONE "deepread" proposal carrying the human's
	// authority: bound to the org, on behalf of the requester.
	var kind, status string
	var onBehalf ids.UUID
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT kind, status, on_behalf_of FROM approval WHERE id = $1`,
			done.ProposalIDs[0]).Scan(&kind, &status, &onBehalf)
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "deepread" || status != "pending" || onBehalf != e.Rep1 {
		t.Fatalf("approval = %s/%s on behalf of %s, want deepread/pending on behalf of the requesting human", kind, status, onBehalf)
	}

	// A River retry after the terminal outcome no-ops on the Begin CAS:
	// no second crawl, no second proposal.
	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("retry after done: %v", err)
	}
	if n := deepReadApprovals(t, e); n != 1 {
		t.Fatalf("retry staged again: %d deepread approvals, want 1", n)
	}

	// Acceptance lands BOTH halves in one transaction: the profile fields
	// as agent:deepread evidence rows, the category facts in
	// organization_fact linked back to the dossier, and ONE
	// organization.updated event on the outbox carrying the whole delta.
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](done.ProposalIDs[0]), true, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}
	var profileRows, factRows, updatedEvents int
	var capturedBy, legalName, factCapturedBy string
	var phoneValue, signalValue string
	var phoneSiteRead ids.UUID
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT count(*), max(captured_by) FROM organization_profile_field WHERE organization_id = $1`,
			org).Scan(&profileRows, &capturedBy); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(value), '') FROM organization_profile_field
			 WHERE organization_id = $1 AND field = 'legal_name'`, org).Scan(&legalName); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT count(*), max(captured_by) FROM organization_fact WHERE organization_id = $1`,
			org).Scan(&factRows, &factCapturedBy); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT value, site_read_id FROM organization_fact
			 WHERE organization_id = $1 AND category = 'company' AND field = 'phone'`,
			org).Scan(&phoneValue, &phoneSiteRead); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(value), '') FROM organization_fact
			 WHERE organization_id = $1 AND category = 'signal' AND field = 'named_customer'`,
			org).Scan(&signalValue); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM event_outbox
			 WHERE envelope->>'type' = 'organization.updated'`).Scan(&updatedEvents)
	})
	if err != nil {
		t.Fatal(err)
	}
	if profileRows != 2 || capturedBy != "agent:deepread" {
		t.Fatalf("accept wrote %d evidence rows as %q, want 2 as agent:deepread", profileRows, capturedBy)
	}
	if legalName != "Acme Robotics GmbH" {
		t.Fatalf("legal_name = %q, want the Impressum's statement over the home page's guess", legalName)
	}
	if factRows != 2 || factCapturedBy != "agent:deepread" {
		t.Fatalf("accept wrote %d organization_fact rows as %q, want 2 as agent:deepread", factRows, factCapturedBy)
	}
	if phoneValue != "+49 711 555 0100" || phoneSiteRead != read.ID {
		t.Fatalf("company/phone = %q linked to read %s, want the Impressum's number linked to the dossier", phoneValue, phoneSiteRead)
	}
	if signalValue == "" {
		t.Fatal("the home page's named_customer signal never landed in organization_fact")
	}
	if updatedEvents != 1 {
		t.Fatalf("%d organization.updated outbox events after accept, want exactly 1 for the whole delta", updatedEvents)
	}
}

func TestDeepReadWithNothingEvidencedIsAnHonestEmptyDoneWithNoProposal(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	// Every reply hallucinates: no snippet is verbatim on either page, so
	// nothing survives the no-guess gate — on the field passes or the
	// category passes.
	hallucinated := `{"fields":[{"field":"icp","value":"guessed","evidence_snippet":"nowhere on any page","confidence":0.9}]}`
	hallucinatedFact := `{"fields":[{"field":"named_customer","value":"guessed","evidence_snippet":"nowhere on any page","confidence":0.9}]}`
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(),
		ai.NewFakeClient().Script(hallucinated, hallucinatedFact, hallucinated, hallucinatedFact))
	read, args := startDeepRead(t, e, org)

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	done, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.FactCount != 0 {
		t.Fatalf("dossier = %+v, want done with fact_count 0 — an empty read is not an error", done)
	}
	if len(done.ProposalIDs) != 0 {
		t.Fatalf("proposal_ids = %v, want none — nothing evidenced stages nothing", done.ProposalIDs)
	}
	if n := deepReadApprovals(t, e); n != 0 {
		t.Fatalf("%d deepread approvals staged from an empty read, want 0", n)
	}
}

func TestDeepReadCrawlFailureFinishesFailedAndARetryNoOps(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	// The seed page itself is unreachable: a failed crawl, not a partial one.
	worker, _ := newDeepReadTestWorker(e, &fakeSite{pages: map[string]fakeSitePage{}}, ai.NewFakeClient())
	read, args := startDeepRead(t, e, org)

	if err := worker.run(context.Background(), args); err == nil {
		t.Fatal("a failed crawl returned nil — River would record success")
	}
	failed, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.FinishedAt == nil {
		t.Fatalf("dossier = %+v, want failed with finished_at stamped", failed)
	}

	// The River retry after the recorded failure: Begin CAS-misses and the
	// attempt no-ops — one honest failure, no zombie re-crawl.
	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("retry after failed: %v", err)
	}
	after, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "failed" || !after.FinishedAt.Equal(*failed.FinishedAt) {
		t.Fatalf("retry touched the failed dossier: %+v", after)
	}
	if n := deepReadApprovals(t, e); n != 0 {
		t.Fatalf("%d deepread approvals after a failed crawl, want 0", n)
	}
}

func TestDeepReadOnABrainlessWorkerFailsTheReadActionably(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), nil)
	read, args := startDeepRead(t, e, org)

	err := worker.run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "--ai-routing") {
		t.Fatalf("run on a brainless worker → %v, want the actionable no-model-path error", err)
	}
	failed, gerr := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if failed.Status != "failed" {
		t.Fatalf("dossier = %+v, want failed — never queued forever behind a worker that cannot extract", failed)
	}
}

func TestDeepReadModelFailureMidwayKeepsWhatWasReadAsPartial(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	// The home page's two passes extract, then the model lane dies before
	// the Impressum.
	brain := &failsAfter{inner: ai.NewFakeClient().Script(deepHomeReply, deepHomeSignalReply), limit: 2}
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), brain)
	read, args := startDeepRead(t, e, org)

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	partial, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "partial" {
		t.Fatalf("dossier status = %q, want partial — evidence in hand is kept, not discarded", partial.Status)
	}
	if len(partial.Pages) != 1 || partial.Pages[0].Kind != "home" {
		t.Fatalf("pages = %+v, want only the page whose findings made the proposal", partial.Pages)
	}
	// Both home-page fields and its signal fact ground verbatim, so all
	// three survive.
	if partial.FactCount != 3 || len(partial.ProposalIDs) != 1 {
		t.Fatalf("fact_count = %d proposals = %v, want the home page's 2 fields + 1 fact staged", partial.FactCount, partial.ProposalIDs)
	}
}

// acmeServicesSite is a two-page site whose services page lists what the
// company sells — the offering category's fixture.
func acmeServicesSite() *fakeSite {
	return &fakeSite{pages: map[string]fakeSitePage{
		seedURL:               {text: readable("Acme home.")},
		seedURL + "/services": {text: readable("Services.") + " We deliver CRM Rollout projects end to end. Margince is our CRM product."},
	}}
}

// deepOfferingReply lists the same service twice under different
// descriptions (one normalized value_key) plus a distinct product — the
// dedupe fixture.
const deepOfferingReply = `{"fields":[
	{"field":"service","value":"CRM Rollout — implementation projects","evidence_snippet":"CRM Rollout projects","confidence":0.6},
	{"field":"service","value":"CRM Rollout — end-to-end delivery","evidence_snippet":"CRM Rollout projects end to end","confidence":0.9},
	{"field":"product","value":"Margince — our CRM product","evidence_snippet":"Margince is our CRM product","confidence":0.8}]}`

// runServicesDeepRead crawls acmeServicesSite with nothing on the field
// passes and deepOfferingReply on the services page's offering pass, and
// returns the finished dossier. Call order: home fields, home signal,
// services fields, services offering.
func runServicesDeepRead(t *testing.T, e *integration.Env, org ids.UUID) (people.SiteRead, *approvals.Service) {
	t.Helper()
	worker, svc := newDeepReadTestWorker(e, acmeServicesSite(),
		ai.NewFakeClient().Script(noCategoryFacts, noCategoryFacts, noCategoryFacts, deepOfferingReply))
	read, args := startDeepRead(t, e, org)
	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}
	done, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.FactCount != 2 || len(done.ProposalIDs) != 1 {
		t.Fatalf("dossier = %+v, want the 2 deduped offerings staged as one proposal", done)
	}
	return done, svc
}

func TestDeepReadOfferingsDedupeOnValueKeyAndAcceptRespectsHumanPrecedence(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runServicesDeepRead(t, e, org)

	// The staged payload carries ONE service row — the higher-confidence
	// spelling of the shared value_key — plus the product.
	var proposedChange []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT proposed_change FROM approval WHERE id = $1`, done.ProposalIDs[0]).Scan(&proposedChange)
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := people.UnmarshalDeepRead(proposedChange)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Facts) != 2 {
		t.Fatalf("staged facts = %+v, want the deduped service + the product", proposal.Facts)
	}
	service := proposal.Facts[0]
	if service.Field != "service" || service.ValueKey != "crm rollout" || service.Value != "CRM Rollout — end-to-end delivery" {
		t.Fatalf("staged service = %+v, want the higher-confidence spelling under value_key 'crm rollout'", service)
	}

	// A human has since claimed the service fact; the accept must land the
	// product beside it and leave the human's row untouched.
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_fact
			  (workspace_id, organization_id, category, field, value, value_key,
			   evidence_snippet, source_url, confidence, source, captured_by)
			VALUES ($1, $2, 'offering', 'service', 'CRM Rollout (human curated)', 'crm rollout',
			        'set by hand', '', 1, 'manual', $3)`,
			e.WS, org, "human:"+e.Rep1.String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](done.ProposalIDs[0]), true, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}

	var factRows int
	var serviceValue, serviceCapturedBy, productValue string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM organization_fact WHERE organization_id = $1`, org).Scan(&factRows); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT value, captured_by FROM organization_fact
			 WHERE organization_id = $1 AND field = 'service' AND value_key = 'crm rollout'`,
			org).Scan(&serviceValue, &serviceCapturedBy); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT coalesce(max(value), '') FROM organization_fact
			 WHERE organization_id = $1 AND field = 'product'`, org).Scan(&productValue)
	})
	if err != nil {
		t.Fatal(err)
	}
	if factRows != 2 {
		t.Fatalf("%d organization_fact rows after accept, want 2 (the human's service + the landed product)", factRows)
	}
	if serviceValue != "CRM Rollout (human curated)" || serviceCapturedBy != "human:"+e.Rep1.String() {
		t.Fatalf("service row = %q by %q — the accept overwrote a human-claimed fact", serviceValue, serviceCapturedBy)
	}
	if productValue != "Margince — our CRM product" {
		t.Fatalf("product row = %q, want the staged product landed beside the human's row", productValue)
	}
}

func TestDeepReadRejectionLandsNothing(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	done, svc := runServicesDeepRead(t, e, org)

	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](done.ProposalIDs[0]), false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_fact`); n != 0 {
		t.Fatalf("%d organization_fact rows after a rejection, want 0", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_profile_field`); n != 0 {
		t.Fatalf("%d profile-field rows after a rejection, want 0", n)
	}
}

// fakeInserter stands in for the insert-only River client so handler
// tests can count what start enqueues.
type fakeInserter struct {
	inserts []river.JobArgs
	err     error
}

func (f *fakeInserter) Enqueue(_ context.Context, args river.JobArgs, _ *river.InsertOpts) error {
	if f.err != nil {
		return f.err
	}
	f.inserts = append(f.inserts, args)
	return nil
}

func newDeepReadTestEngine(e *integration.Env, inserter *fakeInserter) *deepReadEngine {
	return &deepReadEngine{
		people:  e.People,
		enqueue: inserter,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// postDeepRead drives the start handler as the given caller and decodes
// the 202 handle (or fails the test on any other status when want202).
func postDeepRead(t *testing.T, e *integration.Env, engine *deepReadEngine, caller ids.UUID, org ids.UUID) (*httptest.ResponseRecorder, crmcontracts.SiteReadStarted) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/deep-read", nil).
		WithContext(e.As(caller, nil, integration.AdminPerms))
	rec := httptest.NewRecorder()
	engine.start(rec, req, openapi_types.UUID(org))
	var started crmcontracts.SiteReadStarted
	if rec.Code == http.StatusAccepted {
		if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
			t.Fatalf("decoding SiteReadStarted: %v", err)
		}
	}
	return rec, started
}

func TestDeepReadStartQueuesOnceAndAReClickJoinsWithoutASecondInsert(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	inserter := &fakeInserter{}
	engine := newDeepReadTestEngine(e, inserter)

	rec, first := postDeepRead(t, e, engine, e.Rep1, org)
	if rec.Code != http.StatusAccepted || first.Status != crmcontracts.SiteReadStartedStatusQueued {
		t.Fatalf("first start → %d %+v, want 202 queued", rec.Code, first)
	}
	if len(inserter.inserts) != 1 {
		t.Fatalf("first start enqueued %d jobs, want 1", len(inserter.inserts))
	}
	args, ok := inserter.inserts[0].(SiteDeepReadArgs)
	if !ok {
		t.Fatalf("enqueued %T, want SiteDeepReadArgs", inserter.inserts[0])
	}
	if args.WorkspaceID != e.WS || args.OrganizationID != org ||
		args.SiteReadID != ids.UUID(first.ReadId) ||
		args.SeedURL != "https://acme.example" || args.RequestedBy != "human:"+e.Rep1.String() {
		t.Fatalf("job args = %+v, want the dossier's own identity and the org's domain as seed", args)
	}

	// A second click while the read is in flight joins it: same read id,
	// answered as running, and NO second job rides the queue.
	rec2, second := postDeepRead(t, e, engine, e.Rep2, org)
	if rec2.Code != http.StatusAccepted || second.Status != crmcontracts.SiteReadStartedStatusRunning {
		t.Fatalf("joining start → %d %+v, want 202 running", rec2.Code, second)
	}
	if second.ReadId != first.ReadId {
		t.Fatalf("joining start answered read %s, want the in-flight %s", second.ReadId, first.ReadId)
	}
	if len(inserter.inserts) != 1 {
		t.Fatalf("joining start enqueued a rival job (%d inserts, want 1)", len(inserter.inserts))
	}
}

func TestDeepReadStartWithoutADomainOrOverrideIs422(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "", "")
	inserter := &fakeInserter{}
	engine := newDeepReadTestEngine(e, inserter)

	rec, _ := postDeepRead(t, e, engine, e.Rep1, org)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("start with no URL to read → %d, want 422", rec.Code)
	}
	if len(inserter.inserts) != 0 {
		t.Fatalf("a refused start enqueued %d jobs, want 0", len(inserter.inserts))
	}
	if n := e.WsCount(t, `SELECT count(*) FROM site_read`); n != 0 {
		t.Fatalf("a refused start left %d dossiers, want 0", n)
	}
}

func TestDeepReadStartClosesTheDossierWhenTheEnqueueFails(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	inserter := &fakeInserter{err: context.DeadlineExceeded}
	engine := newDeepReadTestEngine(e, inserter)

	rec, _ := postDeepRead(t, e, engine, e.Rep1, org)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("start with a broken queue → %d, want 500", rec.Code)
	}
	// The dossier must not squat the org's in-flight slot: it is closed as
	// failed, so the next start mints a fresh read instead of joining a
	// zombie.
	if n := e.WsCount(t, `SELECT count(*) FROM site_read WHERE status = 'failed'`); n != 1 {
		t.Fatalf("%d failed dossiers after an enqueue failure, want 1", n)
	}
	inserter.err = nil
	rec2, retried := postDeepRead(t, e, engine, e.Rep1, org)
	if rec2.Code != http.StatusAccepted || retried.Status != crmcontracts.SiteReadStartedStatusQueued {
		t.Fatalf("retry after a closed enqueue failure → %d %+v, want a fresh 202 queued", rec2.Code, retried)
	}
}

// The terminal dossier write must survive the work context's death: a deep
// read whose crawl+extract exhausted the job deadline still has to CLOSE its
// dossier, or the read is left running forever and squats the org's one
// in-flight slot. terminalCtx (WithoutCancel + a fresh deadline) is what makes
// that hold; this pins it against a refactor that re-threads the dead ctx.
func TestDeepReadFinishSurvivesACancelledWorkContext(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), ai.NewFakeClient())
	read, args := startDeepRead(t, e, org)

	// The dossier is picked up (queued → running), then the work context dies
	// — exactly the shape the live incident hit mid-extraction.
	workCtx, cancel := context.WithCancel(deepReadWorkerCtx(context.Background(), args))
	if _, err := worker.people.BeginSiteRead(workCtx, read.ID); err != nil {
		t.Fatalf("begin: %v", err)
	}
	cancel()

	if err := worker.finish(workCtx, read.ID, "partial", nil, siteCrawl{}, 0, nil); err != nil {
		t.Fatalf("finish under a cancelled work context: %v", err)
	}

	got, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "partial" {
		t.Fatalf("dossier status = %q, want partial — the terminal write was starved by the dead work context", got.Status)
	}
}
