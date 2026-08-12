// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// Which HTTP method an operation may declare, and what follows from the one it
// picks.
//
// Split from verb.go, which owns the declaration as a whole. These rules belong
// together because they are one subject with one reason: the method is not
// decoration on this surface. It decides where a client puts the arguments, and
// it is what the human seat ceiling classifies a mutation BY — so a method that
// disagrees with the declared scope is not untidy, it is a read seat's route to a
// write.

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
)

// validateMethod admits the closed set of methods this seam can mount.
//
// GET and DELETE were absent until arguments could arrive without a body. The
// reason given for excluding them — "a route whose arguments never arrive" —
// was true of a seam that read the body and nothing else, and it cost the
// surface the one thing the method is FOR: a read declared POST is
// indistinguishable from a write to any intermediary, any generated client, and
// (the defect that prompted this) the seat ceiling, which refused a read seat
// from every extension read because every extension route was unsafe. Now a
// bodyless method carries its arguments in the query string, so the exclusion
// buys nothing and costs the invariant.
func validateMethod(method string) error {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return nil
	}
	return fmt.Errorf("method %q is not one an extension operation may declare (get, post, put, patch, delete)", method)
}

// CarriesBody reports whether an operation declared on this method takes its
// arguments from the request body. The false case takes them from the query
// string instead — see validateQueryEncodable for what a declaration must look
// like to be encodable that way, and compose/extroutes.go for the decode.
//
// Exported because the serving seam asks the same question this file's rules are
// written against, and two spellings of "does this method carry a body" could
// disagree — the disagreement being a route that reads a body nothing sent, or
// ignores arguments a client did send.
func CarriesBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// validateMethodAuthority holds the method and the requested scope to each
// other. It is the rule that makes the method authoritative again, and it is
// load-bearing rather than tidy: the human seat ceiling classifies a mutation by
// method (identity.serveAsHuman), so a mutating GET would be a read seat's route
// to a write, and admitting GET without this check would OPEN that hole rather
// than merely leave it theoretical.
//
// Both directions, because each is a different lie about the operation. A GET
// that mutates tells every cache, crawler, retry and prefetch that replaying it
// is free. A PUT, PATCH or DELETE that only reads tells a reader the opposite —
// that the call changes something — which is a declaration a reviewer trusts and
// a client guards against for no reason.
//
// POST is the one method with no rule, and that is the model rather than an
// omission: POST is the RPC method, the method that means only "here are some
// arguments, run this". Every other verb states a semantic the scope can
// contradict, so every other verb is held to it.
func (v Verb) validateMethodAuthority() error {
	if v.Method == http.MethodGet && v.RequestedScope != ScopeRead {
		return fmt.Errorf("operation %s declares GET but requests the %q scope — a safe method may not carry "+
			"mutating authority: the seat ceiling classifies a mutation by method, so this would admit a read "+
			"seat to a write. Declare it POST, or request the read scope",
			v.OperationID, string(v.RequestedScope))
	}
	switch v.Method {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		if v.RequestedScope == ScopeRead {
			return fmt.Errorf("operation %s declares %s but requests the read scope — a method that means "+
				"\"change this\" may not name an operation that changes nothing. Declare it GET when its "+
				"arguments are flat, POST otherwise",
				v.OperationID, v.Method)
		}
	}
	return nil
}

// validateQueryEncodable refuses an argument shape a bodyless method cannot
// honestly publish. A GET and a DELETE carry their arguments in the query
// string, and a query string is a flat list of name/value pairs where every
// value is text — so the declared schema has to be a flat object of primitives.
//
// Nested objects and arrays are refused rather than encoded. Every convention
// for putting them in a query (bracket notation, repeated keys, comma joins,
// JSON in a value) is a SECOND contract that the published schema does not
// describe, and a client generated from that schema would not produce it. An
// operation whose arguments have structure is a POST, which is the whole reason
// the body path still exists.
//
// The absent schema is fine: an operation taking no arguments is the common
// bodyless case (a list, a status probe), and it needs no query at all.
func validateQueryEncodable(method string, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var doc struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// The root shape is validateSchemaObject's rule; what this can add is that
		// `properties` and `required` are the shapes they have to be to be read —
		// a `required` that is a bare string rather than a list lands here too.
		return fmt.Errorf("InputSchema declares a `properties` or `required` this reader cannot walk, so a %s route "+
			"cannot be told which query parameters it takes", method)
	}
	// Sorted, because this returns on the FIRST structured argument and Go
	// randomises map order: a declaration with two of them would name a different
	// one on each generation, so an author comparing two runs sees the refusal move
	// rather than the fault. `required` below is a slice and already deterministic,
	// and querySchema sorts what it emits for the same reason.
	for _, name := range slices.Sorted(maps.Keys(doc.Properties)) {
		switch doc.Properties[name].Type {
		case "string", "number", "integer", "boolean":
			continue
		}
		return fmt.Errorf("operation declares %s but its argument %q is of type %q — a query string carries flat "+
			"text pairs, so a bodyless method's arguments must each be a string, number, integer or boolean. "+
			"An argument with structure belongs in a POST body",
			method, name, doc.Properties[name].Type)
	}
	// `required` must name declared arguments, and this check is HERE rather than
	// only at the serving seam so that Validate is genuinely the whole rule for a
	// bodyless declaration. It was not: compose's mount refused this shape while
	// Validate blessed it, which made the mount strictly stricter than the gate
	// that is documented as total — and because mounting panics on a refusal, a
	// declaration Validate accepted could still be a boot crash.
	for _, name := range doc.Required {
		if _, declared := doc.Properties[name]; !declared {
			return fmt.Errorf("operation declares %s and requires the argument %q, which its own properties do not "+
				"declare — no call could ever satisfy the route", method, name)
		}
	}
	return nil
}
