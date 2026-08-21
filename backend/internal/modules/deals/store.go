// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
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
	// db binds the workspace this store runs for (ADR-0091 §9 step 3).
	db *database.DB
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

	// stampCorrespondence shields the correspondence a concluded deal turns
	// into a Handelsbrief. Injected, because deals may not import activities.
	stampCorrespondence StampCorrespondence
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
	// StampCorrespondence shields the correspondence a concluded deal turns
	// into a Handelsbrief (A165/ADR-0114). It rides here rather than on a
	// WithX setter because every construction site already passes this struct,
	// and a seam a caller can forget is one that silently stops shielding.
	StampCorrespondence StampCorrespondence
	// EnsurePartner refuses a partner_org_id that names a company with no
	// partner programme. `people` owns that table, so the edge is injected
	// here rather than read across the module boundary (ADR-0054).
	EnsurePartner EnsurePartner
}

// EnsurePartner answers whether an organization may be named as a deal's
// partner, inside the caller's own transaction so the answer cannot go stale
// between the check and the write.
type EnsurePartner func(ctx context.Context, tx pgx.Tx, organizationID ids.OrganizationID) error

// NewStore binds the store to the pool every tenant query runs through, and
// to the seam that answers the installation's own values.
func NewStore(db *database.DB, inst Installation) *Store {
	inst = inst.orRefusing()
	return &Store{
		db: db, clock: time.Now, installation: inst,
		stampCorrespondence: inst.StampCorrespondence,
	}
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
	if i.StampCorrespondence == nil {
		i.StampCorrespondence = refusingStamp()
	}
	if i.EnsurePartner == nil {
		i.EnsurePartner = refusingEnsurePartner()
	}
	return i
}

// refusingEnsurePartner is what an un-injected partner check becomes: it
// refuses every attribution rather than admitting every one. A seam that failed
// OPEN here would silently restore the hole it exists to close.
func refusingEnsurePartner() EnsurePartner {
	return func(context.Context, pgx.Tx, ids.OrganizationID) error {
		return errors.New("deals: the EnsurePartner seam was not injected; " +
			"construct this store with installseam.Deals(), which binds people's " +
			"EnsureOrganizationIsPartner")
	}
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

// CustomColumns is the catalog's answer, carried from a caller that had to
// fetch it before it opened its transaction to the seam that runs inside that
// transaction.
//
// The columns are unexported deliberately. They become quoted identifiers in a
// SELECT list and in an UPDATE's SET clause (storekit's customcolumns
// helpers), so a caller able to name its own would be able to widen a read to
// any column of the same table, or to write a core column past the typed input
// this store validates — `fx_rate_to_base` reached through the custom-field
// patch would bypass every money invariant beside it. Only this package can
// populate one, so that is unrepresentable rather than forbidden by comment.
// The zero value is the honest empty answer: core columns only.
type CustomColumns struct {
	cols []fieldcatalog.Column
}

// ErrCustomFieldsNeedTheStoresOwnTransaction refuses custom-field values on a
// caller-opened create.
//
// The catalog those values are matched against is read in a transaction of its
// own, so a caller-opened write cannot obtain it without taking the second
// connection these seams exist to avoid — and a write that dropped the values
// silently would be worse than one that refuses: the deal would come back
// created, missing what the caller sent, with nothing saying why. The
// store-opened entry point beside it carries custom fields exactly as before.
var ErrCustomFieldsNeedTheStoresOwnTransaction = errors.New(
	"deals: a caller-opened create cannot carry custom fields — the store-opened entry point reads the catalog before it opens its transaction")

// ActiveDealColumns is the caller-side half of UpdateDealTx: a caller that
// opens the transaction itself does this read BEFORE opening it, then threads
// the answer in. That is the same order every store-opened entry point uses;
// it is exported only because the caller of a tx-accepting seam is outside
// this package.
//
// Unlike people's twin it takes no grant of its own: its one caller is the
// extraction accept-write, which has already taken deal:update before it
// reaches the write phase, and deal:read is not what that seat holds this for.
func (s *Store) ActiveDealColumns(ctx context.Context) (CustomColumns, error) {
	cols, err := s.activeColumns(ctx)
	if err != nil {
		return CustomColumns{}, err
	}
	return CustomColumns{cols: cols}, nil
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.db.Tx(ctx, fn)
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}
