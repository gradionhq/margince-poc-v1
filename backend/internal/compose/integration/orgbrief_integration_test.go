// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The brief's cache, against a real database.
//
// Three things only a real read can prove:
//
//   - the cache HITS on an unchanged account, and misses the moment the
//     account changes — the whole reason the key is the input rather than the
//     organization's row version;
//   - two readers of the same account get their own brief, because a brief
//     written from one reader's row scope is not true for another's;
//   - an agent is refused, and an account the caller cannot read has no
//     brief rather than a refusal that confirms it exists.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// briefClock is the brief's pinned instant, so generated_at is a fact the
// test can assert rather than a moving target. It is absolute rather than
// derived from time.Now(): a clock offset from the real one makes the test
// read the wall clock, and a far-future constant becomes a date that starts
// failing on its own.
//
// Nothing here asserts a window measured against it — the §4 strength window
// and the 60-day stall window are both duration comparisons, and the
// harness's own fixtures are written with the database's now(). A test that
// DOES assert either one sets its fixture timestamps explicitly, the way the
// visit-baseline suite does, rather than moving this clock to suit itself.
var briefClock = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// countingLane records how many times the model was asked. The cache's whole
// job is to keep this number down, so counting it IS the assertion.
type countingLane struct {
	calls int
	reply string
}

func (l *countingLane) Complete(context.Context, model.Request) (model.Response, error) {
	l.calls++
	return model.Response{Text: l.reply}, nil
}

func briefService(e *Env, lane orgbrief.Completer, routingVersion string) *orgbrief.Service {
	store := people.NewStore(e.Pool)
	view := org360.NewService(e.Pool, store, approvals.NewService(e.Pool),
		func() time.Time { return briefClock })
	// The same store serves both halves of the brief: the 360 for how the
	// account stands with us, its profile fields for what the company is.
	return orgbrief.NewService(e.Pool, view, store, lane, routingVersion,
		func() time.Time { return briefClock })
}

// nativeMode is the workspace reading from THIS system of record, which is
// what every case here is about. The overlay refusal has its own case below.
func nativeMode(context.Context) (bool, error) { return false, nil }

var briefReaderPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Read: true},
		"person":       {Create: true, Read: true},
		"deal":         {Create: true, Read: true, Update: true},
		"activity":     {Read: true},
		"pipeline":     {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

func TestOrganizationBriefCachesUntilTheAccountChanges(t *testing.T) {
	e := Setup(t)
	pipeline, stage, won := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	deal := e.SeedDeal(t, "Fleet retrofit", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org.UUID)

	reader := e.As(e.Rep1, nil, briefReaderPerms)
	lane := &countingLane{reply: `{"sentences":[{"text":"One open deal.","evidence":[{"entity_type":"organization","entity_id":"` + org.String() + `"}]}]}`}
	svc := briefService(e, lane, "routing-1")

	first, err := svc.Get(reader, org, false)
	if err != nil {
		t.Fatalf("first brief: %v", err)
	}
	if lane.calls != 1 {
		t.Fatalf("model calls = %d after the first brief, want 1", lane.calls)
	}
	if first.GeneratedBy != "model" {
		t.Errorf("generated_by = %q, want model", first.GeneratedBy)
	}
	if !first.GeneratedAt.Equal(briefClock) {
		t.Errorf("generated_at = %v, want the pinned instant", first.GeneratedAt)
	}

	// Same account, unchanged: the cache answers and the model is not asked.
	if _, err := svc.Get(reader, org, false); err != nil {
		t.Fatalf("second brief: %v", err)
	}
	if lane.calls != 1 {
		t.Errorf("model calls = %d on an unchanged account, want the cache to answer", lane.calls)
	}

	// The deal closes, which touches no organization row. That is exactly
	// why the key is the assembled input: a cache keyed on the org's row
	// version would keep serving a brief about a pipeline this account no
	// longer has. (A stage move WITHIN open is the same argument on a
	// smaller change; the unit test in orgbrief covers that one, since it
	// needs no second open stage to exist.)
	if _, err := e.Deals.AdvanceDeal(e.Admin(), ids.From[ids.DealKind](deal),
		deals.AdvanceDealInput{ToStageID: won}); err != nil {
		t.Fatalf("advancing the deal: %v", err)
	}
	if _, err := svc.Get(reader, org, false); err != nil {
		t.Fatalf("third brief: %v", err)
	}
	if lane.calls != 2 {
		t.Errorf("model calls = %d after the account changed, want a fresh brief", lane.calls)
	}
}

// The explicit refresh ignores a cache that still matches — the reader asked.
func TestOrganizationBriefRefreshIgnoresAMatchingCache(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	reader := e.As(e.Rep1, nil, briefReaderPerms)
	lane := &countingLane{reply: `{"sentences":[{"text":"Nothing open.","evidence":[{"entity_type":"organization","entity_id":"` + org.String() + `"}]}]}`}
	svc := briefService(e, lane, "routing-1")

	if _, err := svc.Get(reader, org, false); err != nil {
		t.Fatalf("first brief: %v", err)
	}
	if _, err := svc.Get(reader, org, true); err != nil {
		t.Fatalf("forced brief: %v", err)
	}
	if lane.calls != 2 {
		t.Errorf("model calls = %d, want the forced refresh to bypass the cache", lane.calls)
	}
}

// Two readers of one account each get their own brief. A brief written from
// one reader's row scope is not true for another's, so sharing the row would
// either leak or understate.
func TestOrganizationBriefIsCachedPerReader(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	lane := &countingLane{reply: `{"sentences":[{"text":"An account.","evidence":[{"entity_type":"organization","entity_id":"` + org.String() + `"}]}]}`}
	svc := briefService(e, lane, "routing-1")

	if _, err := svc.Get(e.As(e.Rep1, nil, briefReaderPerms), org, false); err != nil {
		t.Fatalf("first reader: %v", err)
	}
	if _, err := svc.Get(e.As(e.Rep2, nil, briefReaderPerms), org, false); err != nil {
		t.Fatalf("second reader: %v", err)
	}
	if lane.calls != 2 {
		t.Errorf("model calls = %d for two readers, want one brief each", lane.calls)
	}
	if rows := e.WsCount(t, `SELECT count(*) FROM org_brief WHERE organization_id = $1`, org.UUID); rows != 2 {
		t.Errorf("org_brief rows = %d, want one per reader", rows)
	}
}

// The per-viewer claim itself: a reader who cannot see a deal must get a
// brief that does not mention it.
//
// The sibling test above proves the cache is keyed per reader. That is a
// different claim — two readers could each get their own row and both rows
// still describe everything. This one checks the content, through the
// deterministic floor so the assertion is about the assembled INPUT rather
// than about what a model chose to write.
func TestOrganizationBriefDescribesOnlyWhatItsReaderCanSee(t *testing.T) {
	e := Setup(t)
	pipeline, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", nil))
	// Owned by Rep3, who sits in the other team: Rep1's team scope cannot
	// reach this deal, and an unbounded reader can.
	hidden := e.SeedDeal(t, "Other team retrofit", pipeline, stage, &e.Rep3)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, hidden, org.UUID)

	svc := briefService(e, nil, "")

	// The assertion is on the CITATIONS, not on the writer's wording. A
	// brief cites the records it was written from, so "was this reader told
	// about that deal" is answerable without coupling the test to a sentence
	// anyone might reasonably rephrase.
	unbounded, err := svc.Get(e.As(e.Rep1, nil, briefReaderPerms), org, false)
	if err != nil {
		t.Fatalf("brief for an unbounded reader: %v", err)
	}
	if !cites(briefSentences(unbounded), hidden) {
		t.Errorf("the unbounded reader's brief never cites the deal they can see: %q",
			briefText(briefSentences(unbounded)))
	}

	scoped := briefReaderPerms
	scoped.RowScope = principal.RowScopeTeam
	restricted, err := svc.Get(e.As(e.Rep1, []ids.UUID{e.Team1}, scoped), org, false)
	if err != nil {
		t.Fatalf("brief for a team-scoped reader: %v", err)
	}
	// A clickable reference to a record the reader would be refused is the
	// disclosure, whatever the sentence around it says.
	if cites(briefSentences(restricted), hidden) {
		t.Errorf("the brief cites deal %v, which this reader cannot open", hidden)
	}
	// The two readers' briefs must actually differ. Without this, both
	// assertions above would also hold for a writer that said nothing at all.
	if briefText(briefSentences(restricted)) == briefText(briefSentences(unbounded)) {
		t.Errorf("both readers got the same brief: %q", briefText(briefSentences(restricted)))
	}
}

