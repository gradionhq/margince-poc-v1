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
// discarded edge"): an owner with no mirror_user_map row imports
// ownerless, a deal whose raw stage identity doesn't resolve lands on
// the default pipeline's first open stage — each with a disclosure line
// in the run report. OVA-MAP-6 leaves stage materialization open
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
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// flipWriters implements migration.Writers for the overlay→native flip.
type flipWriters struct {
	pool       *pgxpool.Pool
	people     *people.Store
	deals      *deals.Store
	activities *activities.Store
	ms         *overlay.MirrorStore
	// incumbent names the source system in provenance stamps
	// ("hubspot:person:123" — UC-E11-03's <source>:<object>:<id>).
	incumbent string
	// nativeIDs caches external key → native id within one run; a resumed
	// run rebuilds entries lazily through lookupBySource.
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
		incumbent:  incumbent,
		nativeIDs:  map[string]ids.UUID{},
	}
}

// SetAssociations hands the estate's edges to the writer before the run
// (see the assocs field for why activities need them at insert time).
func (w *flipWriters) SetAssociations(assocs []migration.Assoc) { w.assocs = assocs }

var _ migration.Writers = (*flipWriters)(nil)

// provenance is the imported row's source stamp.
func (w *flipWriters) provenance(object, ext string) string {
	return fmt.Sprintf("%s:%s:%s", w.incumbent, object, ext)
}

func (w *flipWriters) cacheKey(object, ext string) string { return object + "/" + ext }

// Exists answers whether the row's provenance already landed natively —
// the engine's create-vs-update classification and the resume path both
// read it.
func (w *flipWriters) Exists(ctx context.Context, object, ext string) (bool, error) {
	_, found, err := w.lookup(ctx, object, ext)
	return found, err
}

func (w *flipWriters) lookup(ctx context.Context, object, ext string) (ids.UUID, bool, error) {
	if id, ok := w.nativeIDs[w.cacheKey(object, ext)]; ok {
		return id, true, nil
	}
	var query string
	var args []any
	// No archived_at filter on either arm: an archived row still holds
	// the provenance key, so re-importing over it would mint a duplicate
	// rather than recognise what already landed. (lead/activity have no
	// archived_at-free natural key to fall back on — their unique index
	// spans archived rows too, so the two arms must agree.)
	switch object {
	case "person", "organization", "deal":
		query = fmt.Sprintf(`SELECT id FROM %s WHERE source = $1`, object)
		args = []any{w.provenance(object, ext)}
	case "lead", "activity":
		query = fmt.Sprintf(`SELECT id FROM %s WHERE source_system = $1 AND source_id = $2`, object)
		args = []any{w.incumbent, ext}
	default:
		return ids.UUID{}, false, fmt.Errorf("flip import: %q is not an importable object", object)
	}
	var id ids.UUID
	found := false
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, query, args...).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("flip import: looking up %s %s by provenance: %w", object, ext, err)
		}
		found = true
		return nil
	})
	if err != nil {
		return ids.UUID{}, false, err
	}
	if found {
		w.nativeIDs[w.cacheKey(object, ext)] = id
	}
	return id, found, nil
}

func (w *flipWriters) remember(object, ext string, id ids.UUID) {
	w.nativeIDs[w.cacheKey(object, ext)] = id
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
	case "organization":
		return w.ensureOrganization(ctx, row)
	case "person":
		return w.ensurePerson(ctx, row)
	case "lead":
		return w.ensureLead(ctx, row)
	case "deal":
		return w.ensureDeal(ctx, row)
	case "activity":
		return w.ensureActivity(ctx, row)
	default:
		return migration.EnsureResult{}, fmt.Errorf("flip import: %q is not an importable object", object)
	}
}

// resolveOwner maps the row's incumbent owner id (carried in-band by the
// flip source) onto the mapped app_user; unmapped imports ownerless with
// a disclosure.
func (w *flipWriters) resolveOwner(ctx context.Context, row migration.Row, object string) (*ids.UserID, string, error) {
	raw := strings.TrimSpace(fieldString(row.Fields, flipFieldOwnerExternalID))
	if raw == "" {
		return nil, "", nil
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
		return nil, fmt.Sprintf("%s %s: incumbent owner %s has no user mapping; imported ownerless", object, row.ExternalID, raw), nil
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
	owner, disclosure, err := w.resolveOwner(ctx, row, "organization")
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
		Source:      w.provenance("organization", row.ExternalID),
	}
	if band := crmcontracts.OrganizationSizeBand(fieldString(row.Fields, "size_band")); band.Valid() {
		s := string(band)
		in.SizeBand = &s
	}
	org, err := w.people.CreateOrganization(ctx, in)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: creating organization %s: %w", row.ExternalID, err)
	}
	w.remember("organization", row.ExternalID, ids.UUID(org.Id))
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
}

func (w *flipWriters) ensurePerson(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, "person")
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
		Source:    w.provenance("person", row.ExternalID),
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
	w.remember("person", row.ExternalID, ids.UUID(person.Id))
	return migration.EnsureResult{Created: true, Disclosure: disclosure}, nil
}

