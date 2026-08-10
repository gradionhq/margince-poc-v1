// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// whereSeed opens a list/read WHERE chain so every optional narrowing
// appends uniformly — the chain is never empty even when no filter applies.
const whereSeed = "1=1"

// liveRowsClause narrows a read to the rows nothing has archived. It lives
// beside the store every one of this module's reads runs through, because
// three of them append it — the offer-template list, the project read and the
// pipeline catalog — and a reader of any one should find it without knowing
// which record type happened to name it first.
const liveRowsClause = " AND archived_at IS NULL"

// Store owns this module's tables (data-seam ownership, ADR-0014 Am.1);
// every write rides the storekit audit+outbox shape in one transaction.
type Store struct {
	pool *pgxpool.Pool
	// catalog is the fieldcatalog seam (custom-field columns); nil means
	// no catalog is wired and every read/write runs core-columns-only.
	catalog fieldcatalog.Reader
	// clock is the "today" source for effective-dated writes (fx_rate);
	// injected so append-forward date validation is deterministic in tests.
	clock func() time.Time
	// installation resolves the values this module needs from the installation
	// itself (ADR-0090/A135). REQUIRED by the constructor: this module FREEZES
	// a conversion rate onto closed deals, so a store that only looked
	// constructed would write a basis it cannot take back.
	installation Installation
}

// InstallationValue resolves ONE installation-identity value inside a
// transaction the caller already holds. Compose supplies the real
// implementations: deals may not import the module that owns the settings, so
// the edge is injected rather than imported (ADR-0054).
type InstallationValue func(context.Context, pgx.Tx) (string, error)

// Installation is the set of them this module reads. A struct rather than
// positional parameters because the three are added to over time and a
// constructor with four bare functions in a row invites a swapped pair that
// still compiles — currency and zone are both strings.
type Installation struct {
	// Name is the display name an offer's issuer snapshot records.
	Name InstallationValue
	// BaseCurrency is the currency amounts are reported in and frozen against.
	BaseCurrency InstallationValue
	// Timezone is the IANA zone a "today" is computed in.
	Timezone InstallationValue
}

// NewStore binds the store to the pool every tenant query runs through, and
// to the seam that answers the installation's own values.
func NewStore(pool *pgxpool.Pool, inst Installation) *Store {
	return &Store{pool: pool, clock: time.Now, installation: inst.orRefusing()}
}

// orRefusing replaces any value the composition left unset with one that
// refuses. An un-injected seam fails CLOSED at the first operation that needs
// it rather than dereferencing nil inside an open transaction; the fields'
// docs call them required, and this is what makes that a check rather than a
// claim. A panic would say it sooner, but this module may not raise one (the
// craft gate's panic-in-domain rule).
func (i Installation) orRefusing() Installation {
	for name, f := range map[string]*InstallationValue{
		"Name": &i.Name, "BaseCurrency": &i.BaseCurrency, "Timezone": &i.Timezone,
	} {
		if *f == nil {
			*f = refusing(name)
		}
	}
	return i
}

func refusing(field string) InstallationValue {
	return func(context.Context, pgx.Tx) (string, error) {
		return "", errors.New("deals: the installation " + field + " seam was not injected; " +
			"construct this store with installseam.Deals(), which binds identity's " +
			"NameOf/BaseCurrencyOf/TimezoneOf")
	}
}

// WithClock overrides the "today" source (tests only). Returns the store
// for chaining.
func (s *Store) WithClock(clock func() time.Time) *Store {
	s.clock = clock
	return s
}

// WithFieldCatalog wires the workspace custom-field catalog in
// (compose injects modules/customfields' Service here — ADR-0054: a
// module never imports a sibling), making active cf_* columns
// participate in deal reads and writes.
func (s *Store) WithFieldCatalog(catalog fieldcatalog.Reader) *Store {
	s.catalog = catalog
	return s
}

// activeColumns answers the workspace's active custom columns for the
// deal object (this store's one record type). It runs its own catalog
// transaction, so callers fetch BEFORE opening their write/read
// transaction (never inside it — a nested pool acquire under load is a
// deadlock shape). A store without a wired catalog answers empty: core
// columns only.
func (s *Store) activeColumns(ctx context.Context) ([]fieldcatalog.Column, error) {
	return s.activeColumnsFor(ctx, "deal")
}

// activeColumnsFor is activeColumns for the module's other record type.
// The rule about fetching BEFORE the transaction opens is the same.
func (s *Store) activeColumnsFor(ctx context.Context, object string) ([]fieldcatalog.Column, error) {
	if s.catalog == nil {
		return nil, nil
	}
	return s.catalog.ActiveColumns(ctx, object)
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return database.WithWorkspaceTx(ctx, s.pool, fn)
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}
