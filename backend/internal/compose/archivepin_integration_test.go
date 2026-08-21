// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The archive's version guard, against real Postgres.
//
// The unit lane proves the tool hands the executor the version the approval was
// granted against. What it cannot prove is that the executor then REFUSES on
// it: the compare lives in Patch.ApplyGuarded, inside the transaction each
// archive store opens, and only a database answers that. A fake that records
// the input it was handed agrees with a store that reads the field and ignores
// it, which is exactly the shape this defect had.
//
// So both directions run here for every type archive_record stages: a stale pin
// must refuse AND leave the record live, and the current pin must archive. The
// second half is not ceremony — a guard that refuses everything passes the
// first half perfectly.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// A stale pin refuses the archive and changes nothing.
//
// This is the outcome #2015 is about, one layer below the approval: redemption
// verified the record was at version N and committed, something changed the
// record, and the archive then landed anyway because the write carried no
// version clause. With the pin threaded through the seam, that write refuses.
func TestAnArchiveAtAStaleVersionRefusesAndLeavesTheRecordLive(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	admin := e.Admin()

	for _, tc := range archivePinCases(admin, t, e, native) {
		t.Run(string(tc.ref.Type), func(t *testing.T) {
			stale := tc.version
			// The concurrent change: a real update through the real provider,
			// which is what moves the version on. A hand-bumped column would
			// prove the clause reads a number, not that the number moves for
			// the reason a racing writer moves it.
			bumpVersion(admin, t, e, native, tc.table, tc.ref)

			_, err := native.ArchiveAt(admin, datasource.ArchiveInput{Ref: tc.ref, IfVersion: &stale})
			if !errors.Is(err, apperrors.ErrVersionSkew) {
				t.Fatalf("archiving a %s at the stale version %d answered %v, want version skew — an "+
					"approval granted against that version must not be carried out against another",
					tc.ref.Type, stale, err)
			}
			if archivedAt(t, e, tc.table, tc.ref.ID) {
				t.Errorf("the %s was archived anyway: the refusal above is a message, not a guard",
					tc.ref.Type)
			}
		})
	}
}

// The current pin archives, so the guard above refuses skew rather than
// refusing everything.
func TestAnArchiveAtTheCurrentVersionRuns(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	admin := e.Admin()

	for _, tc := range archivePinCases(admin, t, e, native) {
		t.Run(string(tc.ref.Type), func(t *testing.T) {
			current := tc.version
			if _, err := native.ArchiveAt(admin, datasource.ArchiveInput{
				Ref: tc.ref, IfVersion: &current,
			}); err != nil {
				t.Fatalf("archiving a %s at its current version %d answered %v, want the archive to run",
					tc.ref.Type, current, err)
			}
			if !archivedAt(t, e, tc.table, tc.ref.ID) {
				t.Errorf("the %s reports no archived_at, so the pinned write did not land", tc.ref.Type)
			}
		})
	}
}

// An archive with NO pin still runs — the ordinary unapproved write, which
// takes the row lock instead and must not have become a skew failure.
func TestAnUnpinnedArchiveStillRuns(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	admin := e.Admin()

	for _, tc := range archivePinCases(admin, t, e, native) {
		t.Run(string(tc.ref.Type), func(t *testing.T) {
			if _, err := native.Archive(admin, tc.ref); err != nil {
				t.Fatalf("archiving a %s with no pin answered %v — the unapproved path must be "+
					"unchanged", tc.ref.Type, err)
			}
			if !archivedAt(t, e, tc.table, tc.ref.ID) {
				t.Errorf("the %s reports no archived_at after an unpinned archive", tc.ref.Type)
			}
		})
	}
}

// archivePinCase is one seeded record plus the version it currently sits at and
// the table its archived_at is read from.
type archivePinCase struct {
	ref     datasource.EntityRef
	table   string
	version int64
}

// archivePinCases seeds one record of every type the native provider archives
// through a pin — person, organization and deal.
//
// project and relationship are deliberately absent, and the reason is worth
// stating: both are reached by a create this harness does not have a one-line
// fixture for, and a case that seeds half a fixture proves less than no case.
// Their stores take the same storekit.ApplyGuarded call as these three; what
// would be new about covering them is the seeding, not the guard.
func archivePinCases(as context.Context, t *testing.T, e *integration.Env, p *Provider) []archivePinCase {
	t.Helper()
	person := seedForArchivePin(as, t, p, datasource.EntityPerson,
		`{"full_name":"Pin Probe","owner_id":"`+e.AdminUser.String()+`"}`)
	org := seedForArchivePin(as, t, p, datasource.EntityOrganization,
		`{"display_name":"Pin Probe GmbH","owner_id":"`+e.AdminUser.String()+`"}`)

	pipeline, open, _ := integration.DealFixture(t, e)
	deal := seedForArchivePin(as, t, p, datasource.EntityDeal,
		`{"name":"Pin probe","owner_id":"`+e.AdminUser.String()+
			`","pipeline_id":"`+pipeline.String()+`","stage_id":"`+open.String()+`"}`)

	return []archivePinCase{
		{ref: person, table: "person", version: versionOf(t, e, "person", person.ID)},
		{ref: org, table: "organization", version: versionOf(t, e, "organization", org.ID)},
		{ref: deal, table: "deal", version: versionOf(t, e, "deal", deal.ID)},
	}
}

