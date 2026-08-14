// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// A read id lives only in the browser tab that started the crawl. Everything
// here exists so a company page loaded later can still find out that the crawl
// failed — the state that made an account look unenriched when it was in fact
// unreadable, and left a draft with nothing to write from.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The newest read wins, so a retry supersedes the attempt before it rather than
// the page reporting whichever row the database happened to return first.
func TestLatestSiteReadAnswersTheNewestAttempt(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgForRead(ctx, t, e)

	first, _, err := e.store.StartSiteRead(ctx, orgID, "https://voltaq.test", "human:"+e.rep.String())
	if err != nil {
		t.Fatalf("start the first read: %v", err)
	}
	// The create-or-join rule returns the SAME row for a second start while one
	// is open, which is correct and is not what this test is about: close the
	// first so the second is a genuinely new attempt.
	failRead(ctx, t, e, first.ID)

	second, _, err := e.store.StartSiteRead(ctx, orgID, "https://voltaq.test", "human:"+e.rep.String())
	if err != nil {
		t.Fatalf("start the second read: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("the second start joined the finished read instead of opening a new one")
	}

	latest, err := e.store.LatestSiteRead(ctx, orgID)
	if err != nil {
		t.Fatalf("read the latest: %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("expected the newest read %s, got %s", second.ID, latest.ID)
	}
}

// A failed crawl is exactly the state the company page could not see, so the
// status has to survive the read rather than being smoothed into something that
// looks like an account nobody tried to enrich.
func TestLatestSiteReadCarriesAFailedStatus(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgForRead(ctx, t, e)

	read, _, err := e.store.StartSiteRead(ctx, orgID, "https://voltaq.test", "human:"+e.rep.String())
	if err != nil {
		t.Fatalf("start the read: %v", err)
	}
	failRead(ctx, t, e, read.ID)

	latest, err := e.store.LatestSiteRead(ctx, orgID)
	if err != nil {
		t.Fatalf("read the latest: %v", err)
	}
	if latest.Status != "failed" {
		t.Fatalf("expected the failed status to survive, got %q", latest.Status)
	}
	if latest.PagesRead != 0 {
		t.Fatalf("a failed crawl read no pages, got %d", latest.PagesRead)
	}
}

// Never read and read-but-empty are different answers, and the page renders
// them differently: one offers a first crawl, the other reports a failure.
func TestLatestSiteReadIsNotFoundWhenNothingWasEverRead(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	orgID := seedOrgForRead(ctx, t, e)

	if _, err := e.store.LatestSiteRead(ctx, orgID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an account never read, got %v", err)
	}
}

// An account the caller cannot see must answer the same not-found as one that
// was never read: the existence of a crawl is itself information about a record
// they were not granted.
func TestLatestSiteReadHidesAnAccountTheCallerCannotSee(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	unknown := ids.From[ids.OrganizationKind](ids.NewV7())
	if _, err := e.store.LatestSiteRead(ctx, unknown); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("expected existence-hiding ErrNotFound, got %v", err)
	}
}

func seedOrgForRead(ctx context.Context, t *testing.T, e *dedupeEnv) ids.OrganizationID {
	t.Helper()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Voltaq Systems GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "voltaq.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}
	return ids.From[ids.OrganizationKind](ids.UUID(org.Id))
}

// failRead closes a read the way a real crawl failure closes one. The engine
// that would do it lives in the worker role, which this store test does not
// run — but the shape is not a detail this test may invent: site_read_outcome_shape
// requires a failed row to name its code and detail, and a test that wrote a
// bare status would be asserting against a row the product never produces.
func failRead(ctx context.Context, t *testing.T, e *dedupeEnv, readID ids.UUID) {
	t.Helper()
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE site_read
			   SET status = 'failed', status_code = 'http_server_error',
			       status_detail = 'the site answered 503 on every attempt',
			       finished_at = now()
			 WHERE id = $1`, readID)
		return err
	})
	if err != nil {
		t.Fatalf("fail read %s: %v", readID, err)
	}
}
