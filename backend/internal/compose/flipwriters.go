// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's migration.Writers over the native stores — the injected
// seam that keeps the migration module blind to the record modules it
// feeds (people/deals/activities each own their write shape; every
// create below rides their audited, event-emitting entry points).
//
// Idempotency: the flip's source is a FROZEN snapshot, so a re-imported
// row (checkpoint replay, or a full re-run) can never carry different
// values than the row that already landed — Ensure answers
// "already landed" without a second write instead of upserting
// identical values through a second audit row.
//
// Fidelity gaps are DISCLOSED, never silent (IEM-FORM-2's "record every
// discarded edge"): a row whose incumbent owner has no mirror_user_map
// entry — or which names no owner at all — is imported under the flip
// operator rather than left ownerless (an ownerless native row is
// workspace-shared, while the mirror row was hidden from every seat),
// and a deal whose raw stage identity doesn't resolve lands on the
// default pipeline's first open stage — each with a disclosure line in
// the run report. OVA-MAP-6 leaves stage materialization open
// upstream; this fallback is the disclosed spec-fill.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// errFlipReplayed aborts a landing that wrote nothing: the store answered with
// a record that already existed under its natural key rather than creating one.
// Rolling back is what keeps the identity row out of the map — see the
// disclosure note at either call site for why that matters.
var errFlipReplayed = errors.New("flip import: the record replayed under its natural key")

// flipWriters implements migration.Writers for the overlay→native flip.
type flipWriters struct {
	pool       *pgxpool.Pool
	people     *people.Store
	deals      *deals.Store
	activities *activities.Store
	ms         *overlay.MirrorStore
	identities *migration.RunStore
	// incumbent names the source system in provenance stamps
	// ("hubspot:person:123" — UC-E11-03's <source>:<object>:<id>) and
	// keys the engine-owned identity map.
	incumbent string
	// runID attributes each identity-map row to the run that landed it.
	runID migration.RunID
	// operator owns records whose incumbent owner did not map. Pre-flip
	// those rows are hidden from EVERY seat (the mirror's fail-closed
	// NULL-owner rule); a native row with a null owner is workspace-
	// shared at every tier, so importing them ownerless would silently
	// widen visibility the cutover was never asked to change.
	operator *ids.UserID
	// nativeIDs caches external key → native id within one run; a resumed
	// run rebuilds entries lazily through lookup, which falls back to the
	// engine-owned identity map.
	nativeIDs map[string]ids.UUID
	// assocs are the estate's edges, set before the run: activity links
	// must ride LogActivity's insert (links are write-once with the row),
	// so EnsureActivity reads its own edges here while Associate applies
	// the person/org/deal edges after every endpoint exists.
	assocs []migration.Assoc
	// stages is the native stage catalog, loaded lazily on the first deal.
	stages *flipStageCatalog
	// ownerOverride resolves an incumbent owner id WITHOUT the live
	// mirror_user_map — the reconstruction path's map comes out of the
	// bundle, because a clean instance has no mirror rows to read.
	ownerOverride map[string]ids.UUID
	// ownerCache memoizes live mirror_user_map lookups for the run.
	ownerCache map[string]ids.UUID
}

// WithOwnerMap resolves owners from an explicit incumbent-user → app-user
// map instead of the live mirror_user_map (reconstruction, flipbundle.go).
func (w *flipWriters) WithOwnerMap(m map[string]ids.UUID) *flipWriters {
	w.ownerOverride = m
	return w
}

// newFlipWriters builds the estate writers for ONE workspace: db is bound to
// the tenant the estate lands in. That is the caller's, not the installation's
// — a reconstruction rebuilds into the workspace whose operator ordered it,
// which on a clean instance is not the one the server resolved at boot.
func newFlipWriters(db *database.DB, ms *overlay.MirrorStore, incumbent string) *flipWriters {
	pool := db.Pool()
	return &flipWriters{
		pool:       pool,
		people:     people.NewStore(db),
		deals:      deals.NewStore(db, DealsInstallation()),
		activities: activities.NewStore(db),
		ms:         ms,
		identities: migration.NewRunStore(db),
		incumbent:  incumbent,
		nativeIDs:  map[string]ids.UUID{},
	}
}

// SetAssociations hands the estate's edges to the writer before the run
// (see the assocs field for why activities need them at insert time).
func (w *flipWriters) SetAssociations(assocs []migration.Assoc) { w.assocs = assocs }

// forRun binds the writer to the run whose identities it records and to
// the operator who inherits unmapped-owner records.
func (w *flipWriters) forRun(runID migration.RunID, operator *ids.UserID) *flipWriters {
	w.runID = runID
	w.operator = operator
	return w
}

