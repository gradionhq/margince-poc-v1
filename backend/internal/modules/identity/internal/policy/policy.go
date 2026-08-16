// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package policy owns the role permission-policy documents (B-EP03.1,
// data-model §2.4): the JSONB shape stored in role.permissions, the five
// seeded system-role defaults, the validator that keeps
// a policy honest, and the merge that resolves a user's role set into
// one effective principal.Permissions at authentication time.
package policy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// coreObjects is the closed set of RBAC-governed object types
// (features/04 §1). A policy naming anything else is rejected — a typo'd
// object would otherwise silently grant nothing and read as a bug in the
// role, not the document.
var coreObjects = []string{"person", "organization", "deal", "lead", "activity", "pipeline", "list", "tag", "relationship", "partner", "automation", "voice_profile", "product", "offer", "signal", "saved_view", "custom_field", "computed_field", "quota", "offer_template", "overlay_connection", "embedding_reindex", "webhook_subscription", "fx_rate", "ai_model_rate", "capture_settings", "project", "channel_connection", "import_run", "installation_settings", "finance", "integrations", "retention_policy", "capture_trace", "license", "contract"}

// IsCoreObject reports whether an RBAC object is in the closed set a role
// document may grant. Parse enforces it on stored documents; it is also the
// answer to "could ANY principal ever hold a grant on this object", which a
// caller deriving an authority requirement needs before trusting it.
func IsCoreObject(object string) bool {
	return slices.Contains(coreObjects, object)
}

// Document is the role.permissions JSONB shape:
// {"objects": {"<object>": {"create":…,"read":…,"update":…,"delete":…}},
//
//	"row_scope": "own"|"team"|"all", "field_masks": […]}.
type Document struct {
	Objects  map[string]grant   `json:"objects"`
	RowScope principal.RowScope `json:"row_scope"`
	// FieldMasks is carried for shape-completeness; enforcement is
	// B-EP03.4 (field-level masking), not built yet.
	FieldMasks []string `json:"field_masks,omitempty"`
}

