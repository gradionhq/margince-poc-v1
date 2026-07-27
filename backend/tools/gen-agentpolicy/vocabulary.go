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
// names to their declared enum.
type schemaNode struct {
	Properties map[string]struct {
		Enum []string `yaml:"enum"`
	} `yaml:"properties"`
}

// vocabularySchema names the schema whose properties declare the vocabularies,
// and toolSchema the one whose `tier` must agree with it — the same tier
// vocabulary is declared twice (once for the wire type the tools listing
// returns, once here), so the generator holds them equal rather than trusting
// two hand-written lists to stay in step.
const (
	vocabularySchema = "AgentAdmissionPolicy"
	toolSchema       = "AgentTool"
)

// vocabulary is one closed set per annotation field, in declaration order.
type vocabulary struct {
	access     []string
	recordType []string
	tier       []string
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
		"access": &v.access, "record_type": &v.recordType, "tier": &v.tier,
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