var _ migration.Writers = (*flipWriters)(nil)

// provenance is the imported row's source stamp.
func (w *flipWriters) provenance(object, ext string) string {
	return provenance.ReservedSourceSystemPrefix + fmt.Sprintf("%s:%s:%s", w.incumbent, object, ext)
}

// importSourceSystem namespaces the source_system the flip writes on the
// two objects whose stores key their own idempotent replay on
// (source_system, source_id). The prefix is refused at the WIRE
// MAPPERS — people.leadCreateInput and activities.activityLogInput —
// so a caller cannot pre-plant a row under a guessed incumbent id and
// have the store hand it back as already existing. The stores
// themselves accept the namespace, which is how this in-process writer
// can use it; the engine-owned identity map remains the authority for
// "already imported".
func (w *flipWriters) importSourceSystem() string {
	return provenance.ReservedSourceSystemPrefix + w.incumbent
}

// skipReasonNaturalKeyTaken marks an estate row the flip could not land
// because something else already holds its natural key.
const skipReasonNaturalKeyTaken = "natural_key_already_taken"

// skipReasonDuplicateEmail marks an estate contact whose email a native person
// already holds — a merge candidate the flip discloses rather than resolves.
const skipReasonDuplicateEmail = "duplicate_email"

func (w *flipWriters) cacheKey(object, ext string) string { return object + "/" + ext }

// Exists answers whether the row's provenance already landed natively —
// the engine's create-vs-update classification and the resume path both
// read it.
func (w *flipWriters) Exists(ctx context.Context, object, ext string) (bool, error) {
	_, found, err := w.lookup(ctx, object, ext)
	return found, err
}

// lookup answers whether this external id already landed natively, via
// the ENGINE-OWNED identity map — never by reading a row's own
// provenance. Outside the reserved import namespace those columns are
// client-writable, so a caller could pre-plant a row under an incumbent
// id and have the flip treat the real estate record as already
// imported: suppressing it, and capturing the activities that resolve
// through the same identity.
//
// The crash repair (flipreconcile.go) DOES read provenance back, and
// the two are consistent: it matches only the reserved prefix, which no
// client-facing path can write, and only on live rows.
func (w *flipWriters) lookup(ctx context.Context, object, ext string) (ids.UUID, bool, error) {
	if !flipImportable(object) {
		return ids.UUID{}, false, fmt.Errorf("flip import: %q is not an importable object", object)
	}
	if id, ok := w.nativeIDs[w.cacheKey(object, ext)]; ok {
		return id, true, nil
	}
	id, found, err := w.identities.LookupIdentity(ctx, w.incumbent, object, ext)
	if err != nil {
		return ids.UUID{}, false, err
	}
	if found {
		w.nativeIDs[w.cacheKey(object, ext)] = id
	}
	return id, found, nil
}

// flipImportable gates the object names that may reach a lookup or an
// ensure — the allowlist the identity map's own writes rely on.
func flipImportable(object string) bool {
	switch object {
	case flipObjectPerson, flipObjectOrganization, flipObjectDeal, flipObjectLead, flipObjectActivity:
		return true
	default:
		return false
	}
}

// cacheLanded caches an external→native binding whose identity row is already
// COMMITTED.
//
// Split out because the cache must not be written from inside the landing
// transaction: an entry for a landing that then rolled back would make this
// run's later pages, and the association phase, resolve an id that does not
// exist — and `lookup` answers from the cache before it ever asks the map,
// so nothing downstream would catch it.
func (w *flipWriters) cacheLanded(object, ext string, id ids.UUID) {
	w.nativeIDs[w.cacheKey(object, ext)] = id
}

// landRecord commits one native record and its identity-map row in ONE
// transaction, then caches the binding.
//
// create returns the id it wrote; the identity row goes in beside it, so a
// process that dies mid-landing leaves neither — rather than a record the
// resume cannot name and would create a second time. flipreconcile.go remains
// the repair for the classes that still land in two steps (see remember), and
// for orphans already in the estate.
func (w *flipWriters) landRecord(ctx context.Context, object, ext string, create func(tx pgx.Tx) (ids.UUID, error)) (ids.UUID, error) {
	var id ids.UUID
	if err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		var err error
		if id, err = create(tx); err != nil {
			return err
		}
		return w.identities.RecordIdentityTx(ctx, tx, w.runID, w.incumbent, object, ext, id)
	}); err != nil {
		return ids.UUID{}, err
	}
	w.cacheLanded(object, ext, id)
	return id, nil
}

