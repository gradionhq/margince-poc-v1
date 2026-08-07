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
// missing `q`, `kind` or `segment` reached whichever handler happened to check
// for it — and the reason one spelling at one chokepoint wins is the reason
// idargs.go already gives: thirteen handlers each failed to make their own
// schema's `required` true, one at a time.
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

// declaredRequired reads a schema's `required` list once, at registration,
// keeping only the names the schema also DECLARES as properties.
//
// The intersection matters. A `required` entry naming an undeclared property is
// a schema defect, and enforcing it would refuse every call to that tool for a
// field no caller could ever learn about — a refusal with no way out. The gate
// that catches the defect itself is a fitness test over the registry, where it
// can name the tool at boot instead of at a caller.
//
// The id-shaped arguments are left to idargs.go. Both would otherwise refuse the
// same missing argument, and the caller would be told about it twice, in two
// different sentences.
func declaredRequired(inputSchema json.RawMessage) []string {
	var schema struct {
		Required   []string `json:"required"`
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
	names := make([]string, 0, len(schema.Required))
	for _, name := range schema.Required {
		prop, declared := schema.Properties[name]
		if !declared || (prop.Type == "string" && prop.Format == "uuid") {
			continue
		}
		names = append(names, name)
	}
	// Sorted, so a call missing two arguments is refused in the same words every
	// time rather than in the order the schema happened to list them.
	sort.Strings(names)
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
		Guidance: fmt.Sprintf("supply %s — the schema tools/list advertises lists %s as required",
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
