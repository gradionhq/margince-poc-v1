// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The company logos, so a seeded installation looks like a CRM someone uses
// rather than a wall of placeholder initials.
//
// The bytes come from the dataset (datasets/v1/logos/<domain>.png), already
// normalized to the product's own shape — imagenorm.SquarePNG at 300px, the
// edge compose/sitelogo.go uses. The dataset stores them because the crawl
// puts a logo straight into the crawling installation's object store and
// never writes it to disk: the URL survives in report.json, the picture does
// not.
//
// Two writes per logo, in the order the store requires. blobstore.Put lands
// the object, then people.SetOrganizationLogo records the reference; that
// store is deliberately blob-free, so a caller writes the object first and
// the row second. The key carries a fresh UUID per attempt, matching
// compose/sitelogo.go's organizationLogoKey, because a key derived from the
// organization alone would let two concurrent writers overwrite each other's
// bytes while the row named whichever committed last.
//
// A second seed writes nothing: the phase reads which organizations already
// carry a dataset-stamped logo_origin and skips them, so re-running neither
// re-uploads nor orphans objects.
//
// The phase is skipped, not failed, when no blobstore is configured: a seed
// against a stack without MinIO should still produce every other record.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/imagenorm"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedLogos uploads each company's committed logo and points its organization
// at the stored object. It reports how many logos landed.
func seedLogos(ctx context.Context, dsn, dataset string, c *client, orgIDs map[string]string, mode runMode) (int, error) {
	if mode == modeDryRun {
		return 0, nil
	}
	blob, configured, err := blobstore.FromEnv(ctx, config.FromOS)
	if err != nil {
		return 0, fmt.Errorf("opening the blobstore for the logo writer: %w", err)
	}
	if !configured {
		// No MinIO in this environment. Every other record still seeds, and
		// saying so beats a silent zero.
		fmt.Println("logos:         skipped — no blobstore configured (MARGINCE_BLOBSTORE_ENDPOINT)")
		return 0, nil
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("opening a pool for the logo writer: %w", err)
	}
	defer pool.Close()

	// The same wiring the fact writer uses: the installation resolves through
	// identity rather than being named, and the session is the one the API
	// rows are attributable to.
	svc := identity.NewService(pool)
	db := database.Bind(pool, svc.InstallationWorkspace)
	actor, err := actorContext(ctx, svc, c)
	if err != nil {
		return 0, err
	}
	wsID, err := svc.InstallationWorkspace(ctx)
	if err != nil {
		return 0, fmt.Errorf("resolving the installation workspace: %w", err)
	}

	// Which organizations this seeder has already given a logo. Every other
	// phase reads before it writes so a second run creates nothing, and
	// without this one the logo phase would re-upload all 149 objects on
	// every seed, orphaning the previous 149 in MinIO each time.
	//
	// The origin prefix is the test, not the presence of a key: a logo the
	// CRAWL resolved, or one a person uploaded, must be left exactly as it
	// is. Only the seeder's own earlier write is a no-op.
	seeded, err := logosAlreadySeeded(ctx, pool)
	if err != nil {
		return 0, err
	}

	store := people.NewStore(db)
	written := 0
	skipped := 0
	logoDir := filepath.Join(dataset, "datasets/v1/logos")
	for domain, orgID := range orgIDs {
		if seeded[orgID] {
			skipped++
			continue
		}
		did, err := seedOneLogo(ctx, blob, store, actor, wsID, logoDir, domain, orgID)
		if err != nil {
			return written, err
		}
		if did {
			written++
		}
	}
	switch {
	case written == 0 && skipped > 0:
		fmt.Printf("logos:         %d already seeded\n", skipped)
	default:
		fmt.Printf("logos:         %d uploaded\n", written)
	}
	return written, nil
}