// Ensure lands one estate row through the owning store.
func (w *flipWriters) Ensure(ctx context.Context, object string, row migration.Row) (migration.EnsureResult, error) {
	if id, found, err := w.lookup(ctx, object, row.ExternalID); err != nil {
		return migration.EnsureResult{}, err
	} else if found {
		// The flip's source is a FROZEN snapshot, so an already-landed
		// row cannot differ from what is stored: nothing to rewrite, and
		// the report says so rather than claiming an update.
		//
		// A deal is the exception, because landing it takes TWO steps —
		// born open, then advanced to its terminal stage. An adopted
		// orphan (created before a crash, recovered by the reconcile)
		// completed only the first, so returning Unchanged here would
		// leave a closed-won estate deal parked open forever, reported as
		// converged. Re-asserting the close is idempotent: a deal that
		// already reached its terminal stage needs nothing.
		if object == flipObjectDeal {
			return w.settleAdoptedDeal(ctx, ids.From[ids.DealKind](id), row)
		}
		return migration.EnsureResult{Unchanged: true}, nil
	}
	switch object {
	case flipObjectOrganization:
		return w.ensureOrganization(ctx, row)
	case flipObjectPerson:
		return w.ensurePerson(ctx, row)
	case flipObjectLead:
		return w.ensureLead(ctx, row)
	case flipObjectDeal:
		return w.ensureDeal(ctx, row)
	case flipObjectActivity:
		return w.ensureActivity(ctx, row)
	default:
		return migration.EnsureResult{}, fmt.Errorf("flip import: %q is not an importable object", object)
	}
}

func (w *flipWriters) ensureOrganization(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, flipObjectOrganization)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	name := strings.TrimSpace(fieldString(row.Fields, "display_name"))
	if name == "" {
		name = overlayUnnamed
	}
	in := people.CreateOrganizationInput{
		DisplayName: name,
		Industry:    fieldStringPtr(row.Fields, "industry"),
		OwnerID:     owner,
		Address:     overlayAddress(row.Fields),
		Domains:     flipOrgDomains(row.Fields),
		Source:      w.provenance(flipObjectOrganization, row.ExternalID),
	}
	if band := crmcontracts.OrganizationSizeBand(fieldString(row.Fields, "size_band")); band.Valid() {
		s := string(band)
		in.SizeBand = &s
	}
	if _, err := w.landRecord(ctx, flipObjectOrganization, row.ExternalID, func(tx pgx.Tx) (ids.UUID, error) {
		org, err := w.people.CreateOrganizationTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("flip import: creating organization %s: %w", row.ExternalID, err)
		}
		return ids.UUID(org.Id), nil
	}); err != nil {
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
}

func (w *flipWriters) ensurePerson(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, flipObjectPerson)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	fullName := strings.TrimSpace(fieldString(row.Fields, "full_name"))
	if fullName == "" {
		fullName = overlayUnnamed
	}
	in := people.CreatePersonInput{
		FullName:  fullName,
		FirstName: fieldStringPtr(row.Fields, "first_name"),
		LastName:  fieldStringPtr(row.Fields, "last_name"),
		Title:     fieldStringPtr(row.Fields, "title"),
		OwnerID:   owner,
		Address:   overlayAddress(row.Fields),
		Emails:    flipPersonEmails(row.Fields),
		Source:    w.provenance(flipObjectPerson, row.ExternalID),
	}
	if _, err := w.landRecord(ctx, flipObjectPerson, row.ExternalID, func(tx pgx.Tx) (ids.UUID, error) {
		person, err := w.people.CreatePersonTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("flip import: creating person %s: %w", row.ExternalID, err)
		}
		return ids.UUID(person.Id), nil
	}); err != nil {
		var dup *people.DuplicateEmailError
		if errors.As(err, &dup) {
			// An estate contact whose email already belongs to a native
			// person is a merge candidate, never auto-merged (AC-M9's
			// posture) — disclosed as a skip, not silently dropped. Nothing
			// of it is left behind: the landing rolled back, so no identity
			// row names a person this run did not create.
			return migration.EnsureResult{Skipped: true, SkipReason: skipReasonDuplicateEmail}, nil
		}
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
}

// childAttrIsPrimary is the mapping-declared attribute naming the primary row
// of a child collection.
const childAttrIsPrimary = "is_primary"