// cites reports whether any sentence names one record as its evidence.
// briefSentences flattens a sectioned brief. Every assertion below is about
// what the WHOLE brief says or cites, and which heading a claim landed under
// is a different question from whether the reader was allowed to see it.
func briefSentences(brief crmcontracts.OrganizationBrief) []crmcontracts.OrganizationBriefSentence {
	var out []crmcontracts.OrganizationBriefSentence
	for _, section := range brief.Sections {
		out = append(out, section.Sentences...)
	}
	return out
}

func cites(sentences []crmcontracts.OrganizationBriefSentence, id ids.UUID) bool {
	for _, sentence := range sentences {
		for _, cited := range sentence.Evidence {
			if ids.UUID(cited.EntityId) == id {
				return true
			}
		}
	}
	return false
}

// briefText joins a brief's sentences, for the "these two differ" check and
// for failure messages — never as the assertion itself.
func briefText(sentences []crmcontracts.OrganizationBriefSentence) string {
	var out strings.Builder
	for _, sentence := range sentences {
		out.WriteString(sentence.Text)
		out.WriteString(" ")
	}
	return out.String()
}

// Re-pointing the model lane rewrites cached briefs rather than leaving text
// attributed to a model that no longer writes it.
func TestOrganizationBriefRewritesWhenTheLaneIsRepointed(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	reader := e.As(e.Rep1, nil, briefReaderPerms)
	reply := `{"sentences":[{"text":"An account.","evidence":[{"entity_type":"organization","entity_id":"` + org.String() + `"}]}]}`

	before := &countingLane{reply: reply}
	if _, err := briefService(e, before, "routing-1").Get(reader, org, false); err != nil {
		t.Fatalf("brief on the first routing: %v", err)
	}
	after := &countingLane{reply: reply}
	if _, err := briefService(e, after, "routing-2").Get(reader, org, false); err != nil {
		t.Fatalf("brief on the second routing: %v", err)
	}
	if after.calls != 1 {
		t.Errorf("model calls = %d after re-pointing the lane, want the brief rewritten", after.calls)
	}
}

// No lane at all is a deployment running no model, not a failure: the reader
// gets the deterministic floor, and generated_by says so.
func TestOrganizationBriefServesTheFloorWithoutALane(t *testing.T) {
	e := Setup(t)
	pipeline, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	deal := e.SeedDeal(t, "Fleet retrofit", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org.UUID)

	brief, err := briefService(e, nil, "").Get(e.As(e.Rep1, nil, briefReaderPerms), org, false)
	if err != nil {
		t.Fatalf("brief without a lane: %v", err)
	}
	if brief.GeneratedBy != "deterministic" {
		t.Errorf("generated_by = %q, want deterministic", brief.GeneratedBy)
	}
	if len(briefSentences(brief)) == 0 {
		t.Fatal("no sentences: the floor must always produce a brief")
	}
	// The floor cites too, so the card links the same records either way.
	// Asserted on the citation rather than the wording: the floor reports a
	// pipeline COUNT and leaves naming the deal to the card, which has the
	// id it needs from here.
	if len(briefSentences(brief)[0].Evidence) == 0 {
		t.Error("a deterministic sentence carries no evidence")
	}
	if !cites(briefSentences(brief), deal) {
		t.Errorf("the floor never cites the open deal: %q", briefText(briefSentences(brief)))
	}
}

// An account the caller cannot read has no brief, and the refusal is the same
// existence-hiding answer the record read gives.
func TestOrganizationBriefHidesAnAccountOutOfRowScope(t *testing.T) {
	e := Setup(t)
	theirs := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Other Team Account", &e.Rep3))
	scoped := briefReaderPerms
	scoped.RowScope = principal.RowScopeTeam

	_, err := briefService(e, nil, "").Get(e.As(e.Rep1, []ids.UUID{e.Team1}, scoped), theirs, false)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("brief for an out-of-scope account → %v, want ErrNotFound", err)
	}
	if rows := e.WsCount(t, `SELECT count(*) FROM org_brief`); rows != 0 {
		t.Errorf("org_brief rows = %d after a refused read, want 0", rows)
	}
}

// A brief is a reading aid for a person. An agent reading records through a
// passport has the records themselves.
func TestOrganizationBriefRefusesAnAgent(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	agent := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", SeatType: principal.SeatFull,
		// The shape a passport really mints: the granting human's id rides
		// along, so refusing on "no user" would prove nothing.
		UserID: e.Rep1, OnBehalfOf: e.Rep1, Permissions: briefReaderPerms,
	})

	if _, err := briefService(e, nil, "").Get(agent, org, false); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent brief → %v, want ErrPermissionDenied", err)
	}
}

