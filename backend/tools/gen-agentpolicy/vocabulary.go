// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The admission vocabularies, read from the contract rather than restated here.
//
// `components.schemas.AgentAdmissionPolicy` declares the closed sets an
// `x-agent-access` / `x-mcp-tool` annotation may draw from. That schema describes
// no response body and the OpenAPI generator prunes it — its consumer is this
// generator, which reads the enums straight out of the file and does two things
// with them: refuses to generate a table containing a value outside them, and
// emits a Go TYPE per vocabulary so the admission gate compares constants instead
// of string literals.
//
// The payoff is where extensions land. A new operation whose annotation says
// `human_only` instead of `human-only` fails the build with the offending
// operationId, rather than becoming an unrecognized string the gate reads as "not
// a tool" — which, for an access class, silently means REFUSED, and for a tier
// silently means the strictest branch never matches.

import (
	"fmt"
	"sort"
	"strings"
)

// schemaNode is the slice of a component schema this generator reads: property
// names to their declared enum, or — for an array property — its items' enum.
type schemaNode struct {
	Properties map[string]struct {
		Enum  []string `yaml:"enum"`
		Items struct {
			Enum []string `yaml:"enum"`
		} `yaml:"items"`
	} `yaml:"properties"`
}

// vocabularySchema names the schema whose properties declare the vocabularies;
// toolSchema the one whose `tier` must agree with it, and passportSchema the one
// whose grantable `scopes` must. Each of those vocabularies is declared twice in
// the contract — once for a wire type, once for the annotations — so the
// generator holds them equal rather than trusting hand-written lists to stay in
// step. The scope pairing is the load-bearing one: a cap an operation demands
// that no passport can be granted would refuse every call to it, fail-closed
// and silently, which reads exactly like the endpoint being broken.
const (
	vocabularySchema = "AgentAdmissionPolicy"
	toolSchema       = "AgentTool"
	passportSchema   = "IssuePassportRequest"
)

// vocabulary is one closed set per annotation field, in declaration order.
type vocabulary struct {
	access     []string
	recordType []string
	tier       []string
	scope      []string
}

func readVocabulary(schemas map[string]schemaNode) (vocabulary, error) {
	declared, ok := schemas[vocabularySchema]
	if !ok {
		return vocabulary{}, fmt.Errorf(
			"components.schemas.%s is missing: the admission vocabularies are declared there, and without it "+
				"an annotation typo cannot be caught at build time", vocabularySchema)
	}
	var v vocabulary
	for field, target := range map[string]*[]string{
		"access": &v.access, "record_type": &v.recordType, "tier": &v.tier, "scope": &v.scope,
	} {
		prop, ok := declared.Properties[field]
		if !ok || len(prop.Enum) == 0 {
			return vocabulary{}, fmt.Errorf(
				"components.schemas.%s.properties.%s declares no enum — the vocabulary must be closed to be checkable",
				vocabularySchema, field)
		}
		*target = prop.Enum
	}
	// The tools listing returns the same tier vocabulary on the wire. Two
	// declarations of one vocabulary drift; hold them equal here so the drift is
	// a build failure rather than a wire/gate disagreement.
	if tool, ok := schemas[toolSchema]; ok {
		if wire := tool.Properties["tier"].Enum; len(wire) > 0 && !sameSet(wire, v.tier) {
			return vocabulary{}, fmt.Errorf(
				"%s.tier declares %v but %s.tier declares %v — one tier vocabulary, two spellings",
				toolSchema, wire, vocabularySchema, v.tier)
		}
	}
	// The caps an operation may DEMAND and the caps a passport may be GRANTED
	// have to be the same set. A scope in one and not the other is not a typo
	// the gate can survive: an operation demanding an ungrantable cap refuses
	// every caller, and a grantable cap no operation names is authority nobody
	// can spend.
	if pass, ok := schemas[passportSchema]; ok {
		if grantable := pass.Properties["scopes"].Items.Enum; len(grantable) > 0 && !sameSet(grantable, v.scope) {
			return vocabulary{}, fmt.Errorf(
				"%s.scopes grants %v but %s.scope demands %v — a cap an operation requires must be one a passport can hold",
				passportSchema, grantable, vocabularySchema, v.scope)
		}
	}
	return v, nil
}

// violations reports every derived policy carrying a value the contract does not
// declare. Empty is always allowed and always distinct from invalid: an
// operation with no tier or no record type declares none, which the emitted zero
// value represents.
func (v vocabulary) violations(policies []policy) []string {
	var defects []string
	for _, p := range policies {
		for _, check := range []struct {
			field, value string
			allowed      []string
		}{
			{"x-agent-access", p.Access, v.access},
			{"x-mcp-tool.record_type", p.RecordType, v.recordType},
			{"x-mcp-tool.tier", p.Tier, v.tier},
			{"x-mcp-tool.scope", p.Scope, v.scope},
		} {
			if check.value == "" || contains(check.allowed, check.value) {
				continue
			}
			defects = append(defects, fmt.Sprintf(
				"%s (%s): %s = %q is not one of %v — add it to components.schemas.%s if it is real, or fix the typo",
				p.Route, p.Op, check.field, check.value, check.allowed, vocabularySchema))
		}
	}
	return defects
}

// renderVocabulary emits one Go type + constant block per vocabulary. The
// constant names are derived from the values, so a value added to the contract
// arrives as a usable constant with no second edit.
func renderVocabulary(v vocabulary) string {
	var b strings.Builder
	for _, block := range []struct {
		typeName, prefix, doc string
		values                []string
	}{
		{"agentAccess", "access",
			"agentAccess is an operation's admission class for an AGENT principal.", v.access},
		{"agentTier", "tier",
			"agentTier is the autonomy tier the gate admits against, identical on REST and MCP.", v.tier},
		{"agentRecordType", "recordType",
			"agentRecordType is the record an operation targets; the zero value means it declares none.", v.recordType},
		{"agentScope", "scope",
			"agentScope is the passport cap an operation consumes. Unlike the others it has no zero\n// value in practice: every tool operation declares one, so the gate never has to invent it.", v.scope},
	} {
		fmt.Fprintf(&b, "\n// %s\n//\n// Values are the closed set declared by components.schemas.%s in\n"+
			"// api/crm.yaml; a value outside it fails generation.\ntype %s string\n\nconst (\n",
			block.doc, vocabularySchema, block.typeName)
		for _, value := range block.values {
			fmt.Fprintf(&b, "\t%s%s %s = %q\n", block.prefix, goIdent(value), block.typeName, value)
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// goIdent turns a contract value into an exported-style identifier fragment:
// "human-only" → "HumanOnly", "data_subject_request" → "DataSubjectRequest".
func goIdent(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' })
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

func contains(set []string, value string) bool {
	for _, s := range set {
		if s == value {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
