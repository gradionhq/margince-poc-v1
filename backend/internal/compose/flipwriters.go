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

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

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
}

// WithOwnerMap resolves owners from an explicit incumbent-user → app-user
// map instead of the live mirror_user_map (reconstruction, flipbundle.go).
func (w *flipWriters) WithOwnerMap(m map[string]ids.UUID) *flipWriters {
	w.ownerOverride = m
	return w
}

func newFlipWriters(pool *pgxpool.Pool, ms *overlay.MirrorStore, incumbent string) *flipWriters {
	return &flipWriters{
		pool:       pool,
		people:     people.NewStore(pool),
		deals:      deals.NewStore(pool),
		activities: activities.NewStore(pool),
		ms:         ms,
		identities: migration.NewRunStore(pool),
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
	return fmt.Sprintf("%s:%s:%s", w.incumbent, object, ext)
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

func (w *flipWriters) cacheKey(object, ext string) string { return object + "/" + ext }

// Exists answers whether the row's provenance already landed natively —
// the engine's create-vs-update classification and the resume path both
// read it.
func (w *flipWriters) Exists(ctx context.Context, object, ext string) (bool, error) {
	_, found, err := w.lookup(ctx, object, ext)
	return found, err
}

// lookup answers whether this external id already landed natively, via
// the ENGINE-OWNED identity map. It deliberately does not read the rows'
// own source/source_system columns: those are client-writable on every
// create path, so a caller could pre-plant a row under an incumbent id
// and have the flip treat the real estate record as already imported —
// suppressing it, and capturing the activities that resolve through the
// same identity.
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

// remember records the external→native identity in the engine-owned map
// (and the per-run cache), so a later page, a resumed run, and the
// association phase all resolve the same row.
func (w *flipWriters) remember(ctx context.Context, object, ext string, id ids.UUID) error {
	w.nativeIDs[w.cacheKey(object, ext)] = id
	return w.identities.RecordIdentity(ctx, w.runID, w.incumbent, object, ext, id)
}

// Ensure lands one estate row through the owning store.
func (w *flipWriters) Ensure(ctx context.Context, object string, row migration.Row) (migration.EnsureResult, error) {
	if _, found, err := w.lookup(ctx, object, row.ExternalID); err != nil {
		return migration.EnsureResult{}, err
	} else if found {
		// The flip's source is a FROZEN snapshot, so an already-landed
		// row cannot differ from what is stored: nothing to rewrite, and
		// the report says so rather than claiming an update.
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

// resolveOwner maps the row's incumbent owner id (carried in-band by the
// flip source) onto the mapped app_user. An owner that does not map —
// or a row that names none at all — imports under the flip OPERATOR,
// disclosed: an ownerless native row is workspace-shared at every tier,
// while the mirror row it came from was hidden from every seat.
func (w *flipWriters) resolveOwner(ctx context.Context, row migration.Row, object string) (*ids.UserID, string, error) {
	raw := strings.TrimSpace(fieldString(row.Fields, flipFieldOwnerExternalID))
	if raw == "" {
		// No incumbent owner at all: the mirror row was hidden from every
		// seat (the fail-closed NULL-owner rule), so it inherits the
		// operator rather than becoming a workspace-shared native row.
		return w.operator, fmt.Sprintf("%s %s: the incumbent record names no owner; imported under the flip operator rather than left workspace-visible", object, row.ExternalID), nil
	}
	var id ids.UUID
	var found bool
	if w.ownerOverride != nil {
		id, found = w.ownerOverride[raw]
	} else {
		var err error
		id, found, err = w.ms.ResolveMirrorOwner(ctx, raw)
		if err != nil {
			return nil, "", err
		}
	}
	if !found {
		// Inherited by the operator, not left ownerless: an ownerless
		// native row is visible to every seat, while the mirror row it
		// came from was hidden from all of them.
		return w.operator, fmt.Sprintf("%s %s: incumbent owner %s has no user mapping; imported under the flip operator rather than left workspace-visible", object, row.ExternalID, raw), nil
	}
	owner := ids.From[ids.UserKind](id)
	return &owner, "", nil
}

func flipAddress(fields map[string]any) *crmcontracts.Address {
	raw, ok := fields["address"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	get := func(k string) *string {
		s, ok := raw[k].(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil
		}
		return &s
	}
	addr := &crmcontracts.Address{
		Line1: get("address"), City: get("city"), Region: get("state"),
		PostalCode: get("zip"), Country: get("country"),
	}
	if addr.Line1 == nil && addr.City == nil && addr.Region == nil && addr.PostalCode == nil && addr.Country == nil {
		return nil
	}
	return addr
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
		Address:     flipAddress(row.Fields),
		Source:      w.provenance(flipObjectOrganization, row.ExternalID),
	}
	if band := crmcontracts.OrganizationSizeBand(fieldString(row.Fields, "size_band")); band.Valid() {
		s := string(band)
		in.SizeBand = &s
	}
	org, err := w.people.CreateOrganization(ctx, in)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: creating organization %s: %w", row.ExternalID, err)
	}
	if err := w.remember(ctx, flipObjectOrganization, row.ExternalID, ids.UUID(org.Id)); err != nil {
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
		Address:   flipAddress(row.Fields),
		Source:    w.provenance(flipObjectPerson, row.ExternalID),
	}
	if email := strings.TrimSpace(fieldString(row.Fields, "person_email.email")); email != "" {
		in.Emails = []people.PersonEmailInput{{Email: email, EmailType: "work", IsPrimary: true}}
	}
	person, err := w.people.CreatePerson(ctx, in)
	if err != nil {
		var dup *people.DuplicateEmailError
		if errors.As(err, &dup) {
			// An estate contact whose email already belongs to a native
			// person is a merge candidate, never auto-merged (AC-M9's
			// posture) — disclosed as a skip, not silently dropped.
			return migration.EnsureResult{Skipped: true, SkipReason: "duplicate_email"}, nil
		}
		return migration.EnsureResult{}, fmt.Errorf("flip import: creating person %s: %w", row.ExternalID, err)
	}
	if err := w.remember(ctx, flipObjectPerson, row.ExternalID, ids.UUID(person.Id)); err != nil {
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
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
	lead, created, err := w.people.CreateLead(ctx, in)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: creating lead %s: %w", ext, err)
	}
	if !created {
		// The identity map did not know this row, yet the store replayed
		// an existing one under the flip's namespaced key. It is NOT
		// adopted into the map: recording a row this run did not create
		// would make the next attempt resolve it as already-imported and
		// converge silently, turning a one-shot disclosure into none.
		return migration.EnsureResult{Skipped: true, SkipReason: skipReasonNaturalKeyTaken}, nil
	}
	if err := w.remember(ctx, flipObjectLead, ext, ids.UUID(lead.Id)); err != nil {
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
}

func (w *flipWriters) ensureDeal(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, flipObjectDeal)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	stages, err := w.stageCatalog(ctx)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	rawStage := fieldString(row.Fields, "stage_id")
	placement := stages.place(rawStage)

	name := strings.TrimSpace(fieldString(row.Fields, "name"))
	if name == "" {
		name = overlayUnnamed
	}
	in := deals.CreateDealInput{
		Name:       name,
		Currency:   fieldStringPtr(row.Fields, "currency"),
		PipelineID: placement.pipeline,
		StageID:    placement.birthStage,
		OwnerID:    owner,
		Source:     w.provenance(flipObjectDeal, row.ExternalID),
	}
	if minor, ok := fieldInt64(row.Fields, "amount_minor"); ok {
		in.AmountMinor = &minor
	}
	if closeAt, ok := overlayTime(row.Fields, "expected_close_date"); ok {
		in.ExpectedClose = &closeAt
	}
	deal, err := w.deals.CreateDeal(ctx, in)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: creating deal %s: %w", row.ExternalID, err)
	}
	dealID := ids.From[ids.DealKind](ids.UUID(deal.Id))
	if err := w.remember(ctx, flipObjectDeal, row.ExternalID, ids.UUID(deal.Id)); err != nil {
		return migration.EnsureResult{}, err
	}

	// A closed estate deal is born open (the store's open-birth-stage
	// rule), then advanced to the terminal stage — the same won/lost path
	// a native close takes, FX freeze included.
	if placement.closedStage != nil {
		var lostReason *string
		if placement.closedSemantic == "lost" {
			reason := "imported closed-lost from the incumbent estate"
			lostReason = &reason
		}
		if _, err := w.deals.AdvanceDeal(ctx, dealID, deals.AdvanceDealInput{ToStageID: *placement.closedStage, LostReason: lostReason}); err != nil {
			return migration.EnsureResult{}, fmt.Errorf("flip import: closing imported deal %s: %w", row.ExternalID, err)
		}
	}
	notes := stageDisclosure(placement, rawStage, row.ExternalID)
	if disclosure != "" {
		if notes != "" {
			notes += "; "
		}
		notes += disclosure
	}
	return migration.EnsureResult{Created: true, Disclosure: notes}, nil
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
	activity, created, err := w.activities.LogActivity(ctx, in)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: logging activity %s: %w", ext, err)
	}
	if !created {
		// See ensureLead: not adopted into the identity map, and
		// disclosed rather than treated as converged.
		return migration.EnsureResult{Skipped: true, SkipReason: skipReasonNaturalKeyTaken}, nil
	}
	if err := w.remember(ctx, flipObjectActivity, ext, ids.UUID(activity.Id)); err != nil {
		return migration.EnsureResult{}, err
	}
	return migration.EnsureResult{Created: true}, nil
}
