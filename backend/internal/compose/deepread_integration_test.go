// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deep read end-to-end: the worker crawls the site, extracts every
// page through the shared evidence gate, stages ONE ordinary "enrich"
// proposal a human can accept (the existing scrapeaccept executor fills
// only empty fields), and records the honest outcome on the dossier —
// done with facts, done with zero facts and NO proposal, partial when the
// model lane dies midway, failed when the crawl itself does. Retries ride
// BeginSiteRead's CAS: a second attempt after any terminal outcome no-ops.

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
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
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
		seedURL + "/impressum": {text: readable("Impressum.") + " Acme Robotics GmbH, Werkstr. 1, 70435 Stuttgart."},
	}}
}

// The scripted model replies, one per crawled page in crawl order (home,
// then the Impressum probe). The home reply grounds a positioning fact
// and GUESSES the legal name off marketing copy; the Impressum states it.
const (
	deepHomeReply = `{"fields":[
		{"field":"value_proposition","value":"Fast onboarding","evidence_snippet":"Onboard your team in minutes, not weeks","confidence":0.9},
		{"field":"legal_name","value":"Acme (guessed)","evidence_snippet":"Built for RevOps leaders","confidence":0.95}]}`
	deepImpressumReply = `{"fields":[
		{"field":"legal_name","value":"Acme Robotics GmbH","evidence_snippet":"Acme Robotics GmbH","confidence":0.7}]}`
)

// newDeepReadTestWorker builds the worker over the fake site with the
// real approvals service, the enrich accept effect wired exactly as
// compose wires it in production.
func newDeepReadTestWorker(e *integration.Env, site *fakeSite, brain runner.Brain) (*siteDeepReadWorker, *approvals.Service) {
	svc := approvals.NewService(e.Pool)
	svc.WithEffect("enrich", scrapeAcceptEffect(svc, e.People))
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

// enrichApprovals counts staged "enrich" rows (workspace-scoped).
func enrichApprovals(t *testing.T, e *integration.Env) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'enrich'`)
}

func TestDeepReadCrawlsExtractsStagesOneEnrichProposalAndFinishesDone(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, svc := newDeepReadTestWorker(e, acmeDeepSite(), ai.NewFakeClient().Script(deepHomeReply, deepImpressumReply))
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
	// value_proposition from the home page + legal_name — the Impressum's
	// statement beating the home page's higher-confidence guess.
	if done.FactCount != 2 {
		t.Fatalf("fact_count = %d, want 2 (merged across pages, one answer per field)", done.FactCount)
	}
	if len(done.Pages) != 2 || done.Pages[0].Kind != "home" || done.Pages[1].Kind != "impressum" {
		t.Fatalf("pages = %+v, want [home, impressum] in crawl order", done.Pages)
	}
	if len(done.ProposalIDs) != 1 {
		t.Fatalf("proposal_ids = %v, want exactly the one staged bundle", done.ProposalIDs)
	}

	// The staged row is an ORDINARY enrich proposal carrying the human's
	// authority: same kind, bound to the org, on behalf of the requester.
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
	if kind != "enrich" || status != "pending" || onBehalf != e.Rep1 {
		t.Fatalf("approval = %s/%s on behalf of %s, want enrich/pending on behalf of the requesting human", kind, status, onBehalf)
	}

	// A River retry after the terminal outcome no-ops on the Begin CAS:
	// no second crawl, no second proposal.
	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("retry after done: %v", err)
	}
	if n := enrichApprovals(t, e); n != 1 {
		t.Fatalf("retry staged again: %d enrich approvals, want 1", n)
	}

	// The proposal is decidable by the EXISTING accept executor: the merged
	// fields land as agent:scrape evidence rows, nothing new invented.
	if _, err := svc.Decide(e.As(e.Rep2, nil, integration.AdminPerms), ids.From[ids.ApprovalKind](done.ProposalIDs[0]), true, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}
	var profileRows int
	var capturedBy, legalName string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*), max(captured_by) FROM organization_profile_field WHERE organization_id = $1`,
			org).Scan(&profileRows, &capturedBy); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT coalesce(max(value), '') FROM organization_profile_field
			 WHERE organization_id = $1 AND field = 'legal_name'`, org).Scan(&legalName)
	})
	if err != nil {
		t.Fatal(err)
	}
	if profileRows != 2 || capturedBy != "agent:scrape" {
		t.Fatalf("accept wrote %d evidence rows as %q, want 2 as agent:scrape", profileRows, capturedBy)
	}
	if legalName != "Acme Robotics GmbH" {
		t.Fatalf("legal_name = %q, want the Impressum's statement over the home page's guess", legalName)
	}
}

func TestDeepReadWithNothingEvidencedIsAnHonestEmptyDoneWithNoProposal(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	// Both replies hallucinate: no snippet is verbatim on either page, so
	// nothing survives the no-guess gate.
	hallucinated := `{"fields":[{"field":"icp","value":"guessed","evidence_snippet":"nowhere on any page","confidence":0.9}]}`
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), ai.NewFakeClient().Script(hallucinated, hallucinated))
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
	if n := enrichApprovals(t, e); n != 0 {
		t.Fatalf("%d enrich approvals staged from an empty read, want 0", n)
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
	if n := enrichApprovals(t, e); n != 0 {
		t.Fatalf("%d enrich approvals after a failed crawl, want 0", n)
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
	// The home page extracts, then the model lane dies before the Impressum.
	brain := &failsAfter{inner: ai.NewFakeClient().Script(deepHomeReply), limit: 1}
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
	// Both home-page fields ground verbatim, so both survive.
	if partial.FactCount != 2 || len(partial.ProposalIDs) != 1 {
		t.Fatalf("fact_count = %d proposals = %v, want the home page's 2 fields staged", partial.FactCount, partial.ProposalIDs)
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