type grant struct {
	Create bool `json:"create"`
	Read   bool `json:"read"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

// crud/read are the two grant rows every default builds from.
var (
	crud       = grant{Create: true, Read: true, Update: true, Delete: true}
	readOnly   = grant{Read: true}
	readUpdate = grant{Read: true, Update: true}
	// writeNoDelete is the append-forward config posture: create + read +
	// same-day-correct (update), never delete. The rate sheets (fx_rate,
	// ai_model_rate) have no delete surface at all — a past-dated row prices
	// historical rollups and must never disappear — so no role holds delete.
	writeNoDelete = grant{Create: true, Read: true, Update: true}
)

// defaults are the seeded system-role policies (they encode
// the choices: reps work team-scoped without delete; managers are
// team-scoped with delete; pipeline, automation, custom-field config AND
// quota targets are admin/ops-owned — each reshapes what the system does
// (or stores) on everyone's records, so they follow the pipeline-config
// posture: everyone reads the catalog, only admin/ops change it (quota's
// createQuota/updateQuota/archiveQuota carry the matching x-agent-access:
// human-only gate in the contract — a target is never agent-set).
// computed_field is read-only for every role, admin/ops included —
// RD-AC-7: no runtime formula-authoring surface exists, so there is no
// write to grant). offer_template follows the SAME posture as product/
// offer, not the pipeline-config posture: it's the offer's own branding
// input, not a locked-down schema surface, so reps create and work
// templates like any other offer-adjacent record; delete stays manager/
// admin/ops (archiveOfferTemplate carries no x-agent-access gate — any
// role holding delete may call it directly). overlay_connection follows
// the SAME posture as quota (0068_quota_rbac.up.sql's backfill precedent):
// connecting/disconnecting the workspace's incumbent binding is
// destructive workspace-wide config (it purges the mirror and flips
// sor_mode for everyone), so create/update/delete are admin/ops-only;
// every role may read the connection status (a rep needs to see whether
// overlay mode is live, the same as a quota's attainment read).
// embedding_reindex has no create/delete surface at all — it is a single
// deployment-level trigger, not a record kind — so only read and update
// are ever granted, and both are admin/ops-only: admin/ops may update
// (trigger a reindex; the confirm route itself carries x-agent-access:
// human-only in the contract, so this grant only ever fires from a
// human session, never an agent), and admin/ops alone may read it — the
// banner/card that consumes the read is itself ops-gated in the SPA, so
// manager/rep/read_only have no legitimate consumer of this object and
// get the zero grant, unlike quota's attainment or
// overlay_connection's status which every role legitimately reads.
// webhook_subscription follows the SAME admin/ops-owned posture: a
// subscription registers outbound egress of governed events, so managing
// the fan-out surface is workspace integration config (create/update/
// delete admin/ops-only); every role may read subscriptions and their
// delivery health. (UC-E10-04 narrates a Rep registering one; that
// posture question is tracked upstream against the spec, not settled
// here.)
// channel_connection follows overlay_connection's posture exactly: a bot
// bound at the workspace level carries every seat's inbound channel traffic,
// so create/update/delete are admin/ops-only, while every role may read the
// binding's status (a rep needs to know whether the channel is live before
// expecting a reply to arrive there).
// import_run is admin/ops-only on EVERY verb, read included: a
// migration run is a workspace-wide bulk mutation of the estate (the
// overlay→native flip executes through it), and unlike quota or
// overlay_connection there is no per-rep read surface — the mode-flip
// and migrate-in screens are admin surfaces.
// managerObjects is the grid a team lead (`manager`) and the whole-organization
// `management` seat share; only their row scope differs.
var managerObjects = objects(crud, crud, crud, crud, crud, readOnly, crud, crud, crud, crud, readOnly, crud, crud, crud, crud, crud, readOnly, readOnly, readOnly, crud, readOnly, grant{}, readOnly, grant{}, grant{}, grant{Create: true, Read: true}, crud, readOnly, grant{}, readOnly, readOnly, readOnly, grant{}, readOnly, grant{}, crud)

var defaults = map[string]Document{
	"admin": {
		Objects:  objects(crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, readOnly, crud, crud, crud, readUpdate, crud, writeNoDelete, writeNoDelete, writeNoDelete, crud, crud, crud, readUpdate, crud, crud, crud, readOnly, readOnly, crud),
		RowScope: principal.RowScopeAll,
	},
	// management is the sales leader's seat (ADR-0110): the manager grid over
	// EVERY row in the organization, and no admin power. Governance actions
	// (inviting users, changing roles, editing role policy, binding another
	// user's passport, issuing password links) key on the literal admin role,
	// so an unbounded row scope here widens what management sees, never what
	// it administers. The two grids are one variable so they cannot drift.
	"management": {
		Objects:  managerObjects,
		RowScope: principal.RowScopeAll,
	},
	"manager": {
		Objects:  managerObjects,
		RowScope: principal.RowScopeTeam,
	},
	"rep": {
		// Reps create and work records but never delete them — except
		// leads, where disqualify IS the delete and is routine rep work.
		// Lists and tags are everyday organizational surfaces: reps
		// create and use them; archiving stays manager/admin. A voice
		// profile is the rep's own working material: create/maintain
		// yes, delete stays manager/admin. Rate-card products, offers and
		// warm-room signals follow the record posture: reps create and
		// work them, delete stays manager/admin. An offer template
		// follows the same posture (see the comment above defaults). A
		// saved view is the rep's own per-user view state (owner-scoped
		// in the store) — full self-service, including deleting one's own
		// view. A quota is read-only even for its own owner: the target
		// itself is admin/ops-set config, not the rep's working material —
		// only the attainment READ is the rep's to consult.
		Objects: objects(
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			crud,
			grant{Create: true, Read: true, Update: true},
			readOnly,
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			readOnly,
			readOnly,
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			crud,
			readOnly,
			readOnly,
			readOnly,
			grant{Create: true, Read: true, Update: true},
			readOnly,
			grant{},
			readOnly,
			grant{}, // fx_rate — admin/ops-only
			grant{}, // ai_model_rate — admin/ops-only
			// capture_settings — everyone reads (a rep sees whether
			// auto-enrich is on), only admin/ops toggle it or carve a
			// domain back out of the consumer-mail baseline. `create` is
			// the one write a rep holds: contributing a consumer domain
			// the shipped baseline missed (CAP-PARAM-5) is everyday
			// judgment about the mail they read, not workspace config.
			grant{Create: true, Read: true},
			// project — the record posture: reps create and work a body
			// of work, archiving stays manager/admin/ops.
			grant{Create: true, Read: true, Update: true},
			// channel_connection — everyone reads (a rep needs to know
			// whether the channel is live), only admin/ops bind one.
			readOnly,
			grant{},  // import_run — admin/ops-only
			readOnly, // installation_settings — the base currency and zone
			readOnly, // finance — a rep reads whether the customer pays on time
			// integrations — a rep reads whether a provider is connected, so
			// a dated value on a person record has an explanation; connecting
			// one spends money and is admin/ops.
			readOnly,
			// retention_policy — admin/ops-only, read included (see objects()).
			grant{},
			// capture_trace — a rep debugs no shared bot binding, and their own
			// capture activity needs no grant.
			grant{},
			// license — admin/ops-only. A rep reads their own seat elsewhere;
			// the workspace's entitlement is not theirs to see (UC-ADMIN-03 F1).
			grant{},
			// A rep records and maintains the agreements they close; archiving
			// one stays with manager/admin, like every other commercial record.
			grant{Create: true, Read: true, Update: true}),
		RowScope: principal.RowScopeTeam,
	},
	"read_only": {
		// A read-only role still owns its personal view state: saved views
		// are P1-exempt per-user prefs (runtime-config-surface.md §3), not
		// shared records, so full self-service is correct even here.
		Objects:  objects(readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, crud, readOnly, readOnly, readOnly, readOnly, readOnly, grant{}, readOnly, grant{}, grant{}, readOnly, readOnly, readOnly, grant{}, readOnly, readOnly, readOnly, grant{}, grant{}, grant{}, readOnly),
		RowScope: principal.RowScopeAll,
	},
	"ops": {
		Objects:  objects(crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, readOnly, crud, crud, crud, readUpdate, crud, writeNoDelete, writeNoDelete, writeNoDelete, crud, crud, crud, readUpdate, crud, crud, crud, readOnly, readOnly, crud),
		RowScope: principal.RowScopeAll,
	},
}

// objects zips grants onto coreObjects in declaration order — one line
// per role instead of twelve repeated map literals.
//
// The parameter list IS the mechanism, which is why it is this long: each role
// states its grants once, positionally, against the object order declared above,
// and rbacfixture_test.go holds the result to the matrix the server seeds. Five
// map literals of every key is what this replaced. Shortening it is a refactor of
// the seed rather than of this call.
func objects(person, organization, deal, lead, activity, pipeline, list, tag, relationship, partner, automation, voiceProfile, product, offer, signal, savedView, customField, computedField, quota, offerTemplate, overlayConnection, embeddingReindex, webhookSubscription, fxRate, aiModelRate, captureSettings, project, channelConnection, importRun, installationSettings, finance, integrations, retentionPolicy, captureTrace, license, contract grant) map[string]grant { // NOSONAR(go:S107) -- the parameter list is the mechanism; see above
	return map[string]grant{
		"person": person, "organization": organization, "deal": deal,
		"lead": lead, "activity": activity, "pipeline": pipeline,
		"list": list, "tag": tag, "relationship": relationship, "partner": partner,
		"automation": automation, "voice_profile": voiceProfile,
		"product": product, "offer": offer, "signal": signal,
		"saved_view": savedView, "custom_field": customField,
		"computed_field": computedField, "quota": quota,
		"offer_template": offerTemplate, "overlay_connection": overlayConnection,
		"embedding_reindex":    embeddingReindex,
		"webhook_subscription": webhookSubscription,
		"fx_rate":              fxRate,
		"ai_model_rate":        aiModelRate,
		"capture_settings":     captureSettings,
		"project":              project,
		"channel_connection":   channelConnection,
		"import_run":           importRun,
		// The workspace's SHARED-CHANNEL capture trace (a bot binding's inbound
		// traffic). Read-only for everyone who holds it, because nothing writes
		// it but the pipeline and nothing deletes it but its 24h sweep -- there
		// is no create/update/delete anyone could hold.
		//
		// It reaches ONLY rows with no member behind them. A member's own capture
		// traffic is personal data and no grant widens it, which is why rep and
		// read_only hold nothing here rather than holding read: there is no
		// shared-channel debugging in their job, and a grant that looked
		// harmless would be the one somebody later widened.
		"capture_trace": captureTrace,
		// The agreements an account has signed (ADR-0109/A160). Read is broad
		// because a contract is what makes an account's commercial state legible
		// and every role that opens a company page needs it; writing one stays
		// with the roles that own commercial records, and read_only holds read
		// alone. A contract carries no owner column — visibility is inherited
		// from its deal, falling back to its organization — so the row scope
		// that reaches it is the deal's, not one of its own.
		"contract": contract,
		// The installation's own identity and reporting basis (ADR-0090/A135).
		// Read is broad on purpose — a rep reading amounts benefits from knowing
		// which currency they are in — and only admin/ops change it.
		"installation_settings": installationSettings,
		// The ingested finance mirror (ADR-0083/A128). READ is broad — every
		// role that opens a company page sees whether the customer pays on
		// time — and connecting or disconnecting the accounting source is
		// destructive workspace-wide config, so create/update/delete are
		// admin/ops only, exactly like overlay_connection.
		//
		// No role holds a write on a finance RECORD, because there is no such
		// action: the mirror's read-only posture is the absence of the grant
		// (FIN-DDL-N-1), not a runtime refusal.
		"finance": finance,
		// The licensed data-provider connections (ADR-0101/A152). READ is
		// broad for the same reason finance's is: a rep looking at a person
		// record needs to know whether a provider is connected and why a
		// value is dated, and every state the card renders is a fact about
		// the installation rather than about a customer. Connecting one
		// spends the customer's money and sends their contacts' identifiers
		// to a third party, so the writes are admin/ops only.
		"integrations": integrations,
		// The installation's entitlement: what the license grants, and how much of
		// it is used. Admin/ops-only on read — import_run's and retention_policy's
		// posture rather than quota's, because a seat meter is commercial standing
		// and UC-ADMIN-03 F1 gives a rep their own seat and not the workspace's
		// entitlement.
		//
		// READ IS THE ONLY VERB, for admin as much as anyone: the token is resolved
		// from the deployment file at boot, so there is no write path for a grant to
		// govern. Any other verb here would describe an action this product has not
		// got.
		"license": license,
		// The storage-limitation ladder (UC-GDPR-09, GCS-WIRE-1..5). Admin/ops-only
		// on EVERY verb, READ INCLUDED — the import_run precedent, not quota's. A
		// retention policy decides what the installation destroys and when, and the
		// screen showing it is an admin surface: unlike a quota's attainment or an
		// overlay connection's status, no rep has a legitimate consumer for the read.
		// The retain-only posture setting is gated by this same object, so whoever may
		// author a policy may also suspend every destructive one.
		"retention_policy": retentionPolicy,
	}
}

// MustDefaultJSON returns the seeded policy document for a system role key,
// ready for the role.permissions column. Unknown keys panic: the caller
// iterates the compiled-in role list, so a miss is a programming error.
func MustDefaultJSON(roleKey string) []byte {
	doc, ok := defaults[roleKey]
	if !ok {
		panic(fmt.Sprintf("policy: no default document for role %q", roleKey))
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err) // a compiled-in document always marshals
	}
	return raw
}

// Parse reads one STORED role.permissions document.
//
// It rejects a malformed document and an invalid row_scope, and it DROPS an
// object outside the grantable vocabulary — logged, never fatal. The asymmetry
// is the point, and it was learned the hard way.
//
// A row_scope this code cannot read is a question with no safe answer: the
// value decides how far the grants below it reach, and neither guessing nor
// defaulting is honest. An unknown OBJECT is different — dropping it grants
// nothing, which is the strictest possible reading, and the only reading that
// cannot be exploited.
//
// Rejecting the whole document was the earlier behaviour, and it made the
// vocabulary — which is a property of the COMPILED-IN code plus whichever
// extensions this process happens to compose — a precondition for reading
// STORED DATA that outlives both. Removing an extension therefore did not
// degrade its screen: it took the whole installation's authentication down,
// because `crmauth` fails the login when any of the user's roles will not
// parse. Every user in a workspace whose role still carried
// `ext_<unit>_<object>` was locked out, with no endpoint and no migration to
// clear it — found by the Task 14 UAT, on the removal leg the tier's own
// guarantee requires an operator to perform.
//
// Data outlives the code that gave it meaning. That is not a special case for
// extensions; it is what a stored document IS.
//
// What is NOT given up: a typo'd object still grants nothing, so the
// motivating case for strictness — "a typo must not read as a bug in the role"
// — is answered by the log line rather than by refusing to authenticate. When
// a role-editing endpoint lands (`/roles` CRUD is deferred), that WRITE path is
// where an unknown object must be refused: refusing input a human just typed
// costs them a correction, while refusing stored data costs them their session.
func Parse(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, fmt.Errorf("policy: malformed permissions document: %w", err)
	}
	for object := range doc.Objects {
		// The GRANTABLE vocabulary, not the core one: a composed extension
		// registers its ext_<unit>_<object> names at boot (composable.go).
		if IsGrantableObject(object) {
			continue
		}
		// Deleted from the parsed document, so the grant cannot reach
		// principal.Permissions by any later path. Mutating the map while
		// ranging it is defined in Go for the entry being visited.
		delete(doc.Objects, object)
		slog.Default().Warn("policy: dropping a grant on an object this installation does not know",
			"object", object,
			"why", "the object is neither a core object nor one a composed extension registered at boot; "+
				"most likely its unit was removed, or the name is a typo. The grant is ignored — it "+
				"authorizes nothing — and the rest of the document still applies.")
	}
	switch doc.RowScope {
	case principal.RowScopeOwn, principal.RowScopeTeam, principal.RowScopeAll:
	case "":
		// An unset scope means the narrowest, never a silent widest.
		doc.RowScope = principal.RowScopeOwn
	default:
		return Document{}, fmt.Errorf("policy: invalid row_scope %q (want own|team|all)", doc.RowScope)
	}
	return doc, nil
}

// Merge resolves a user's assigned roles into the effective permission
// set: grants union (any role allowing an action allows it), row scope
// widens to the maximum any role holds. Zero roles yield zero grants.
func Merge(byRole map[string]Document) principal.Permissions {
	extensionObjects := RegisteredObjects()
	merged := principal.Permissions{
		Objects:  make(map[string]principal.ObjectGrant, len(coreObjects)+len(extensionObjects)),
		RowScope: principal.RowScopeOwn,
	}
	// Every registered extension object is SEEDED at the zero grant, before any
	// role document is read. The seeded core role documents list all thirty core
	// objects, so /me's snapshot has always been the complete vocabulary with
	// the holder's grants filled in — a client can tell "you hold nothing on
	// this" from "no such object". An extension object arrives after those
	// documents were seeded, so without this it would be absent from the
	// snapshot for every principal who was not explicitly granted it, and the
	// unit's screen could not tell the two apart. A union follows below: a role
	// that DOES grant the object widens the zero, never the reverse.
	for _, object := range extensionObjects {
		merged.Objects[object] = principal.ObjectGrant{}
	}
	for _, key := range slices.Sorted(maps.Keys(byRole)) {
		doc := byRole[key]
		merged.RoleKeys = append(merged.RoleKeys, key)
		for object, g := range doc.Objects {
			have := merged.Objects[object]
			merged.Objects[object] = principal.ObjectGrant{
				Create: have.Create || g.Create,
				Read:   have.Read || g.Read,
				Update: have.Update || g.Update,
				Delete: have.Delete || g.Delete,
			}
		}
		if doc.RowScope.Wider(merged.RowScope) {
			merged.RowScope = doc.RowScope
		}
	}
	return merged
}