// flipPersonEmails shapes the mirrored contact's address into the people
// store's input. The mapper lands a TargetChild under its PARENT key, as one
// row of a collection, never under the dotted To — so this reads it with the
// same helper the wire projection uses; a flat lookup silently returns "" and
// drops every contact's email, and with it the duplicate-email skip
// ensurePerson depends on. Type and primary flag are the row's own declared
// attributes, so the native row inherits what the mapping said rather than an
// assumption. A row carrying neither — the mirror is a cache whose rows are
// rewritten only as the poller next touches the record — is the single primary
// work address one mapped address means.
func flipPersonEmails(fields map[string]any) []people.PersonEmailInput {
	row, email := overlayPersonEmailRow(fields)
	if email == "" {
		return nil
	}
	in := people.PersonEmailInput{Email: email, EmailType: "work", IsPrimary: true}
	if emailType := strings.TrimSpace(fieldString(row, "email_type")); emailType != "" {
		in.EmailType = emailType
	}
	if primary, declared := row[childAttrIsPrimary].(bool); declared {
		in.IsPrimary = primary
	}
	return []people.PersonEmailInput{in}
}

// flipOrgDomains shapes the mirrored company's domain the way flipPersonEmails
// shapes the contact's address — the same child-collection row, carried across
// rather than dropped (the people store normalizes the host, so no pre-cleaning
// here).
func flipOrgDomains(fields map[string]any) []people.OrgDomainInput {
	row, domain := overlayOrgDomainRow(fields)
	if domain == "" {
		return nil
	}
	in := people.OrgDomainInput{Domain: domain, IsPrimary: true}
	if primary, declared := row[childAttrIsPrimary].(bool); declared {
		in.IsPrimary = primary
	}
	return []people.OrgDomainInput{in}
}

func (w *flipWriters) ensureLead(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, flipObjectLead)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	ext := row.ExternalID
	sourceSystem := w.importSourceSystem()
	in := people.CreateLeadInput{
		FullName:     fieldStringPtr(row.Fields, "full_name"),
		Email:        fieldStringPtr(row.Fields, "email"),
		CompanyName:  fieldStringPtr(row.Fields, "company_name"),
		Status:       "new",
		OwnerID:      owner,
		SourceSystem: &sourceSystem,
		SourceID:     &ext,
		Source:       w.provenance(flipObjectLead, ext),
	}
	if _, err := w.landRecord(ctx, flipObjectLead, ext, func(tx pgx.Tx) (ids.UUID, error) {
		lead, created, err := w.people.CreateLeadTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("flip import: creating lead %s: %w", ext, err)
		}
		if !created {
			return ids.UUID{}, errFlipReplayed
		}
		return ids.UUID(lead.Id), nil
	}); err != nil {
		if errors.Is(err, errFlipReplayed) {
			// The identity map did not know this row, yet the store replayed
			// an existing one under the flip's namespaced key. It is NOT
			// adopted into the map: recording a row this run did not create
			// would make the next attempt resolve it as already-imported and
			// converge silently, turning a one-shot disclosure into none.
			return migration.EnsureResult{Skipped: true, SkipReason: skipReasonNaturalKeyTaken}, nil
		}
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
}

func (w *flipWriters) ensureActivity(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	ext := row.ExternalID
	sourceSystem := w.importSourceSystem()
	in := activities.LogActivityInput{
		Kind:         fieldString(row.Fields, "kind"),
		Subject:      fieldStringPtr(row.Fields, "subject"),
		Body:         fieldStringPtr(row.Fields, "body"),
		Direction:    fieldStringPtr(row.Fields, "direction"),
		SourceSystem: &sourceSystem,
		SourceID:     &ext,
		Source:       w.provenance(flipObjectActivity, ext),
	}
	links, err := w.activityLinks(ctx, ext)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	in.Links = links
	if occurred, ok := overlayTime(row.Fields, "occurred_at"); ok {
		in.OccurredAt = &occurred
	}
	if due, ok := overlayTime(row.Fields, "due_at"); ok {
		in.DueAt = &due
	}
	if _, err := w.landRecord(ctx, flipObjectActivity, ext, func(tx pgx.Tx) (ids.UUID, error) {
		activity, created, err := w.activities.LogActivityTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("flip import: logging activity %s: %w", ext, err)
		}
		if !created {
			return ids.UUID{}, errFlipReplayed
		}
		return ids.UUID(activity.Id), nil
	}); err != nil {
		if errors.Is(err, errFlipReplayed) {
			// See ensureLead: not adopted into the identity map, and
			// disclosed rather than treated as converged.
			return migration.EnsureResult{Skipped: true, SkipReason: skipReasonNaturalKeyTaken}, nil
		}
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true}, nil
}
