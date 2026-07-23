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
	"maps"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// coreObjects is the closed set of RBAC-governed object types
// (features/04 §1). A policy naming anything else is rejected — a typo'd
// object would otherwise silently grant nothing and read as a bug in the
// role, not the document.
var coreObjects = []string{"person", "organization", "deal", "lead", "activity", "pipeline", "list", "tag", "relationship", "partner", "automation", "voice_profile", "product", "offer", "signal", "saved_view", "custom_field", "computed_field", "quota", "offer_template", "overlay_connection", "embedding_reindex", "webhook_subscription", "fx_rate", "ai_model_rate"}

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
var defaults = map[string]Document{
	"admin": {
		Objects:  objects(crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, readOnly, crud, crud, crud, readUpdate, crud, writeNoDelete, writeNoDelete),
		RowScope: principal.RowScopeAll,
	},
	"manager": {
		Objects:  objects(crud, crud, crud, crud, crud, readOnly, crud, crud, crud, crud, readOnly, crud, crud, crud, crud, crud, readOnly, readOnly, readOnly, crud, readOnly, grant{}, readOnly, grant{}, grant{}),
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
			grant{},  // fx_rate — admin/ops-only
			grant{}), // ai_model_rate — admin/ops-only
		RowScope: principal.RowScopeTeam,
	},
	"read_only": {
		// A read-only role still owns its personal view state: saved views
		// are P1-exempt per-user prefs (runtime-config-surface.md §3), not
		// shared records, so full self-service is correct even here.
		Objects:  objects(readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, readOnly, crud, readOnly, readOnly, readOnly, readOnly, readOnly, grant{}, readOnly, grant{}, grant{}),
		RowScope: principal.RowScopeAll,
	},
	"ops": {
		Objects:  objects(crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, crud, readOnly, crud, crud, crud, readUpdate, crud, writeNoDelete, writeNoDelete),
		RowScope: principal.RowScopeAll,
	},
}

// objects zips grants onto coreObjects in declaration order — one line
// per role instead of twelve repeated map literals.
func objects(person, organization, deal, lead, activity, pipeline, list, tag, relationship, partner, automation, voiceProfile, product, offer, signal, savedView, customField, computedField, quota, offerTemplate, overlayConnection, embeddingReindex, webhookSubscription, fxRate, aiModelRate grant) map[string]grant {
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

// Parse validates one role.permissions document. It rejects unknown
// objects and invalid row_scope tokens rather than ignoring them
// (B-EP03.1 schema-validity requirement).
func Parse(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, fmt.Errorf("policy: malformed permissions document: %w", err)
	}
	for object := range doc.Objects {
		if !slices.Contains(coreObjects, object) {
			return Document{}, fmt.Errorf("policy: unknown object %q in permissions document", object)
		}
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
	merged := principal.Permissions{
		Objects:  make(map[string]principal.ObjectGrant, len(coreObjects)),
		RowScope: principal.RowScopeOwn,
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
