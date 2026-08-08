// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// The per-reader assembly cache both generated company surfaces sit on
// (DOSS-DDL-1/2).
//
// The reader predicate is written into every statement EXPLICITLY. Row-level
// security binds the workspace and not the reader, so a read that leaned on it
// alone would serve one colleague's assembly to another — and the whole reason
// these caches are keyed per reader is that an assembly can name records the
// next reader may not see (DOSS-DDL-N-1/N-2).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// entry is one cached assembly: the payload plus what it takes to decide
// whether this build may still serve it.
type entry struct {
	Fingerprint string
	GeneratedAt time.Time
	GeneratedBy string
	Payload     []byte
}

// readerCache holds the two statements for one cache table. They are spelled
// out rather than built from a table name because SQL assembled from a variable
// is a habit worth not having, even where the variable is a constant here.
type readerCache struct {
	selectOne string
	upsertOne string
}

var dossierCache = readerCache{
	selectOne: `
		SELECT fingerprint, generated_at, generated_by, payload FROM org_dossier
		 WHERE user_id = $1 AND organization_id = $2`,
	upsertOne: `
		INSERT INTO org_dossier (workspace_id, user_id, organization_id, fingerprint,
		                         generated_at, generated_by, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id, user_id, organization_id) DO UPDATE
		SET fingerprint = EXCLUDED.fingerprint,
		    generated_at = EXCLUDED.generated_at,
		    generated_by = EXCLUDED.generated_by,
		    payload = EXCLUDED.payload`,
}

var growthFitCache = readerCache{
	selectOne: `
		SELECT fingerprint, generated_at, generated_by, payload FROM org_growth_fit
		 WHERE user_id = $1 AND organization_id = $2`,
	upsertOne: `
		INSERT INTO org_growth_fit (workspace_id, user_id, organization_id, fingerprint,
		                            generated_at, generated_by, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id, user_id, organization_id) DO UPDATE
		SET fingerprint = EXCLUDED.fingerprint,
		    generated_at = EXCLUDED.generated_at,
		    generated_by = EXCLUDED.generated_by,
		    payload = EXCLUDED.payload`,
}

// load reads this reader's cached assembly. A row that is simply absent is a
// miss, not an error: the first read of any company has nothing cached.
func (c readerCache) load(ctx context.Context, pool *pgxpool.Pool,
	userID ids.UserID, orgID ids.OrganizationID,
) (entry, bool, error) {
	var out entry
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, c.selectOne, userID, orgID).
			Scan(&out.Fingerprint, &out.GeneratedAt, &out.GeneratedBy, &out.Payload)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return entry{}, false, nil
	}
	if err != nil {
		return entry{}, false, fmt.Errorf("read the cached assembly: %w", err)
	}
	return out, true, nil
}

// save replaces this reader's entry. The row is derived content with no audit
// row and no outbox event: there is no business fact here to lose, and a full
// rebuild is the remedy for any corruption.
func (c readerCache) save(ctx context.Context, pool *pgxpool.Pool,
	userID ids.UserID, orgID ids.OrganizationID, written entry,
) error {
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, c.upsertOne,
			storekit.MustWorkspace(ctx), userID, orgID, written.Fingerprint,
			written.GeneratedAt, written.GeneratedBy, written.Payload); err != nil {
			return fmt.Errorf("cache the assembly: %w", err)
		}
		return nil
	})
}