func (w *flipWriters) ensureLead(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, "lead")
	if err != nil {
		return migration.EnsureResult{}, err
	}
	ext := row.ExternalID
	in := people.CreateLeadInput{
		FullName:     fieldStringPtr(row.Fields, "full_name"),
		Email:        fieldStringPtr(row.Fields, "email"),
		CompanyName:  fieldStringPtr(row.Fields, "company_name"),
		Status:       "new",
		OwnerID:      owner,
		SourceSystem: &w.incumbent,
		SourceID:     &ext,
		Source:       w.provenance("lead", ext),
	}
	lead, created, err := w.people.CreateLead(ctx, in)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: creating lead %s: %w", ext, err)
	}
	w.remember("lead", ext, ids.UUID(lead.Id))
	return migration.EnsureResult{Created: created, Disclosure: disclosure}, nil
}

func (w *flipWriters) ensureDeal(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, "deal")
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
		Source:     w.provenance("deal", row.ExternalID),
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
	w.remember("deal", row.ExternalID, ids.UUID(deal.Id))

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
	notes := stages.disclosure(rawStage, row.ExternalID)
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
	in := activities.LogActivityInput{
		Kind:         fieldString(row.Fields, "kind"),
		Subject:      fieldStringPtr(row.Fields, "subject"),
		Body:         fieldStringPtr(row.Fields, "body"),
		Direction:    fieldStringPtr(row.Fields, "direction"),
		SourceSystem: &w.incumbent,
		SourceID:     &ext,
		Source:       w.provenance("activity", ext),
		Links:        w.activityLinks(ext),
	}
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
	w.remember("activity", ext, ids.UUID(activity.Id))
	return migration.EnsureResult{Created: created}, nil
}

// activityLinks resolves the activity's own edges to already-imported
// native targets (activities import last, so every endpoint exists).
// An edge whose target never landed (skipped row) is dropped WITH the
// engine's knowledge via Associate's disclosure path — here it simply
// doesn't become a link.
func (w *flipWriters) activityLinks(activityExt string) []activities.ActivityLinkInput {
	var links []activities.ActivityLinkInput
	for _, a := range w.assocs {
		if a.FromType != "activity" || a.FromID != activityExt {
			continue
		}
		switch a.ToType {
		case "person", "organization", "deal":
			if id, ok := w.nativeIDs[w.cacheKey(a.ToType, a.ToID)]; ok {
				links = append(links, activities.ActivityLinkInput{EntityType: a.ToType, EntityID: id})
			}
		}
	}
	return links
}

// Associate applies one estate edge after the row phase. Activity edges
// were already applied at insert time (see activityLinks); person→org
// edges become employment relationship rows; deal→org edges set the
// deal's organization FK — IEM-FORM-2's detangling, on the edges the
// mirror actually holds. Every non-applied edge returns its reason, so
// the run report discloses it rather than counting it as applied.
func (w *flipWriters) Associate(ctx context.Context, a migration.Assoc) (migration.AssocResult, error) {
	if a.FromType == "activity" || a.ToType == "activity" {
		return migration.AssocResult{Applied: true}, nil // applied at LogActivity insert time
	}
	fromID, fromOK, err := w.lookup(ctx, a.FromType, a.FromID)
	if err != nil {
		return migration.AssocResult{}, err
	}
	toID, toOK, err := w.lookup(ctx, a.ToType, a.ToID)
	if err != nil {
		return migration.AssocResult{}, err
	}
	if !fromOK || !toOK {
		return migration.AssocResult{Reason: "endpoint_not_imported"}, nil
	}
	switch {
	case a.FromType == "deal" && a.ToType == "organization":
		orgID := ids.From[ids.OrganizationKind](toID)
		if _, err := w.deals.UpdateDeal(ctx, ids.From[ids.DealKind](fromID), deals.UpdateDealInput{OrganizationID: &orgID}); err != nil {
			return migration.AssocResult{}, fmt.Errorf("flip import: linking deal %s to organization %s: %w", a.FromID, a.ToID, err)
		}
		return migration.AssocResult{Applied: true}, nil
	case a.FromType == "person" && a.ToType == "organization":
		personID := ids.From[ids.PersonKind](fromID)
		orgID := ids.From[ids.OrganizationKind](toID)
		_, err := w.people.CreateRelationship(ctx, people.CreateRelationshipInput{
			Kind:             "employment",
			PersonID:         &personID,
			OrganizationID:   &orgID,
			IsCurrentPrimary: strings.EqualFold(a.Label, "primary"),
			Source:           w.provenance("relationship", a.FromID+"→"+a.ToID),
		})
		if err != nil {
			// The employment edge is unique per (person, organization):
			// a resumed run replaying its association phase re-offers an
			// edge that already landed, which is convergence, not a
			// failure — every other error still stops the run.
			if errors.Is(err, apperrors.ErrConflict) {
				return migration.AssocResult{Applied: true}, nil
			}
			return migration.AssocResult{}, fmt.Errorf("flip import: creating employment %s→%s: %w", a.FromID, a.ToID, err)
		}
		return migration.AssocResult{Applied: true}, nil
	default:
		return migration.AssocResult{Reason: "unmodelled_edge_shape"}, nil
	}
}