func seedForArchivePin(as context.Context, t *testing.T, p *Provider,
	entity datasource.EntityType, fields string,
) datasource.EntityRef {
	t.Helper()
	ref, err := p.Create(as, datasource.CreateInput{
		EntityType: entity, Fields: json.RawMessage(fields), Source: "test",
	})
	if err != nil {
		t.Fatalf("seeding the %s: %v", entity, err)
	}
	return ref
}

// bumpVersion is the concurrent writer: one real patch through the real
// provider, which moves the row's version exactly as a racing human would.
//
// The version is read either side and the MOVE is asserted, which is not
// belt-and-braces: this test caught itself passing for person and organization
// because the patch named `job_title` and `website`, fields those update
// requests do not carry. Neither errored — both requests carry an
// AdditionalProperties map, so an unknown field is absorbed rather than
// refused, the patch set nothing, and the version stayed where it was. A stale
// pin that is not actually stale admits the archive, and the test agreed with
// the bug it was written to catch.
func bumpVersion(as context.Context, t *testing.T, e *integration.Env, p *Provider,
	table string, ref datasource.EntityRef,
) {
	t.Helper()
	before := versionOf(t, e, table, ref.ID)
	patch := map[datasource.EntityType]string{
		datasource.EntityPerson:       `{"title":"changed under the approval"}`,
		datasource.EntityOrganization: `{"description":"changed under the approval"}`,
		datasource.EntityDeal:         `{"name":"changed under the approval"}`,
	}[ref.Type]
	if _, err := p.Update(as, datasource.UpdateInput{
		Ref: ref, Patch: json.RawMessage(patch), Source: "test",
	}); err != nil {
		t.Fatalf("the concurrent change to the %s did not land, so this test is not about a stale "+
			"pin: %v", ref.Type, err)
	}
	if after := versionOf(t, e, table, ref.ID); after == before {
		t.Fatalf("the %s is still at version %d after the concurrent change — the patch set nothing, "+
			"so the pin below is not stale and this test would pass against an unguarded archive",
			ref.Type, before)
	}
}

func versionOf(t *testing.T, e *integration.Env, table string, id ids.UUID) int64 {
	t.Helper()
	var version int64
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version FROM `+table+` WHERE id = $1`, id).Scan(&version)
	}, "reading the row's version")
	return version
}

func archivedAt(t *testing.T, e *integration.Env, table string, id ids.UUID) bool {
	t.Helper()
	var stamped *string
	seedAsAdmin(t, e, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT archived_at::text FROM `+table+` WHERE id = $1`, id).Scan(&stamped)
	}, "reading the row's archived_at")
	return stamped != nil
}

// The REST door honours the pin too, and that is the half #2015 nearly shipped
// closed on one door only.
//
// The agent gate forwards a released approval's version as the request's own
// If-Match (compose/agentgatestaging.go). What left the archive unpinned was
// not the contract: these handlers read the header straight off the request
// with httperr.IfMatchVersion and ignore their Params struct entirely, so an
// undeclared parameter would still have been parsed. It was the handler
// passing a literal nil to the store.
//
// Declaring If-Match on the route is therefore about the CONTRACT telling the
// truth — a client cannot be expected to send a precondition the API does not
// advertise, and the 409 it can now provoke has to be a declared response.
// Only a request through the real handler proves the chain either way.
func TestTheRESTArchiveHonoursAStaleIfMatch(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	admin := e.Admin()

	person := seedForArchivePin(admin, t, native, datasource.EntityPerson,
		`{"full_name":"If-Match Probe","owner_id":"`+e.AdminUser.String()+`"}`)
	stale := versionOf(t, e, "person", person.ID)
	bumpVersion(admin, t, e, native, "person", person)

	rec := archiveOverREST(admin, t, e, person.ID, stale)

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /v1/people/{id} with a stale If-Match answered %d, want 409 — an archive "+
			"released against a version the record has left must not land", rec.Code)
	}
	if archivedAt(t, e, "person", person.ID) {
		t.Error("the person was archived anyway: the 409 is a message, not a guard")
	}
}

// And the current version still archives, so the route refuses skew rather
// than refusing every If-Match it is handed.
func TestTheRESTArchiveAcceptsACurrentIfMatch(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	admin := e.Admin()

	person := seedForArchivePin(admin, t, native, datasource.EntityPerson,
		`{"full_name":"If-Match Probe","owner_id":"`+e.AdminUser.String()+`"}`)

	rec := archiveOverREST(admin, t, e, person.ID, versionOf(t, e, "person", person.ID))

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /v1/people/{id} with the current If-Match answered %d, want 200", rec.Code)
	}
	if !archivedAt(t, e, "person", person.ID) {
		t.Error("the person reports no archived_at after an accepted archive")
	}
}

// archiveOverREST drives the real people handler the router binds, carrying
// If-Match as the agent gate would.
func archiveOverREST(as context.Context, t *testing.T, e *integration.Env,
	person ids.UUID, ifVersion int64,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/people/"+person.String(), nil)
	req.Header.Set("If-Match", strconv.FormatInt(ifVersion, 10))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", person.String())
	req = req.WithContext(context.WithValue(as, chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	people.NewHandlers(e.DB()).ArchivePerson(rec, req, crmcontracts.Id(person),
		crmcontracts.ArchivePersonParams{})
	return rec
}

// No geocode fixture lives here. #2173 records why, and carries what a future
// one has to prove: while organization.workspace_id is missing, a fixture
// cannot tell "the workspace guard refused" from "the query cannot run", so it
// would read GREEN exactly when the subject is most broken.
