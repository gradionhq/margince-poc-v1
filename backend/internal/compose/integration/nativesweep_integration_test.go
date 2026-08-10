// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The native seam's multi-type sweep — what `search_records` answers when a
// caller names no record type.
//
// Three claims, each of which a caller reads off the published schema and acts
// on: the page is at most the limit it asked for, a page that says there is
// more hands back somewhere to resume, and a seat that may read four of the
// five object classes is answered about those four rather than refused.
//
// The first is the one that costs. The schema declares `limit` with a maximum
// of 50, and charging each type the full limit answers up to five times that —
// on the surface where the records land in a paid context window.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// seedSweepable puts two records of each searchable type in the workspace,
// each carrying the same word, so a sweep has more of every type than any
// small page can hold.
func seedSweepable(t *testing.T, e *SearchEnv) {
	t.Helper()
	for _, name := range []string{"Sweepable One", "Sweepable Two"} {
		e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by)
		           VALUES ($1, $2, $3, 'manual', 'human:x')`, name)
		e.Seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by)
		           VALUES ($1, $2, $3, 'manual', 'human:x')`, name)
		e.Seed(t, `INSERT INTO lead (id, workspace_id, full_name, status, source, score, captured_by)
		           VALUES ($1, $2, $3, 'working', 'inbound', 10, 'human:x')`, name)
	}
}

// sweepingProvider is the composite provider over this fixture's pool — the
// one `search_records` reaches.
func sweepingProvider(e *SearchEnv) *compose.Provider {
	return compose.NewProvider(e.Pool)
}

func TestTheNativeSweepAnswersAtMostTheLimitItWasAskedFor(t *testing.T) {
	e := SetupSearch(t)
	seedSweepable(t, e)
	ctx := e.Admin()

	// Three types hold two matching records each. A page of three that charged
	// every type its own limit would answer nine.
	res, err := sweepingProvider(e).Search(ctx, datasource.SearchQuery{Text: "Sweepable", Limit: 3})
	if err != nil {
		t.Fatalf("sweeping with no record type: %v", err)
	}
	if len(res.Records) != 3 {
		t.Fatalf("a sweep with limit=3 answered %d records — the limit bounds the PAGE, not each type, "+
			"and a caller reads the declared maximum as what it is about to be handed", len(res.Records))
	}
	if !res.HasMore || res.NextCursor == "" {
		t.Fatalf("the capped page reported has_more=%v next_cursor=%q — a page that stopped short of the "+
			"walk's end says so and hands back where to resume", res.HasMore, res.NextCursor)
	}
}

func TestTheNativeSweepPagesThroughEveryTypeWithoutRepeating(t *testing.T) {
	e := SetupSearch(t)
	seedSweepable(t, e)
	ctx := e.Admin()
	provider := sweepingProvider(e)

	seen := map[ids.UUID]datasource.EntityType{}
	query := datasource.SearchQuery{Text: "Sweepable", Limit: 1}
	for pages := 0; ; pages++ {
		if pages > 12 {
			t.Fatalf("the sweep did not terminate after %d pages, holding %d records", pages, len(seen))
		}
		res, err := provider.Search(ctx, query)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, rec := range res.Records {
			if _, repeated := seen[rec.Ref.ID]; repeated {
				t.Fatalf("the sweep served %s twice — a resumed walk must not re-serve what it handed over", rec.Ref.ID)
			}
			seen[rec.Ref.ID] = rec.Ref.Type
		}
		if res.HasMore != (res.NextCursor != "") {
			t.Fatalf("has_more=%v with next_cursor=%q — a page claiming a remainder it cannot hand back leaves "+
				"those records unreachable, and one claiming completeness it has not established stops the "+
				"caller looking", res.HasMore, res.NextCursor)
		}
		if !res.HasMore {
			break
		}
		query.Cursor = res.NextCursor
	}

	if len(seen) != 6 {
		t.Fatalf("the sweep reached %d of the 6 seeded records: %v", len(seen), seen)
	}
	for _, want := range []datasource.EntityType{datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityLead} {
		found := false
		for _, got := range seen {
			found = found || got == want
		}
		if !found {
			t.Errorf("the sweep never reached a %s — it walks every searchable type or it is not a sweep", want)
		}
	}
}

func TestTheNativeSweepAnswersTheTypesASeatMayReadRatherThanRefusingAll(t *testing.T) {
	e := SetupSearch(t)
	seedSweepable(t, e)

	// A seat with organization read and nothing else. The sweep it asks for
	// covers five types; four of them it may not see.
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	orgOnly := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"organization": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	provider := sweepingProvider(e)

	res, err := provider.Search(orgOnly, datasource.SearchQuery{Text: "Sweepable", Limit: 50})
	if err != nil {
		t.Fatalf("sweeping as a seat that may read one type: %v — a sweep answers what the seat can see, "+
			"and refusing the whole walk for one missing grant makes the advertised all-types search "+
			"unusable for any seat that is not universal", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("the sweep answered %d records, want the 2 organizations this seat may read", len(res.Records))
	}
	for _, rec := range res.Records {
		if rec.Ref.Type != datasource.EntityOrganization {
			t.Errorf("the sweep answered a %s to a seat granted only organizations", rec.Ref.Type)
		}
	}

	// A caller who NAMES one type still hears the denial: they asked about
	// that type, and an empty page would say it holds nothing. The DENIAL
	// specifically — any-error would also accept a pool failure or a missing
	// fixture, which would prove nothing about the path under test.
	_, err = provider.Search(orgOnly, datasource.SearchQuery{
		Text: "Sweepable", EntityTypes: []datasource.EntityType{datasource.EntityPerson},
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("naming a single denied type answered %v, want the denial — a caller who asked about people "+
			"must not be told there are none", err)
	}
}

// A cursor naming a stream this seam does not search is a fault, not a
// finished walk. The mirror sweeps `activity` and this one does not, so a
// token minted over there and presented here — after a cutover, say — would
// otherwise resume "past" a position this provider cannot place and answer a
// complete empty page to a caller holding a real token.
func TestTheNativeSweepRefusesACursorFromAStreamItDoesNotSearch(t *testing.T) {
	e := SetupSearch(t)
	seedSweepable(t, e)

	foreign, err := storekit.EncodeSweepCursor(storekit.SweepCursor{
		Stream: string(datasource.EntityActivity), Inner: "whatever-the-mirror-minted",
	})
	if err != nil {
		t.Fatalf("minting the foreign position: %v", err)
	}
	res, err := sweepingProvider(e).Search(e.Admin(), datasource.SearchQuery{Text: "Sweepable", Cursor: foreign})
	var malformed *storekit.MalformedCursorError
	if !errors.As(err, &malformed) {
		t.Fatalf("a cursor from a stream this seam does not search answered %d records / %v, want the "+
			"malformed-cursor fault — a complete empty page tells a caller mid-walk that there is nothing "+
			"left, which is the answer they cannot check", len(res.Records), err)
	}
}