// flipStageCatalog resolves incumbent stage identities onto the native
// stage catalog: an exact (normalized) name match wins; HubSpot's
// canonical closedwon/closedlost keys land on the default pipeline's
// won/lost stages; anything else falls back to the default pipeline's
// first open stage, disclosed.
type flipStageCatalog struct {
	pipeline   ids.PipelineID // the default pipeline
	firstOpen  ids.StageID    // the default pipeline's first open stage
	openIn     map[ids.PipelineID]ids.StageID
	byName     map[string]flipStage
	bySemantic map[string]flipStage
}

type flipStage struct {
	id       ids.StageID
	pipeline ids.PipelineID
	semantic string
}

type flipPlacement struct {
	pipeline       ids.PipelineID
	birthStage     ids.StageID
	closedStage    *ids.StageID
	closedSemantic string
	matched        bool
}

func (c *flipStageCatalog) place(rawStage string) flipPlacement {
	norm := normalizeStageKey(rawStage)
	if st, ok := c.byName[norm]; ok {
		if st.semantic == "open" {
			return flipPlacement{pipeline: st.pipeline, birthStage: st.id, matched: true}
		}
		// A closed match is born on its own pipeline's first open stage
		// (transient — the advance below moves it out immediately); a
		// pipeline with no open stage cannot birth a deal at all, so the
		// whole placement falls back to the default pipeline.
		if birth, ok := c.openIn[st.pipeline]; ok {
			closed := st.id
			return flipPlacement{pipeline: st.pipeline, birthStage: birth, closedStage: &closed, closedSemantic: st.semantic, matched: true}
		}
	}
	if norm == "closedwon" || norm == "closedlost" {
		semantic := "won"
		if norm == "closedlost" {
			semantic = "lost"
		}
		if st, ok := c.bySemantic[semantic]; ok {
			closed := st.id
			return flipPlacement{pipeline: st.pipeline, birthStage: c.firstOpen, closedStage: &closed, closedSemantic: semantic, matched: true}
		}
	}
	return flipPlacement{pipeline: c.pipeline, birthStage: c.firstOpen}
}

func (c *flipStageCatalog) disclosure(rawStage, dealExt string) string {
	if c == nil {
		return ""
	}
	if p := c.place(rawStage); !p.matched {
		if strings.TrimSpace(rawStage) == "" {
			return fmt.Sprintf("deal %s: no incumbent stage identity; placed on the default pipeline's first open stage", dealExt)
		}
		return fmt.Sprintf("deal %s: incumbent stage %q has no native match; placed on the default pipeline's first open stage", dealExt, rawStage)
	}
	return ""
}

func normalizeStageKey(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.TrimSpace(s)))
}

func (w *flipWriters) stageCatalog(ctx context.Context) (*flipStageCatalog, error) {
	if w.stages != nil {
		return w.stages, nil
	}
	cat := &flipStageCatalog{
		openIn: map[ids.PipelineID]ids.StageID{},
		byName: map[string]flipStage{}, bySemantic: map[string]flipStage{},
	}
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id, s.pipeline_id, s.name, s.semantic, p.is_default
			FROM stage s JOIN pipeline p ON p.id = s.pipeline_id AND p.workspace_id = s.workspace_id
			WHERE s.archived_at IS NULL AND p.archived_at IS NULL
			ORDER BY p.is_default DESC, s.position`)
		if err != nil {
			return fmt.Errorf("flip import: reading the native stage catalog: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var st flipStage
			var name, semantic string
			var isDefault bool
			if err := rows.Scan(&st.id, &st.pipeline, &name, &semantic, &isDefault); err != nil {
				return fmt.Errorf("flip import: scanning a native stage: %w", err)
			}
			st.semantic = semantic
			if _, taken := cat.byName[normalizeStageKey(name)]; !taken {
				cat.byName[normalizeStageKey(name)] = st
			}
			if semantic == "open" {
				if _, taken := cat.openIn[st.pipeline]; !taken {
					cat.openIn[st.pipeline] = st.id
				}
			}
			if isDefault {
				if cat.pipeline == (ids.PipelineID{}) {
					cat.pipeline = st.pipeline
				}
				if semantic == "open" && cat.firstOpen == (ids.StageID{}) {
					cat.firstOpen = st.id
				}
				if _, taken := cat.bySemantic[semantic]; !taken && semantic != "open" {
					cat.bySemantic[semantic] = st
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if cat.pipeline == (ids.PipelineID{}) || cat.firstOpen == (ids.StageID{}) {
		return nil, errors.New("flip import: the workspace has no default pipeline with an open stage; seed the workspace before flipping")
	}
	w.stages = cat
	return cat, nil
}
