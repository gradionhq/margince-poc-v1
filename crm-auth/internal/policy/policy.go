// Package policy owns the role permission-policy documents (B-EP03.1,
// data-model §2.4): the JSONB shape stored in role.permissions, the five
// seeded system-role defaults (decisions/0006), the validator that keeps
// a policy honest, and the merge that resolves a user's role set into
// one effective crmctx.Permissions at authentication time.
package policy

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/gradionhq/margince/backend/crmctx"
)

// coreObjects is the closed set of RBAC-governed object types
// (features/04 §1). A policy naming anything else is rejected — a typo'd
// object would otherwise silently grant nothing and read as a bug in the
// role, not the document.
var coreObjects = []string{"person", "organization", "deal", "lead", "activity", "pipeline"}

// Document is the role.permissions JSONB shape:
// {"objects": {"<object>": {"create":…,"read":…,"update":…,"delete":…}},
//
//	"row_scope": "own"|"team"|"all", "field_masks": […]}.
type Document struct {
	Objects  map[string]grant `json:"objects"`
	RowScope crmctx.RowScope  `json:"row_scope"`
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
	crud     = grant{Create: true, Read: true, Update: true, Delete: true}
	readOnly = grant{Read: true}
)

// defaults are the seeded system-role policies (decisions/0006 records
// the choices: reps work team-scoped without delete; managers are
// team-scoped with delete; pipeline config is admin/ops-owned).
var defaults = map[string]Document{
	"admin": {
		Objects:  objects(crud, crud, crud, crud, crud, crud),
		RowScope: crmctx.RowScopeAll,
	},
	"manager": {
		Objects:  objects(crud, crud, crud, crud, crud, readOnly),
		RowScope: crmctx.RowScopeTeam,
	},
	"rep": {
		// Reps create and work records but never delete them — except
		// leads, where disqualify IS the delete and is routine rep work.
		Objects: objects(
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			grant{Create: true, Read: true, Update: true},
			crud,
			grant{Create: true, Read: true, Update: true},
			readOnly),
		RowScope: crmctx.RowScopeTeam,
	},
	"read_only": {
		Objects:  objects(readOnly, readOnly, readOnly, readOnly, readOnly, readOnly),
		RowScope: crmctx.RowScopeAll,
	},
	"ops": {
		Objects:  objects(crud, crud, crud, crud, crud, crud),
		RowScope: crmctx.RowScopeAll,
	},
}

// objects zips grants onto coreObjects in declaration order — one line
// per role instead of six repeated map literals.
func objects(person, organization, deal, lead, activity, pipeline grant) map[string]grant {
	return map[string]grant{
		"person": person, "organization": organization, "deal": deal,
		"lead": lead, "activity": activity, "pipeline": pipeline,
	}
}

// DefaultJSON returns the seeded policy document for a system role key,
// ready for the role.permissions column. Unknown keys panic: the caller
// iterates the compiled-in role list, so a miss is a programming error.
func DefaultJSON(roleKey string) []byte {
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
	case crmctx.RowScopeOwn, crmctx.RowScopeTeam, crmctx.RowScopeAll:
	case "":
		// An unset scope means the narrowest, never a silent widest.
		doc.RowScope = crmctx.RowScopeOwn
	default:
		return Document{}, fmt.Errorf("policy: invalid row_scope %q (want own|team|all)", doc.RowScope)
	}
	return doc, nil
}

// Merge resolves a user's assigned roles into the effective permission
// set: grants union (any role allowing an action allows it), row scope
// widens to the maximum any role holds. Zero roles yield zero grants.
func Merge(byRole map[string]Document) crmctx.Permissions {
	merged := crmctx.Permissions{
		Objects:  make(map[string]crmctx.ObjectGrant, len(coreObjects)),
		RowScope: crmctx.RowScopeOwn,
	}
	for _, key := range slices.Sorted(maps.Keys(byRole)) {
		doc := byRole[key]
		merged.RoleKeys = append(merged.RoleKeys, key)
		for object, g := range doc.Objects {
			have := merged.Objects[object]
			merged.Objects[object] = crmctx.ObjectGrant{
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
