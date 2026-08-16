// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The company facts the site reader extracted — what each company sells, who
// it serves, what it runs on. About seventy per company, and the richest
// thing the crawl produces.
//
// These are written by calling the product's OWN writer in process, which is
// the only honest way to place them:
//
//   - There is no create endpoint. `POST /organizations/{id}/facts` does not
//     exist and the PATCH that does can only CORRECT a fact a deep read
//     already produced. A fact carries evidence, so the product lets a crawl
//     create one and nothing else.
//   - Going through a crawl would mean re-reading 21 sites on every seed:
//     real model spend, real outbound traffic, and the end of the
//     offline-and-free property the dataset's two caches exist for.
//   - Writing the rows by hand would mean hand-rolling the audit row and the
//     outbox envelope too — a validated structure with a stream router and a
//     trace — and an envelope subtly wrong is a row the fan-out silently
//     never delivers.
//
// So the seeder authenticates as its own session, borrows the store, and
// calls people.ApplyDeepRead. The facts land through the same fill-empty and
// human-precedence machinery a human accept uses, with the same audit and
// outbox pair, and they stay correct when that writer changes.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedFacts writes every accepted company's facts through the real writer.
func seedFacts(ctx context.Context, dsn string, c *client, companies []company, orgIDs map[string]string, mode runMode) (int, error) {
	if mode == modeDryRun {
		return 0, nil
	}
	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("opening a pool for the fact writer: %w", err)
	}
	defer pool.Close()

	// The same wiring compose uses: the handle resolves the singleton
	// installation through identity rather than being told which workspace it
	// is in, so the seeder cannot address one the product would not.
	svc := identity.NewService(pool)
	db := database.Bind(pool, svc.InstallationWorkspace)

	// The same session the rest of the seed writes under, resolved through the
	// product's own authentication, so these rows are attributable to exactly
	// the principal the API rows are.
	actor, err := actorContext(ctx, svc, c)
	if err != nil {
		return 0, err
	}

	store := people.NewStore(db)
	written := 0
	for _, comp := range companies {
		orgID, ok := orgIDs[strings.ToLower(comp.Domain)]
		if !ok || len(comp.Facts) == 0 {
			continue
		}
		parsed, err := ids.ParseAs[ids.OrganizationKind](orgID)
		if err != nil {
			return written, fmt.Errorf("%s: %w", comp.Domain, err)
		}
		proposal := people.DeepReadProposal{
			OrganizationID: parsed,
			SourceURL:      comp.SeedURL,
			Facts:          deepReadFacts(comp.Facts),
		}
		// ApplyDeepRead is an upsert, so re-running re-sends every fact and
		// changes none. Counting what was sent would report 1550 applied on a
		// run that did nothing; counting the rows that were absent beforehand
		// says what actually happened.
		before, err := factCount(actor, pool, orgID)
		if err != nil {
			return written, err
		}
		if err := store.ApplyDeepRead(actor, proposal); err != nil {
			return written, fmt.Errorf("applying facts for %s: %w", comp.Domain, err)
		}
		after, err := factCount(actor, pool, orgID)
		if err != nil {
			return written, err
		}
		written += after - before
	}
	return written, nil
}

func factCount(ctx context.Context, pool *pgxpool.Pool, orgID string) (int, error) {
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM organization_fact WHERE organization_id = $1`, orgID).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting facts: %w", err)
	}
	return n, nil
}

// deepReadFacts converts the dataset's reviewed facts into the writer's shape.
// Every field maps straight across: the dataset stores what the crawl found,
// which is what the writer expects.
func deepReadFacts(facts []fact) []people.DeepReadFact {
	out := make([]people.DeepReadFact, 0, len(facts))
	for _, f := range facts {
		out = append(out, people.DeepReadFact{
			Category:        f.Category,
			Field:           f.Field,
			Value:           f.Value,
			ValueKey:        f.ValueKey,
			EvidenceSnippet: f.EvidenceSnippet,
			SourceURL:       f.SourceURL,
			Confidence:      f.Confidence,
		})
	}
	return out
}

// seedFactActor is the provenance stamp a seeded fact carries, matching the
// shape the product's own auto-enrich sweep uses (system:capture_auto_enrich).
//
// A SYSTEM principal rather than the human running the seeder, and the
// difference is load-bearing twice over. It does not impersonate anybody: a
// fact no person vouched for must not read as though one did. And the fact
// upsert refuses to overwrite a row whose captured_by starts with "human:",
// so stamping these as human would freeze them — the next crawl, and every
// correction after it, would be silently discarded.
const seedFactActor = "system:seed_demo"

// actorContext binds the seeding system principal, in the workspace the
// installation resolves for itself.
//
// The session is still authenticated first, so the seeder cannot write into
// an installation it could not sign in to — the credential check is real even
// though the resulting rows are attributed to the system rather than to the
// human who holds it.
func actorContext(ctx context.Context, svc *identity.Service, c *client) (context.Context, error) {
	token, err := c.sessionToken()
	if err != nil {
		return nil, err
	}
	id, err := svc.Authenticate(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("resolving the seeding session: %w", err)
	}

	actor := principal.WithWorkspaceID(ctx, id.WorkspaceID.UUID)
	actor = principal.WithActor(actor, principal.Principal{
		Type: principal.PrincipalSystem, ID: seedFactActor,
	})
	// Every event a write emits carries this, so the whole seeding run reads
	// back as one story rather than as N unrelated changes.
	return principal.WithCorrelationID(actor, ids.NewV7()), nil
}

// sessionToken is the opaque cookie the login handed back — the same
// credential the HTTP calls travel under.
func (c *client) sessionToken() (string, error) {
	base, err := url.Parse(c.base)
	if err != nil {
		return "", fmt.Errorf("parsing the base URL: %w", err)
	}
	for _, cookie := range c.http.Jar.Cookies(base) {
		if cookie.Name == sessionCookieName {
			return cookie.Value, nil
		}
	}
	return "", fmt.Errorf("no %s cookie — the seeder is not signed in", sessionCookieName)
}

// sessionCookieName is what the login sets; named here so a rename shows up
// as a compile-adjacent grep rather than as an empty token at run time.
const sessionCookieName = "crm_session"
