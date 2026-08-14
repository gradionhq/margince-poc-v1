// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// ingressUnitSource is a unit declaring the given Ingress slice elements.
func ingressUnitSource(entries string) string {
	return `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func New() extension.Extension {
	return extension.Extension{
		Name:    "x",
		Version: "0.1.0",
		Ingress: []extension.IngressSource{
` + entries + `
		},
	}
}
`
}

// TestIngressDerivesIntoManifest: the declared system becomes half of every
// landed record's provenance, so an operator must be able to read which
// providers a unit reaches core capture from without opening its source.
func TestIngressDerivesIntoManifest(t *testing.T) {
	src := ingressUnitSource(
		"\t\t\t{System: \"zulip\", Lands: []extension.RecordKind{extension.KindActivity}},\n" +
			"\t\t\t{System: \"dispact\", Lands: []extension.RecordKind{extension.KindActivity}},\n",
	)
	derived, err := deriveSynthetic(t, "x", src)
	if err != nil {
		t.Fatal(err)
	}
	s := string(derived)
	for _, want := range []string{`"system": "dispact"`, `"system": "zulip"`, `"activity"`} {
		if !strings.Contains(s, want) {
			t.Errorf("derived manifest misses %s:\n%s", want, s)
		}
	}
	if strings.Index(s, "dispact") > strings.Index(s, "zulip") {
		t.Errorf("ingress sources are not sorted by system:\n%s", s)
	}
}

// TestNoIngressOmitsTheField: every manifest already committed to the tree
// predates this field, so an unconditional key would rewrite all of them for
// something they do not declare.
func TestNoIngressOmitsTheField(t *testing.T) {
	derived, err := deriveSynthetic(t, "x", toolUnitSource("\t\t\tName: \"t\","),
		syntheticVerb("x", "t", "auto_execute", "read"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(derived), "ingress") {
		t.Fatalf("the ingress field must be omitted when nothing is declared:\n%s", derived)
	}
}

// TestDuplicateIngressSourceIsRejected: one system named twice is two
// declarations an operator resolves separately about a single provenance
// namespace, and which of them the port answers from would be declaration
// order.
func TestDuplicateIngressSourceIsRejected(t *testing.T) {
	src := ingressUnitSource(
		"\t\t\t{System: \"dispact\", Lands: []extension.RecordKind{extension.KindActivity}},\n" +
			"\t\t\t{System: \"dispact\", Lands: []extension.RecordKind{extension.KindActivity}},\n",
	)
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("err = %v, want the duplicate-source refusal", err)
	}
}

// TestIngressRulesAreThePublishedOnes pins that the generator refuses through
// extension.IngressSource.Validate rather than a second copy of it, so gen-time
// acceptance cannot diverge from boot-time.
func TestIngressRulesAreThePublishedOnes(t *testing.T) {
	for name, entry := range map[string]string{
		"an empty system key":                   "\t\t\t{System: \"\", Lands: []extension.RecordKind{extension.KindActivity}},\n",
		"a system key that is not a system key": "\t\t\t{System: \"Dispact Chat\", Lands: []extension.RecordKind{extension.KindActivity}},\n",
		"no record kinds at all":                "\t\t\t{System: \"dispact\", Lands: []extension.RecordKind{}},\n",
		"a record kind the core cannot land":    "\t\t\t{System: \"dispact\", Lands: []extension.RecordKind{\"lead\"}},\n",
	} {
		if _, err := deriveSynthetic(t, "x", ingressUnitSource(entry)); err == nil {
			t.Errorf("the generator accepted %s", name)
		}
	}
}

// TestIngressFieldMustBeASliceLiteral: a computed Ingress value cannot be
// derived without evaluating it, which a static reader must not do.
func TestIngressFieldMustBeASliceLiteral(t *testing.T) {
	src := `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func sources() []extension.IngressSource { return nil }

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0", Ingress: sources()}
}
`
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "Ingress must be a slice literal") {
		t.Fatalf("err = %v, want the non-literal-Ingress refusal", err)
	}
}

// TestIngressEntryUnrecognizedFieldFailsClosed mirrors every other declaration
// reader's default: a field this generator does not know could be a future part
// of the request, and omitting it would hide it.
func TestIngressEntryUnrecognizedFieldFailsClosed(t *testing.T) {
	src := ingressUnitSource(
		"\t\t\t{System: \"dispact\", Lands: []extension.RecordKind{extension.KindActivity}, Future: nil},\n",
	)
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "is not derivable by this generator") {
		t.Fatalf("err = %v, want the unrecognized-field refusal", err)
	}
}

// TestIngressEntryMustBeKeyed and the wrong-literal case mirror the two
// refusals every other entry reader in this generator applies.
func TestIngressEntryMustBeKeyed(t *testing.T) {
	src := ingressUnitSource("\t\t\t{\"dispact\", []extension.RecordKind{extension.KindActivity}},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "must be keyed") {
		t.Fatalf("err = %v, want the must-be-keyed refusal", err)
	}
}

func TestIngressEntryMustBeAnIngressSourceLiteral(t *testing.T) {
	src := ingressUnitSource("\t\t\textension.Tool{Name: \"t\"},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "must be an extension.IngressSource literal") {
		t.Fatalf("err = %v, want the wrong-literal-type refusal", err)
	}
}
