// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Replay governance (API-CC-8): a replay is a read, so a recorded body only
// goes back on the wire if the caller can still see the record it carries.
// Without this a stored response is a receipt that outlives the authority it
// was produced under, and "revocation denies the next request" stops being
// true of the retry — a rep whose grant or ownership is pulled would keep
// collecting the frozen snapshot for the rest of the 24h window.
//
// This table is also the replayable set itself: presence here is what makes an
// operation replay-safe, so the promise cannot be granted without someone
// answering what governs it. Whatever a route does NOT re-check carries its
// reason in the same entry — never silently.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// replayTarget locates the row-scoped record inside a recorded response:
// which table it lives in, and where its id sits in the body. `activity`
// scopes through its links rather than an owner column, so it takes the
// link-walk primitive instead of the owner one. A route that probes nothing
// carries the reason instead.
//
// `object` records which RBAC object governs each body. The OBJECT half of
// the gate is not re-run yet and the field is documentation for now: the
// action to re-check is per-route data, not derivable — `ActionRead` is
// stricter than the original write required (a create-only role would have
// every retry refused), and the HTTP method does not carry it either, since
// `POST /v1/deals/{id}/advance`, `/merge` and `/offers/{id}/send` are updates.
// Guessing it turns legitimate retries into 403s, which is a worse failure
// than the gap. STATUS.md carries what closing it needs.
type replayTarget struct {
	object     string // RBAC object governing the body (recorded, not yet re-checked)
	objectNote string // …or why no object grant governs it
	table      string // row-scoped table the body's record lives in
	tableField string // …or the body field naming that table, for a polymorphic reference
	idPath     string // dotted path to the record id inside the recorded body
	pathParam  string // …or the route parameter naming its parent record, for a projection whose body omits it
	rowNote    string // why the body carries no row-scoped record
}

// The row-scoped tables, and the RBAC objects that mirror them word for word.
// One spelling each, so a typo cannot make two entries disagree in silence.
const (
	tablePerson       = "person"
	tableOrganization = "organization"
	tableDeal         = "deal"
	tableLead         = "lead"
	tableProject      = "project"
	tableActivity     = "activity"
	tableVoiceProfile = "voice_profile"
	tableSignal       = "signal"

	objectOffer         = "offer"
	objectPipeline      = "pipeline"
	objectSignal        = "signal"
	objectQuota         = "quota"
	objectOfferTemplate = "offer_template"
	objectProduct       = "product"
)

// Reasons that recur across entries. Named so the same claim reads as one
// claim rather than several that happen to agree.
const (
	noOwnerCatalog    = "catalog config, no owner column"
	fieldCatalogGate  = "the field catalog is admin-gated inside customfields, which holds no policy.coreObjects entry"
	noOwnerTemplate   = "workspace-shared template config, no owner column"
	noOwnerStage      = "stage config under its pipeline, no owner column"
	noOwnerSignal     = "company-level signal, no owner column"
	profileVersionRow = "a profile version under its parent profile, with no owner column of its own"
)

