// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The claim a tool's schema makes about which arguments a call MUST carry, and
// its enforcement — the third sibling of idargs.go and numargs.go, at the same
// chokepoint and for the same reason.
//
// `required` is the first thing a client reads to build a call, and it was the
// last of the schema's claims with nothing behind it. The uuid check held
// presence for id-shaped arguments and the numeric check held ranges, so a
// missing `kind`, `segment` or `record_type` reached whichever handler happened
// to check for it — and the reason one spelling at one chokepoint wins is the reason
// idargs.go already gives: thirteen handlers each failed to make their own
// schema's `required` true, one at a time.
//
// SCOPE: top-level properties, like numargs.go's bounds and idargs.go's own
// presence rule. A `required` inside an array's `items` — `links[].entity_type`,
// `aggregates[].fn` — is a claim about a member GIVEN its parent, and the parent
// is optional on some of those tools, so it cannot be held here without
// inventing a rule about when the parent counts. Those stay where they already
// are: the provider re-validates an activity's links, and the report engine
// refuses an aggregate it cannot read. Reaching them from here is a real
// improvement and a larger one.
//
// WHAT THIS DOES NOT TAKE FROM A HANDLER. Presence is not the same claim as
// meaning. A blank string is present; an enum value outside its list is
// present; a body that is only whitespace is present. Those stay with the
// handler that knows what they mean — this holds the one claim the schema
// itself makes, and holds it identically for every tool.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// declaredRequired reads a schema's `required` list once, at registration.
//
// A `required` entry naming an UNDECLARED property is refused here rather than
// dropped. Dropping it would leave the surface advertising a requirement the
// registry ignores, so a caller and a handler would be working from different
// contracts with nothing saying so; enforcing it would refuse every call to
// that tool for a field no caller could ever learn about. Neither is a state to
// serve, and the schema is a literal in whatever registered the tool — so this
// fails the BOOT, which is the only moment it can still be fixed, and covers a
// composed extension unit exactly as it covers a core tool.
//
// This rests on an assumption worth stating where it binds: every schema this
// surface serves is a FLAT literal listing its own properties. JSON Schema also
// lets `required` name a property supplied by `additionalProperties`,
// `patternProperties` or an `allOf` branch, none of which this reader walks — so
// a unit shipping a composed schema would be refused here for a shape that is
// legitimate. Nothing on this surface writes one today, and assertObjectSchemas
// does not yet forbid it; the day a unit needs one, this reader is what has to
// learn about it rather than the unit having to flatten.
//
// The id-shaped arguments are left to idargs.go. Both would otherwise refuse the
// same missing argument, and the caller would be told about it twice, in two
// different sentences.
func declaredRequired(inputSchema json.RawMessage) []string {
	var schema struct {
		// RawMessage rather than []string, because the two failures a schema can
		// have here are different and only one of them is a decode error: a
		// `required` that is a number or an object fails to decode, and a
		// `required: null` decodes into a nil slice indistinguishable from an
		// absent one. Both are invalid JSON Schema, and a reader claiming to
		// refuse "anything that is not a list of strings" has to refuse both.
		Required   json.RawMessage `json:"required"`
		Properties map[string]struct {
			Type   string `json:"type"`
			Format string `json:"format"`
		} `json:"properties"`
	}
	// assertObjectSchemas has already confirmed this is valid JSON declaring an
	// object, but it decodes only `type`. A schema whose `required` is not a
	// list of strings gets here and fails — a defect in whatever registered it,
	// named while cmd wiring boots rather than on a request.
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: input schema declares an unreadable `required`: " + err.Error())
	}
	required := readRequiredList(schema.Required)
	names := make([]string, 0, len(required))
	for _, name := range required {
		prop, declared := schema.Properties[name]
		if !declared {
			//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
			panic(fmt.Sprintf("crmagents: input schema requires %q, which its own properties never declare — "+
				"a caller reading tools/list has no way to learn the argument exists", name))
		}
		if prop.Type == "string" && prop.Format == "uuid" {
			continue
		}
		names = append(names, name)
	}
	// Sorted, so a call missing two arguments is refused in the same words every
	// time rather than in the order the schema happened to list them.
	sort.Strings(names)
	return names
}

// readRequiredList decodes the `required` keyword, refusing every spelling that
// is not what JSON Schema defines it as. Absent is legal and means nothing is
// required; `null` is NOT absent — it is a present member holding the wrong
// thing, and accepting it would serve a schema this reader claims to have
// checked.
func readRequiredList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// Element by element, because []string hides two things: a null element
	// decodes to "" without error, which would go on to be reported as a
	// requirement with no name, and a repeated name would be reported twice in
	// one refusal. JSON Schema says the list holds unique strings.
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil || elements == nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: input schema's `required` is not a list of strings: " + string(raw))
	}
	names := make([]string, 0, len(elements))
	seen := make(map[string]bool, len(elements))
	for _, element := range elements {
		var name string
		if err := json.Unmarshal(element, &name); err != nil || name == "" {
			//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
			panic("crmagents: input schema's `required` holds " + string(element) + ", which names no property")
		}
		if seen[name] {
			//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
			panic("crmagents: input schema's `required` names " + name + " twice; JSON Schema's list holds unique names, and a repeat would be reported twice in one refusal")
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// requireDeclaredPresence holds a call to the arguments its tool says it cannot
// run without.
//
// An explicit null counts as ABSENT. A caller that spells out `{"q": null}` has
// supplied no query, and answering "present" there would hand the handler a
// zero value it would have to re-refuse in its own words — which is the
// arrangement this replaces.
//
// Every missing argument is named before answering, for the reason the id check
// gives: reporting them one per round trip is accurate and still wasteful, since
// an agent then spends a call per field to learn what one refusal could have
// told it.
func (r *Registry) requireDeclaredPresence(name string, args json.RawMessage) error {
	r.mu.RLock()
	required := r.requiredArgs[name]
	r.mu.RUnlock()
	if len(required) == 0 {
		return nil
	}
	present, isObject := argsAsObject(args)
	if !isObject {
		// Not an object at all, so there are no members to look for. The shape
		// verdict belongs to the steps that own it — the argument split, then the
		// handler's own decode — each of which names what it wanted; a second,
		// vaguer answer to the same question is worse than none.
		return nil
	}
	var missing []string
	for _, argument := range required {
		raw, supplied := present[argument]
		if !supplied || isJSONNull(raw) {
			missing = append(missing, "`"+argument+"`")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &BadArgsError{
		Cause: errors.New(refusalSubject(missing) + " required by this tool's input schema"),
		Guidance: fmt.Sprintf("supply %s — the schema advertised on tools/list names %s as required",
			refusalNoun(missing), strings.Join(missing, ", ")),
	}
}

// refusalSubject renders the missing arguments as the subject of a sentence
// that agrees with itself: one argument "is", several "are".
func refusalSubject(missing []string) string {
	if len(missing) == 1 {
		return missing[0] + " is missing and is"
	}
	return strings.Join(missing, ", ") + " are missing and are"
}

// refusalNoun names what to supply, in the same number.
func refusalNoun(missing []string) string {
	if len(missing) == 1 {
		return "it"
	}
	return "all of them"
}

// isJSONNull reports whether an argument arrived as the literal null — supplied
// in the sense that the key is there, and absent in the sense that matters.
func isJSONNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}
