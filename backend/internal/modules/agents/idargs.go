// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The claims a tool's schema makes about its uuid arguments, and their
// enforcement — split out of registry.go when that file hit the 500-line cap. The
// boundary is real: registry.go decides WHETHER a call may run (registration,
// admission, staging, redemption), and this decides whether its ids name anything.
//
// It lives beside the registry rather than in a handler because that is the whole
// point. `ids.UUID` refuses a malformed value inside decodeArgs but zero-values an
// ABSENT key without erroring, so a handler receives a well-formed id that names
// nothing, reaches a store lookup, matches no row — and the caller is told a record
// it never mentioned does not exist. Thirteen handlers each failed to make their
// own schema's "required" true, which is what one spelling at one chokepoint is for.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// uuidFormat is the JSON Schema `format` an id argument declares. Named because
// the derivation below matches it at three levels of the schema, and a typo in one
// of them would silently leave that level unguarded.
const uuidFormat = "uuid"

// idArgSpec is what a tool's schema says about its uuid arguments: every one it
// declares, which of them it declares required, and the same for the uuids
// declared inside an array property's items.
type idArgSpec struct {
	all      []string
	required map[string]bool
	// itemRequired[property] is the uuid members an ITEM of that array must carry.
	// An absent array is a legal call, so these bind only to items that are
	// present — but a present item owes its own `required`, and an unenforced one
	// sends a zero uuid to a link-target check that answers a bare not-found for a
	// record the caller never named.
	itemRequired map[string][]string
}

// declaredIDArgs reads a schema's uuid arguments once, at registration.
//
// Only top-level properties. A uuid nested in an array item is required GIVEN its
// parent, and an absent parent is a legal call; enforcing it here would refuse
// `log_activity` with no links.
func declaredIDArgs(inputSchema json.RawMessage) idArgSpec {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Format string `json:"format"`
			Items  struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Format string `json:"format"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	// assertObjectSchemas has already confirmed this is valid JSON declaring an
	// object, but it decodes only `type` — a schema whose `required` is not an
	// array, or whose `properties` is not a map, gets here and fails. That is a
	// schema defect in whatever registered it (an extension tool, most likely), so
	// it is named as one: this runs while cmd wiring boots, never on a request.
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: input schema declares an unreadable `required`/`properties`: " + err.Error())
	}
	spec := idArgSpec{required: map[string]bool{}, itemRequired: map[string][]string{}}
	for name, prop := range schema.Properties {
		if prop.Format == uuidFormat {
			spec.all = append(spec.all, name)
		}
		var itemIDs []string
		for _, member := range prop.Items.Required {
			if prop.Items.Properties[member].Format == uuidFormat {
				itemIDs = append(itemIDs, member)
			}
		}
		if len(itemIDs) > 0 {
			sort.Strings(itemIDs)
			spec.itemRequired[name] = itemIDs
		}
	}
	sort.Strings(spec.all)
	for _, name := range schema.Required {
		if schema.Properties[name].Format == uuidFormat {
			spec.required[name] = true
		}
	}
	return spec
}

// requireDeclaredIDs holds every claim a tool's schema makes about its uuid
// arguments: a required one is present, and any one supplied is a real UUID.
//
// Surface-wide with one spelling rather than a per-handler habit, because the
// failure it prevents is invisible at the handler. `ids.UUID` refuses a malformed
// value inside decodeArgs but zero-values an ABSENT key without any error, so the
// handler receives a well-formed id that names nothing, reaches a store lookup,
// matches no row — and the caller is told a record it never mentioned does not
// exist. One spelling for the whole surface, because a per-handler habit is a
// habit every new handler can skip.
//
// It also names WHICH id is malformed, which the handler cannot: encoding/json
// discards the field path when a value's own UnmarshalText fails, so decodeArgs
// can only report `ids: "x" is not a canonical UUID`. On a tool taking one id
// that is merely terse; on `merge_records` or `advance_deal` it does not say
// which of two ids to fix.
//
// Every missing required id is collected before answering. Reporting them one per
// round trip is accurate and still wasteful — an agent spends a call per field to
// learn what one refusal could have told it.
// The lookup key is the REGISTRY key the caller resolved the tool by, not
// spec.Name. They are equal today; keying on the spec would make a tool whose
// Spec() returned a different name silently unguarded, which is the wrong way for
// a surface-wide check to fail.
func (r *Registry) requireDeclaredIDs(name string, args json.RawMessage) error {
	r.mu.RLock()
	spec := r.idArgs[name]
	r.mu.RUnlock()
	if len(spec.all) == 0 && len(spec.itemRequired) == 0 {
		return nil
	}
	present, isObject := argsAsObject(args)
	if !isObject {
		// Not an object at all, so there are no members to check. decodeArgs
		// refuses the shape in the handler's own terms, which is the better
		// message — this check has nothing to add to it.
		return nil
	}
	var missing []string
	for _, field := range spec.all {
		raw, supplied := present[field]
		if !supplied {
			if spec.required[field] {
				missing = append(missing, "`"+field+"`")
			}
			continue
		}
		var id ids.UUID
		if err := json.Unmarshal(raw, &id); err != nil {
			return &BadArgsError{Cause: fmt.Errorf("`%s` is not a canonical UUID", field)}
		}
		if spec.required[field] && id.IsZero() {
			// An explicit null or all-zero uuid names no record any more than an
			// absent key does, so it joins them rather than travelling onward.
			missing = append(missing, "`"+field+"`")
		}
	}
	if len(missing) > 0 {
		return &BadArgsError{Cause: fmt.Errorf("%s %s required",
			strings.Join(missing, ", "), plural(len(missing), "is", "are"))}
	}
	return requireItemIDs(spec, present)
}

// requireItemIDs holds each present array item to its own `required` uuids.
// `log_activity` with links:[{"entity_type":"deal"}] used to send the zero uuid
// to the link-target check, which answers a bare not-found — the D1 symptom one
// level down from the top-level arguments.
func requireItemIDs(spec idArgSpec, present map[string]json.RawMessage) error {
	for property, members := range spec.itemRequired {
		raw, supplied := present[property]
		if !supplied {
			continue
		}
		items, isArray := itemsAsObjects(raw)
		if !isArray {
			// Not an array of objects, so it has no items to hold to anything.
			// decodeArgs refuses the shape with the better message.
			continue
		}
		for i, item := range items {
			for _, member := range members {
				var id ids.UUID
				value, has := item[member]
				if has && json.Unmarshal(value, &id) != nil {
					return &BadArgsError{Cause: fmt.Errorf("`%s[%d].%s` is not a canonical UUID", property, i, member)}
				}
				if id.IsZero() {
					return &BadArgsError{Cause: fmt.Errorf("`%s[%d].%s` is required", property, i, member)}
				}
			}
		}
	}
	return nil
}

// plural picks the verb form for a list of n items, so a refusal naming two
// fields does not read as though it named one.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// argsAsObject decodes a tool call's arguments as a member map, reporting whether
// they were an object at all. The shape verdict belongs to decodeArgs; this only
// needs to know whether there are members to inspect.
func argsAsObject(args json.RawMessage) (map[string]json.RawMessage, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(args, &members); err != nil {
		return nil, false
	}
	return members, true
}

// itemsAsObjects decodes an array property as a list of member maps, reporting
// whether it was an array of objects at all.
func itemsAsObjects(raw json.RawMessage) ([]map[string]json.RawMessage, bool) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	return items, true
}