// replayableOperations mirrors the contract operations that declare the
// IdempotencyKey parameter, keyed by "METHOD <chi route pattern>" exactly like
// agentPolicies. Requests outside this set pass through untouched even when
// they carry the header — the contract scopes the promise, not the client.
// TestIdempotentOperationsMirrorTheContract derives the expected set from
// api/crm.yaml, and TestReplayScopeCoversEveryIdempotentOperation holds each
// entry's governance to what the contract says that route answers.
//
// bookPublicMeeting declares the parameter but is deliberately absent (the
// gate's idempotencyExemptions entry): the anonymous edge binds ONE shared
// system principal for every visitor, so the claim table's per-principal scope
// cannot tell callers apart — one visitor's key + body would replay another's
// recorded confirmation. The anonymous surface needs its own dedupe scope
// (workspace + request digest) before the header's promise can be honored;
// until then the slot's natural key refuses a duplicate booking.
var replayableOperations = map[string]replayTarget{
	// Row-scoped records: both gates apply, and the object and the table are
	// the same word by construction (policy.coreObjects mirrors the table).
	"POST /v1/people":                     {object: tablePerson, table: tablePerson, idPath: "id"},
	"PATCH /v1/people/{id}":               {object: tablePerson, table: tablePerson, idPath: "id"},
	"POST /v1/people/{id}/merge":          {object: tablePerson, table: tablePerson, idPath: "id"},
	"POST /v1/leads/{id}/promote":         {object: tablePerson, table: tablePerson, idPath: "person.id"},
	"POST /v1/organizations":              {object: tableOrganization, table: tableOrganization, idPath: "id"},
	"PATCH /v1/organizations/{id}":        {object: tableOrganization, table: tableOrganization, idPath: "id"},
	"POST /v1/organizations/{id}/merge":   {object: tableOrganization, table: tableOrganization, idPath: "id"},
	"POST /v1/deals":                      {object: tableDeal, table: tableDeal, idPath: "id"},
	"PATCH /v1/deals/{id}":                {object: tableDeal, table: tableDeal, idPath: "id"},
	"POST /v1/deals/{id}/advance":         {object: tableDeal, table: tableDeal, idPath: "id"},
	"POST /v1/projects":                   {object: tableProject, table: tableProject, idPath: "id"},
	"PATCH /v1/projects/{id}":             {object: tableProject, table: tableProject, idPath: "id"},
	"POST /v1/projects/{id}/advance":      {object: tableProject, table: tableProject, idPath: "id"},
	"POST /v1/leads":                      {object: tableLead, table: tableLead, idPath: "id"},
	"PATCH /v1/leads/{id}":                {object: tableLead, table: tableLead, idPath: "id"},
	"POST /v1/activities":                 {object: tableActivity, table: tableActivity, idPath: "id"},
	"PATCH /v1/activities/{id}":           {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/activities/{id}/relink":     {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/activities/{id}/send-email": {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/bookings":                   {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/voice-profiles":             {object: tableVoiceProfile, table: tableVoiceProfile, idPath: "id"},

	"POST /v1/voice-profiles/{id}/corpus/clear": {object: tableVoiceProfile, table: tableVoiceProfile, idPath: "id"},

	// Bodies with no owner column of their own that hand back a record which
	// has one. An offer without its deal's scope would return that deal's
	// pricing and buyer snapshot to someone who can no longer open the deal.
	"POST /v1/deals/{id}/offers":      {object: objectOffer, table: tableDeal, idPath: "deal_id"},
	"POST /v1/offers/{id}/regenerate": {object: objectOffer, table: tableDeal, idPath: "deal_id"},
	"POST /v1/offers/{id}/send":       {object: objectOffer, table: tableDeal, idPath: "deal_id"},
	"POST /v1/offers/{id}/render":     {object: objectOffer, table: tableDeal, idPath: "deal_id"},
	"POST /v1/record-grants": {
		objectNote: "sharing is gated by the manage-sharing permission, which is not an entry in policy.coreObjects",
		tableField: "record_type", idPath: "record_id",
	},

	// Workspace-shared configuration and catalog rows: no owner column, so
	// object RBAC is the whole gate rather than half of it.
	"POST /v1/pipelines":            {object: objectPipeline, rowNote: "pipeline has no owner and is governed by object grants only (auth.EnsureVisible's own note)"},
	"PATCH /v1/pipelines/{id}":      {object: objectPipeline, rowNote: "pipeline config, no owner column"},
	"POST /v1/stages":               {object: objectPipeline, rowNote: noOwnerStage},
	"PATCH /v1/stages/{id}":         {object: objectPipeline, rowNote: noOwnerStage},
	"POST /v1/products":             {object: objectProduct, rowNote: noOwnerCatalog},
	"POST /v1/offer-templates":      {object: objectOfferTemplate, rowNote: noOwnerTemplate},
	"PUT /v1/offer-templates/{id}":  {object: objectOfferTemplate, rowNote: noOwnerTemplate},
	"POST /v1/quotas":               {object: objectQuota, rowNote: "workspace-shared revenue target config (RD-T06), no owner column"},
	"PATCH /v1/quotas/{id}":         {object: objectQuota, rowNote: "workspace-shared revenue target config (RD-T06), no owner column"},
	"POST /v1/signals":              {object: objectSignal, table: tableSignal, idPath: "id"},
	"PATCH /v1/signals/{id}":        {object: objectSignal, table: tableSignal, idPath: "id"},
	"POST /v1/signals/{id}/resolve": {object: objectSignal, table: tableSignal, idPath: "id"},
	"POST /v1/people/{id}/consent":  {object: tablePerson, table: tablePerson, pathParam: "id"},
	"POST /v1/company/site-reads":   {object: tableOrganization, rowNote: "an ingestion job against the installation's own company (A107), not a customer record"},

	"POST /v1/company/site-reads/{readId}/confirm":  {object: tableOrganization, rowNote: "the installation's singleton company profile — one org per installation (A107), so there is no row to scope"},
	"POST /v1/voice-profiles/{id}/builds":           {object: tableVoiceProfile, table: tableVoiceProfile, pathParam: "id"},
	"POST /v1/voice-profiles/{id}/draft-rejections": {object: tableVoiceProfile, table: tableVoiceProfile, pathParam: "id"},
	"POST /v1/voice-profiles/{id}/sources":          {object: tableVoiceProfile, table: tableVoiceProfile, pathParam: "id"},

	"POST /v1/voice-profiles/{id}/versions/{profileVersion}/apply":    {object: tableVoiceProfile, table: tableVoiceProfile, pathParam: "id"},
	"POST /v1/voice-profiles/{id}/versions/{profileVersion}/reject":   {object: tableVoiceProfile, table: tableVoiceProfile, pathParam: "id"},
	"POST /v1/voice-profiles/{id}/versions/{profileVersion}/rollback": {object: tableVoiceProfile, table: tableVoiceProfile, pathParam: "id"},

	// Surfaces whose module gates on something other than a coreObject, so
	// there is no object grant for this middleware to re-check.
	"POST /v1/custom-fields":               {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	"PATCH /v1/custom-fields/{id}":         {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	"PATCH /v1/custom-fields/{id}/options": {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	"POST /v1/custom-fields/{id}/retire":   {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	// KNOWN GAP, deliberately left open rather than closed the wrong way. An
	// approval IS row-scoped — through its TARGET (approvals.decidable =
	// decision grants AND targetVisible) — so replaying returns
	// proposed_change, evidence and the ADR-0036 token for a target the caller
	// may since have lost. The probe lives inside approvals, where this
	// package cannot reach it, and withdrawing the promise instead is WORSE:
	// the first attempt decides the approval and mints a single-use token, so
	// a retry re-executes, fails as already-decided, and the token is lost for
	// good — a 🟡 action nobody can redeem, on any dropped response. Closing it
	// needs an approvals-owned visibility probe injected here, the way compose
	// already injects the approvals adapter. STATUS.md carries it.
	"POST /v1/approvals/{id}/approve": {objectNote: "the approval row IS the authority object (ADR-0036); the approvals engine gates it", rowNote: "row-scoped through its target, but the probe is not reachable from this package — see the note above"},
	"POST /v1/data-subject-requests":  {objectNote: "DSR intake is gated by the privacy module's own case rules", rowNote: "a DSR case row, not a domain record"},
	"PUT /v1/onboarding/state":        {objectNote: "per-workspace onboarding progress, gated by session membership in identity", rowNote: "workspace progress, not a record"},
}

// ensureReplayVisible re-runs, against the caller as they are NOW, whichever
// gates govern the body about to be replayed. Anything it cannot resolve fails
// CLOSED: the middleware cannot show the caller may still see what it is
// handing back, and serving it on the strength of a parse failure is the one
// outcome this exists to prevent.
func ensureReplayVisible(ctx context.Context, pool *pgxpool.Pool, route, body string) error {
	target, replayable := replayableOperations[route]
	if !replayable {
		// The middleware only claims keys for routes in this table, so this
		// is unreachable; if it is ever reached, the unclassified case is
		// exactly the one that must not pay out.
		return apperrors.ErrNotFound
	}

	if target.table == "" && target.tableField == "" {
		return nil
	}

	table := target.table
	if target.tableField != "" {
		resolved, err := stringAt(body, target.tableField)
		if err != nil {
			return err
		}
		table = resolved
	}
	id, err := replayRecordID(ctx, target, body)
	if err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		switch table {
		case tableActivity:
			return auth.EnsureActivityVisible(ctx, tx, id)
		case tableSignal:
			// A signal has no owner column but is scoped through its subject
			// entity; "no owner_id" is never on its own a reason to skip.
			return auth.EnsureSignalVisible(ctx, tx, id)
		}
		// auth rejects any name outside its closed row-scoped set, so an
		// unexpected value refuses the replay rather than reaching SQL.
		return auth.EnsureVisible(ctx, tx, table, id)
	})
}

// replayRecordID resolves the record whose scope governs this body: from the
// recorded body where it names its own record, or from the route parameter
// naming its parent where the body is a projection that omits it.
func replayRecordID(ctx context.Context, target replayTarget, body string) (ids.UUID, error) {
	if target.pathParam == "" {
		return recordIDAt(body, target.idPath)
	}
	raw := chi.RouteContext(ctx).URLParam(target.pathParam)
	id, err := ids.Parse(raw)
	if err != nil {
		return ids.UUID{}, apperrors.ErrNotFound
	}
	return id, nil
}

// recordIDAt walks a dotted path to the record id in a recorded body.
func recordIDAt(body, path string) (ids.UUID, error) {
	raw, err := stringAt(body, path)
	if err != nil {
		return ids.UUID{}, err
	}
	id, err := ids.Parse(raw)
	if err != nil {
		return ids.UUID{}, apperrors.ErrNotFound
	}
	return id, nil
}

// stringAt walks a dotted path to a string in a recorded body. Every miss is
// ErrNotFound rather than a distinct error: whichever way the body surprised
// us, the middleware cannot prove the caller may still see what it is about to
// hand back, and that is the client-visible fact.
func stringAt(body, path string) (string, error) {
	var node any
	if err := json.Unmarshal([]byte(body), &node); err != nil {
		return "", apperrors.ErrNotFound
	}
	for _, segment := range strings.Split(path, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			return "", apperrors.ErrNotFound
		}
		if node, ok = object[segment]; !ok {
			return "", apperrors.ErrNotFound
		}
	}
	text, ok := node.(string)
	if !ok {
		return "", apperrors.ErrNotFound
	}
	return text, nil
}
