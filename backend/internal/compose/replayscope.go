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
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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
// than the gap.
type replayTarget struct {
	object      string // RBAC object governing the body (recorded, not yet re-checked)
	objectNote  string // …or why no object grant governs it
	table       string // row-scoped table the body's record lives in
	tableField  string // …or the body field naming that table, for a polymorphic reference
	moduleProbe string // …or the key of a module-owned visibility probe, where the scope rule lives inside a module
	idPath      string // dotted path to the record id inside the recorded body
	pathParam   string // …or the route parameter naming its parent record, for a projection whose body omits it
	rowNote     string // why the body carries no row-scoped record
	// altTable is the table to probe when the recorded body carries altMarker,
	// for a route that answers more than one record shape. A send answers its
	// ACTIVITY when it went now and its SCHEDULED SEND when it will go later
	// (ADR-0104), and the two ids name different tables — probing the wrong one
	// turns a legitimate retry into a 404, which a client then "recovers" from
	// with a fresh key and a second message.
	altTable  string
	altMarker string // the body field whose presence means altTable
}

// The row-scoped tables, and the RBAC objects that mirror them word for word.
// One spelling each, so a typo cannot make two entries disagree in silence.
const (
	tablePerson        = "person"
	tableOrganization  = "organization"
	tableDeal          = "deal"
	tableLead          = "lead"
	tableProject       = "project"
	tableActivity      = "activity"
	tableVoiceProfile  = "voice_profile"
	tableSignal        = "signal"
	tableScheduledSend = "scheduled_send"

	// jsonNull is the literal a present-but-null JSON field decodes to. A field
	// that is present and null carries no record id, which for a discriminator
	// is the same answer as absent.
	jsonNull = "null"

	// probeApproval keys the approvals-owned visibility probe compose injects.
	probeApproval = "approval"

	// probeContract keys the contracts-owned visibility probe. A contract has
	// no owner column, so the generic row-scope path refuses its table outright
	// (auth.ScopeClauseFor rejects a name it does not know) — a replay routed
	// through it would answer 500 for a retry that should replay the stored 201.
	probeContract = "contract"

	// probeDealRoom keys the dealrooms-owned visibility probe. A Deal Room
	// carries no owner column either — its visibility IS its deal's — so the
	// generic row-scope path refuses deal_room by name, exactly as it does
	// contract.
	probeDealRoom = "deal_room"

	// The field an offer names its deal by.
	offerDealField = "deal_id"

	objectOffer         = "offer"
	objectPipeline      = "pipeline"
	objectCustomField   = "custom_field"
	objectSignal        = "signal"
	objectQuota         = "quota"
	objectOfferTemplate = "offer_template"
	objectProduct       = "product"
	objectIntegrations  = "integrations"
	// objectRetentionPolicy governs the retention ladder AND the controller's
	// two decisions about what a statutory obligation holds: what an
	// installation keeps and what it is held to keeping are one authority.
	objectRetentionPolicy = "retention_policy"
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
	"POST /v1/people":                   {object: tablePerson, table: tablePerson, idPath: "id"},
	"PATCH /v1/people/{id}":             {object: tablePerson, table: tablePerson, idPath: "id"},
	"POST /v1/people/{id}/merge":        {object: tablePerson, table: tablePerson, idPath: "id"},
	"POST /v1/leads/{id}/promote":       {object: tablePerson, table: tablePerson, idPath: "person.id"},
	"POST /v1/organizations":            {object: tableOrganization, table: tableOrganization, idPath: "id"},
	"PATCH /v1/organizations/{id}":      {object: tableOrganization, table: tableOrganization, idPath: "id"},
	"POST /v1/organizations/{id}/merge": {object: tableOrganization, table: tableOrganization, idPath: "id"},
	// A profile-field or fact write is an assertion ABOUT the organization and
	// is governed by its grant, so the replay gate resolves against the
	// organization row — the sidecar carries no independent authority. The
	// replayed body is the sidecar row, which has no id of its own on the wire.
	"PATCH /v1/organizations/{id}/profile-fields/{field}":        {object: tableOrganization, table: tableOrganization, pathParam: "id"},
	"POST /v1/organizations/{id}/profile-fields/{field}/confirm": {object: tableOrganization, table: tableOrganization, pathParam: "id"},
	"PATCH /v1/organizations/{id}/facts/{factKey}":               {object: tableOrganization, table: tableOrganization, pathParam: "id"},
	"POST /v1/organizations/{id}/facts/{factKey}/confirm":        {object: tableOrganization, table: tableOrganization, pathParam: "id"},
	"POST /v1/deals":                       {object: tableDeal, table: tableDeal, idPath: "id"},
	"PATCH /v1/deals/{id}":                 {object: tableDeal, table: tableDeal, idPath: "id"},
	"POST /v1/deals/{id}/advance":          {object: tableDeal, table: tableDeal, idPath: "id"},
	"POST /v1/contracts":                   {object: "contract", moduleProbe: probeContract, idPath: "id", rowNote: "a contract carries no owner column; visibility is inherited from its deal or organization, so the contracts store owns the probe"},
	"POST /v1/deal-rooms":                  {object: probeDealRoom, moduleProbe: probeDealRoom, idPath: "id", rowNote: "a Deal Room carries no owner column; its visibility is its parent deal's, so the dealrooms store owns the probe"},
	"POST /v1/projects":                    {object: tableProject, table: tableProject, idPath: "id"},
	"PATCH /v1/projects/{id}":              {object: tableProject, table: tableProject, idPath: "id"},
	"POST /v1/projects/{id}/advance":       {object: tableProject, table: tableProject, idPath: "id"},
	"POST /v1/projects/transfer-ownership": {object: tableProject, rowNote: "the response is a count, not a record: the handover's rows were each gated on the caller's write authority when it ran, and a replay hands back the number alone"},
	"POST /v1/leads":                       {object: tableLead, table: tableLead, idPath: "id"},
	"PATCH /v1/leads/{id}":                 {object: tableLead, table: tableLead, idPath: "id"},
	"POST /v1/leads/{id}/demote":           {object: tableLead, table: tableLead, idPath: "lead.id"},
	"POST /v1/activities":                  {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/tasks":                       {object: tableActivity, table: tableActivity, idPath: "id"},
	"PATCH /v1/activities/{id}":            {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/activities/{id}/relink":      {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/activities/relink-thread":    {object: tableActivity, rowNote: "the response is a count, not a record: every row moved was gated on the caller's write authority when it ran, and a replay hands back the number alone"},
	"POST /v1/activities/relink-bulk":      {object: tableActivity, rowNote: "the response is a count, not a record: every named row was gated on the caller's sight and write authority when it ran, or nothing moved at all"},
	// A send answers its outbound ACTIVITY when it went now, and its SCHEDULED
	// SEND when the caller asked for it later — different tables behind the
	// same "id". scheduled_at is the discriminator because only the second
	// shape carries one.
	//
	// Spelled as a literal rather than through activities.FieldScheduledAt
	// because the fitness gate reads this table with go/ast and can only
	// resolve literals; a constant here would fail it with a parse complaint
	// rather than a finding anyone could act on.
	"POST /v1/activities/{id}/send-email": {
		object: tableActivity, table: tableActivity, idPath: "id",
		altTable: tableScheduledSend, altMarker: "scheduled_at",
	},
	// The account-started send answers with the outbound activity it wrote,
	// so its replay is gated on that activity exactly as the reply's is. The
	// route carries no id of its own — the origin is in the body — which is
	// why the target is resolved from the RESPONSE's id rather than a path
	// parameter.
	"POST /v1/emails": {
		object: tableActivity, table: tableActivity, idPath: "id",
		altTable: tableScheduledSend, altMarker: "scheduled_at",
	},
	// The channel reply answers with the outbound activity it wrote, so its
	// replay is gated on that activity exactly as the mail send's is. It matters
	// more here, not less: a channel send is irreversible with no provider-side
	// idempotency key behind it, so a retried request that re-executed would put
	// a second copy in the customer's chat.
	"POST /v1/activities/{id}/send-message": {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/bookings":                     {object: tableActivity, table: tableActivity, idPath: "id"},
	"POST /v1/voice-profiles":               {object: tableVoiceProfile, table: tableVoiceProfile, idPath: "id"},

	"POST /v1/voice-profiles/{id}/corpus/clear": {object: tableVoiceProfile, table: tableVoiceProfile, idPath: "id"},

	// Bodies with no owner column of their own that hand back a record which
	// has one. An offer without its deal's scope would return that deal's
	// pricing and buyer snapshot to someone who can no longer open the deal.
	"POST /v1/deals/{id}/offers":      {object: objectOffer, table: tableDeal, idPath: offerDealField},
	"POST /v1/offers/{id}/regenerate": {object: objectOffer, table: tableDeal, idPath: offerDealField},
	"POST /v1/offers/{id}/send":       {object: objectOffer, table: tableDeal, idPath: offerDealField},
	"POST /v1/offers/{id}/render":     {object: objectOffer, table: tableDeal, idPath: offerDealField},
	"POST /v1/record-grants": {
		objectNote: "sharing is gated by the manage-sharing permission, which is not an entry in policy.coreObjects",
		tableField: "record_type", idPath: "record_id",
	},

	// Workspace-shared configuration and catalog rows: no owner column, so
	// object RBAC is the whole gate rather than half of it.
	// The controller's two decisions about a held record (A165/ADR-0114 §4).
	// Both answer 204 with no body, so there is no recorded record to probe:
	// a replay re-serves the same empty answer, and the decision itself was
	// already gated on the retention authority when it first ran.
	"POST /v1/retention/restrictions/{activityId}/release": {
		object:  objectRetentionPolicy,
		rowNote: "the response carries no body, so a replay has no record to re-check; the retention authority governs the decision and retention_policy has no owner column",
	},
	"POST /v1/retention/restrictions/{activityId}/pin": {
		object:  objectRetentionPolicy,
		rowNote: "the response carries no body, so a replay has no record to re-check; the retention authority governs the decision and retention_policy has no owner column",
	},
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
	// The two administered lead vocabularies are gated like the field catalog
	// they extend — auth.Require on "custom_field" in every store entry point —
	// and their rows are workspace-shared config with no owner column.
	"POST /v1/lead-sources":                  {object: objectCustomField, rowNote: noOwnerCatalog},
	"PATCH /v1/lead-sources/{id}":            {object: objectCustomField, rowNote: noOwnerCatalog},
	"POST /v1/lead-disqualify-reasons":       {object: objectCustomField, rowNote: noOwnerCatalog},
	"PATCH /v1/lead-disqualify-reasons/{id}": {object: objectCustomField, rowNote: noOwnerCatalog},
	"POST /v1/custom-fields":                 {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	"PATCH /v1/custom-fields/{id}":           {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	"PATCH /v1/custom-fields/{id}/options":   {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	"POST /v1/custom-fields/{id}/retire":     {objectNote: fieldCatalogGate, rowNote: noOwnerCatalog},
	// An approval is row-scoped through its TARGET (approvals.decidable =
	// decision grants AND targetVisible), and that rule lives inside the
	// approvals module — so the probe is borrowed rather than reimplemented
	// here, where a second copy would drift from the one decide.go enforces.
	// Withdrawing the promise instead is not the alternative it looks like:
	// the first attempt decides the approval and mints a single-use token, so
	// a retry would re-execute, fail as already-decided, and lose the token
	// for good.
	"POST /v1/approvals/{id}/approve": {objectNote: "the approval row IS the authority object (ADR-0036); the approvals engine gates it", moduleProbe: probeApproval, pathParam: "id"},
	"POST /v1/data-subject-requests":  {objectNote: "DSR intake is gated by the privacy module's own case rules", rowNote: "a DSR case row, not a domain record"},
	"PUT /v1/onboarding/state":        {objectNote: "per-workspace onboarding progress, gated by session membership in identity", rowNote: "workspace progress, not a record"},

	// The provider connection is installation-wide configuration: one row per
	// provider, no tenant record to scope, gated by the integrations object.
	"PUT /v1/provider-connections/{provider}":   {object: objectIntegrations, rowNote: "the installation's single connection for one provider — one row per provider, so there is no record to scope"},
	"PATCH /v1/provider-connections/{provider}": {object: objectIntegrations, rowNote: "the installation's single connection for one provider — one row per provider, so there is no record to scope"},

	// Replay matters more here than anywhere else in this map: the body queues
	// a PAID provider call, so a retry that re-executed would buy the same
	// answer twice. The one-live-run index already makes a duplicate a no-op;
	// this makes the retry return the same run rather than race that index.
	//
	// The body is a ProviderRun: a spend-ledger row, not a row-scoped record.
	// It carries no person values — only a state, a cost and the categories
	// that were requested — so there is nothing in it that a person's row
	// scope would protect. The person grant still gates the ORIGINAL request
	// through the handler's own EnsureVisible; what a replay hands back is the
	// receipt, which names no subject.
	"POST /v1/people/{id}/enrichment-runs": {object: tablePerson, rowNote: "a run receipt: state, cost and requested categories, carrying no person values to scope"},
}

// replayProbe answers whether the caller may still see one record, for the
// bodies whose scope rule lives inside a module rather than in a row-scoped
// table. Compose injects them at the composition root, so this package borrows
// the module's own rule instead of keeping a second copy that could drift.
type replayProbe func(ctx context.Context, id ids.UUID) error

// ensureReplayVisible re-runs, against the caller as they are NOW, whichever
// gates govern the body about to be replayed. Anything it cannot resolve fails
// CLOSED: the middleware cannot show the caller may still see what it is
// handing back, and serving it on the strength of a parse failure is the one
// outcome this exists to prevent.
func ensureReplayVisible(ctx context.Context, pool *pgxpool.Pool, probes map[string]replayProbe, route, body string) error {
	target, replayable := replayableOperations[route]
	if !replayable {
		// The middleware only claims keys for routes in this table, so this
		// is unreachable; if it is ever reached, the unclassified case is
		// exactly the one that must not pay out.
		return apperrors.ErrNotFound
	}

	if target.table == "" && target.tableField == "" && target.moduleProbe == "" {
		return nil
	}

	if target.moduleProbe != "" {
		probe, wired := probes[target.moduleProbe]
		if !wired {
			// An unwired probe cannot show the caller may still see this, and
			// the composition root is the only place that could have wired it.
			return apperrors.ErrNotFound
		}
		id, err := replayRecordID(ctx, target, body)
		if err != nil {
			return err
		}
		return probe(ctx, id)
	}

	table := target.table
	if target.tableField != "" {
		resolved, err := stringAt(body, target.tableField)
		if err != nil {
			return err
		}
		table = resolved
	}
	// A route that answers two shapes says which one this body is. The marker
	// is a field only the alternate carries, so the choice is read off the
	// recorded body rather than guessed from the route.
	if target.altTable != "" && bodyHasField(body, target.altMarker) {
		table = target.altTable
	}
	id, err := replayRecordID(ctx, target, body)
	if err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		switch table {
		case tableActivity:
			return auth.EnsureActivityContentVisibleLive(ctx, tx, id)
		case tableSignal:
			// A signal has no owner column but is scoped through its subject
			// entity; "no owner_id" is never on its own a reason to skip.
			return auth.EnsureSignalVisibleLive(ctx, tx, id)
		case tableScheduledSend:
			// A scheduled message is the SENDER's own — an unsent body and its
			// blind-copy list are not workspace-readable the way a sent
			// activity is — so the probe is the same scheduled_by predicate the
			// store reads with, not the generic row-scope clause. It also has
			// no archived_at, which the generic probe requires.
			return ensureScheduledSendVisibleLive(ctx, tx, id)
		}
		// LIVE, not merely visible. The recorded body is a frozen snapshot the
		// store itself would no longer serve: Art. 17 erasure anonymizes the
		// person row in place, stamps archived_at and leaves owner_id alone, so
		// a plain visibility probe still answers "yours" and the middleware
		// would hand back the pre-erasure names, e-mails and phone numbers that
		// every live read path now refuses. EnsureVisibleLive also declines to
		// skip the existence half for an unbounded actor, which is the same
		// hole one role wider.
		//
		// auth rejects any name outside its closed row-scoped set, so an
		// unexpected value refuses the replay rather than reaching SQL.
		return auth.EnsureVisibleLive(ctx, tx, table, id)
	})
}

// ensureScheduledSendVisibleLive refuses a replay whose scheduled message no
// longer belongs to the caller. Existence-hiding, like every other row scope:
// somebody else's message is not found rather than forbidden.
func ensureScheduledSendVisibleLive(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return err
	}
	var visible bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM scheduled_send WHERE id = $1 AND scheduled_by = $2)`,
		id, actor.UserID).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// bodyHasField reports whether the recorded body carries a non-null field —
// the discriminator between two response shapes one route can answer.
func bodyHasField(body, field string) bool {
	if field == "" {
		return false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return false
	}
	raw, ok := probe[field]
	return ok && string(raw) != jsonNull
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