// logosAlreadySeeded names the organizations whose logo THIS tool wrote, by
// the origin it stamps. A crawl-resolved or human-uploaded logo is absent
// from the result on purpose: those are not the seeder's to replace.
func logosAlreadySeeded(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text FROM organization
		WHERE logo_object_key IS NOT NULL
		  AND logo_origin LIKE 'dataset:margince-demo-database/%'`)
	if err != nil {
		return nil, fmt.Errorf("reading which logos are already seeded: %w", err)
	}
	defer rows.Close()

	seeded := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning a seeded logo row: %w", err)
		}
		seeded[id] = true
	}
	return seeded, rows.Err()
}

// seedOneLogo stores one company's logo and points its organization at the
// object. It reports false, and no error, when the dataset carries no picture
// for that company or a human's upload already holds the field.
func seedOneLogo(
	ctx context.Context,
	blob blobstore.Store,
	store *people.Store,
	actor context.Context, //nolint:containedctx,revive // the seeder's authenticated session, distinct from ctx: it carries the principal the rows are attributed to.
	wsID ids.WorkspaceID,
	logoDir, domain, orgID string,
) (bool, error) {
	// The domain is read back from the installation, so it is not this tool's
	// own string to trust: filepath.Base strips any separator a crafted
	// display domain could carry, and the join can then only name a file
	// directly inside the logo directory.
	name := filepath.Base(strings.ToLower(domain)) + ".png"
	path := filepath.Join(logoDir, name)
	png, err := os.ReadFile(path) //nolint:gosec // G304: the path is logoDir joined with a Base'd name, so it cannot escape the directory.
	if os.IsNotExist(err) {
		// 150 of 171 companies have one; the rest published none the crawl
		// could resolve, or answer 403 to a non-browser client.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	id, err := ids.ParseAs[ids.OrganizationKind](orgID)
	if err != nil {
		return false, fmt.Errorf("organization id for %s: %w", domain, err)
	}
	// The same key shape compose/sitelogo.go's organizationLogoKey mints,
	// which is unexported: workspace / "organization_logo" / <org>/<fresh
	// uuid>. The trailing UUID makes the key unique per attempt, which is what
	// SetOrganizationLogo requires.
	key := blobstore.WorkspaceKey(wsID, "organization_logo", id.String()+"/"+ids.NewV7().String())
	if err := blob.Put(ctx, key, bytes.NewReader(png), int64(len(png)), imagenorm.ContentType); err != nil {
		return false, fmt.Errorf("storing the logo for %s: %w", domain, err)
	}
	// The origin records where the SEED took the bytes from. It is also what
	// makes a second run a no-op, and what keeps this phase off a logo the
	// crawl resolved or a person uploaded.
	origin := "dataset:margince-demo-database/datasets/v1/logos/" + name
	ok, superseded, err := store.SetOrganizationLogo(actor, id, key, origin)
	if err != nil {
		return false, fmt.Errorf("pointing %s at its logo: %w", domain, err)
	}
	if !ok {
		// A human's upload holds the field. Their picture wins, and the object
		// this attempt just wrote is now unreferenced.
		dropUnreferencedLogo(ctx, blob, key, domain)
		return false, nil
	}
	if superseded != nil && *superseded != "" {
		dropUnreferencedLogo(ctx, blob, *superseded, domain)
	}
	return true, nil
}

// dropUnreferencedLogo collects an object no row points at any more — either
// the one this attempt wrote and a human's picture beat, or the one this
// attempt superseded.
//
// A failure here is not worth abandoning the seed for: every record is
// already correct and the cost is one orphaned object in MinIO. It is worth
// SAYING, though, because a silent leak is how a dev bucket fills up with
// pictures nothing renders.
func dropUnreferencedLogo(ctx context.Context, blob blobstore.Store, key, domain string) {
	if err := blob.Delete(ctx, key); err != nil {
		fmt.Printf("logos:         %s left an unreferenced object at %s: %v\n", domain, key, err)
	}
}