// The transport is thin, but thin is a claim: the GET serves the cache and
// the POST forces a rewrite, and a refusal from the service has to reach the
// wire as a refusal rather than an empty 200.
func TestOrganizationBriefTransportServesAndForces(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	reader := e.As(e.Rep1, nil, briefReaderPerms)
	lane := &countingLane{reply: `{"sentences":[{"text":"An account.","evidence":[{"entity_type":"organization","entity_id":"` + org.String() + `"}]}]}`}
	handlers := orgbrief.NewHandlers(briefService(e, lane, "routing-1"), nativeMode)
	path := "/v1/organizations/" + org.String() + "/brief"

	rec := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, path, nil)
	handlers.GetOrganizationBrief(rec, get.WithContext(reader), crmcontracts.Id(org.UUID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var body crmcontracts.OrganizationBrief
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the brief: %v", err)
	}
	if len(briefSentences(body)) == 0 {
		t.Error("the served brief carries no sentences")
	}

	// A second GET is the cache; the POST is the reader asking anyway.
	rec = httptest.NewRecorder()
	handlers.GetOrganizationBrief(rec, get.WithContext(reader), crmcontracts.Id(org.UUID))
	// The status matters as much as the call count: a cache read that failed
	// would also leave the model unasked, and "0 extra calls" would read as a
	// hit.
	if rec.Code != http.StatusOK {
		t.Fatalf("cached GET status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var cachedBody crmcontracts.OrganizationBrief
	if err := json.Unmarshal(rec.Body.Bytes(), &cachedBody); err != nil {
		t.Fatalf("decoding the cached brief: %v", err)
	}
	if len(briefSentences(cachedBody)) != len(briefSentences(body)) {
		t.Errorf("the cached brief has %d sentences, the first had %d",
			len(briefSentences(cachedBody)), len(briefSentences(body)))
	}
	if lane.calls != 1 {
		t.Errorf("model calls = %d after a second GET, want the cache to answer", lane.calls)
	}
	rec = httptest.NewRecorder()
	post := httptest.NewRequest(http.MethodPost, path, nil)
	handlers.RegenerateOrganizationBrief(rec, post.WithContext(reader), crmcontracts.Id(org.UUID))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if lane.calls != 2 {
		t.Errorf("model calls = %d after the forced refresh, want a rewrite", lane.calls)
	}
}

// A refusal must reach the wire AS a refusal: an out-of-scope account is a
// 404, not a 200 carrying an empty brief.
func TestOrganizationBriefTransportRefusesOutOfScope(t *testing.T) {
	e := Setup(t)
	theirs := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Other Team Account", &e.Rep3))
	scoped := briefReaderPerms
	scoped.RowScope = principal.RowScopeTeam
	handlers := orgbrief.NewHandlers(briefService(e, nil, ""), nativeMode)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+theirs.String()+"/brief", nil)
	handlers.GetOrganizationBrief(rec,
		req.WithContext(e.As(e.Rep1, []ids.UUID{e.Team1}, scoped)), crmcontracts.Id(theirs.UUID))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d for an out-of-scope account, want 404", rec.Code)
	}
}

// An overlay workspace has no brief to write: the 360 the brief is assembled
// from refuses that mode, and its refusal lives in ITS handler rather than in
// the service this one calls — so without the same gate here, an overlay
// workspace would be handed a brief written from native rows while its own
// company page refuses to render.
func TestOrganizationBriefTransportRefusesAnOverlayWorkspace(t *testing.T) {
	e := Setup(t)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	lane := &countingLane{reply: `{"sentences":[]}`}
	handlers := orgbrief.NewHandlers(briefService(e, lane, "routing-1"),
		func(context.Context) (bool, error) { return true, nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+org.String()+"/brief", nil)
	handlers.GetOrganizationBrief(rec,
		req.WithContext(e.As(e.Rep1, nil, briefReaderPerms)), crmcontracts.Id(org.UUID))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d in overlay mode, want 422; body %s", rec.Code, rec.Body.String())
	}
	if lane.calls != 0 {
		t.Errorf("model calls = %d in overlay mode, want the refusal to come first", lane.calls)
	}
	if rows := e.WsCount(t, `SELECT count(*) FROM org_brief`); rows != 0 {
		t.Errorf("org_brief rows = %d after an overlay refusal, want 0", rows)
	}
}
