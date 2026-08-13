// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// subscriptionsUnitSource is a unit declaring the given Subscriptions slice
// elements.
func subscriptionsUnitSource(entries string) string {
	return `package x

import (
	"context"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func react(context.Context, extension.Runtime, extension.Delivery) error { return nil }

func New() extension.Extension {
	return extension.Extension{
		Name:    "x",
		Version: "0.1.0",
		Subscriptions: []extension.Subscription{
` + entries + `
		},
	}
}
`
}

// TestSubscriptionsDeriveIntoManifest: what an operator needs from a listener
// is its REACH — which of the installation's facts the unit consumes — so the
// name and the event types derive, sorted, and the handler does not.
func TestSubscriptionsDeriveIntoManifest(t *testing.T) {
	src := subscriptionsUnitSource(
		"\t\t\t{Name: \"withdraw_filing\", Events: []string{\"person.archived\", \"activity.archived\"}, Handle: react},\n" +
			"\t\t\t{Name: \"another_listener\", Events: []string{\"deal.created\"}, Handle: react},\n")
	derived, err := deriveSynthetic(t, "x", src)
	if err != nil {
		t.Fatal(err)
	}
	s := string(derived)
	for _, want := range []string{
		`"name": "withdraw_filing"`, `"activity.archived"`, `"person.archived"`,
		`"name": "another_listener"`, `"deal.created"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("derived manifest misses %s:\n%s", want, s)
		}
	}
	if strings.Index(s, "another_listener") > strings.Index(s, "withdraw_filing") {
		t.Errorf("subscriptions are not sorted by name:\n%s", s)
	}
	if strings.Index(s, "activity.archived") > strings.Index(s, "person.archived") {
		t.Errorf("event types are not sorted:\n%s", s)
	}
	if strings.Contains(s, "react") || strings.Contains(s, "Handle") {
		t.Errorf("the handler reached the manifest, which is read without compiling the unit:\n%s", s)
	}
}

// TestNoSubscriptionsOmitsTheField: every manifest already committed to the
// tree predates this field, so an unconditional key would rewrite all of them
// for something they do not declare.
func TestNoSubscriptionsOmitsTheField(t *testing.T) {
	derived, err := deriveSynthetic(t, "x", toolUnitSource("\t\t\tName: \"t\","),
		syntheticVerb("x", "t", "auto_execute", "read"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(derived), "subscription") {
		t.Fatalf("the subscriptions field must be omitted when nothing is declared:\n%s", derived)
	}
}

// TestDuplicateSubscriptionIsRejected: two listeners of one name would key one
// consumer group twice, and the manifest could only show one of them.
func TestDuplicateSubscriptionIsRejected(t *testing.T) {
	src := subscriptionsUnitSource(
		"\t\t\t{Name: \"withdraw_filing\", Events: []string{\"activity.archived\"}, Handle: react},\n" +
			"\t\t\t{Name: \"withdraw_filing\", Events: []string{\"person.archived\"}, Handle: react},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("err = %v, want the duplicate-subscription refusal", err)
	}
}

// TestSubscriptionRulesAreThePublishedOnes pins that the generator refuses
// through extension.Subscription's own declaration rules rather than a second
// copy of them — gen-time acceptance cannot diverge from boot-time.
func TestSubscriptionRulesAreThePublishedOnes(t *testing.T) {
	for name, entry := range map[string]string{
		"a name that is not lower snake_case": "\t\t\t{Name: \"WithdrawFiling\", Events: []string{\"activity.archived\"}, Handle: react},\n",
		"no events at all":                    "\t\t\t{Name: \"withdraw_filing\", Events: []string{}, Handle: react},\n",
		"the same event twice":                "\t\t\t{Name: \"withdraw_filing\", Events: []string{\"activity.archived\", \"activity.archived\"}, Handle: react},\n",
	} {
		if _, err := deriveSynthetic(t, "x", subscriptionsUnitSource(entry)); err == nil {
			t.Errorf("the generator accepted %s", name)
		}
	}
}

// TestSubscriptionsFieldMustBeASliceLiteral: a computed Subscriptions value
// cannot be derived without evaluating it, which a static reader must not do.
func TestSubscriptionsFieldMustBeASliceLiteral(t *testing.T) {
	src := `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func subs() []extension.Subscription { return nil }

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0", Subscriptions: subs()}
}
`
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "Subscriptions must be a slice literal") {
		t.Fatalf("err = %v, want the non-literal-Subscriptions refusal", err)
	}
}

// TestSubscriptionEventsMustBeStringLiterals: an event type resolved from a
// constant or a call is one the manifest reader cannot see, and a listener
// whose reach is invisible is the thing this field exists to prevent.
func TestSubscriptionEventsMustBeStringLiterals(t *testing.T) {
	src := subscriptionsUnitSource(
		"\t\t\t{Name: \"withdraw_filing\", Events: []string{archivedEvent}, Handle: react},\n") +
		"\nconst archivedEvent = \"activity.archived\"\n"
	_, err := deriveSynthetic(t, "x", src)
	if err == nil {
		t.Fatal("the generator derived an event type it could not read as a literal")
	}
}

// TestSubscriptionEntryUnrecognizedFieldFailsClosed mirrors every other
// declaration reader's default: a field this generator does not know could be
// a future part of the request, and omitting it would hide it.
func TestSubscriptionEntryUnrecognizedFieldFailsClosed(t *testing.T) {
	src := subscriptionsUnitSource(
		"\t\t\t{Name: \"withdraw_filing\", Events: []string{\"activity.archived\"}, Handle: react, Future: nil},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "is not derivable by this generator") {
		t.Fatalf("err = %v, want the unrecognized-field refusal", err)
	}
}

// TestSubscriptionEntryMustBeKeyed and the wrong-literal case mirror the same
// two refusals every other entry reader in this generator applies.
func TestSubscriptionEntryMustBeKeyed(t *testing.T) {
	src := subscriptionsUnitSource("\t\t\t{\"withdraw_filing\", []string{\"activity.archived\"}, react},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "must be keyed") {
		t.Fatalf("err = %v, want the must-be-keyed refusal", err)
	}
}

func TestSubscriptionEntryMustBeASubscriptionLiteral(t *testing.T) {
	src := subscriptionsUnitSource("\t\t\textension.Tool{Name: \"t\"},\n")
	_, err := deriveSynthetic(t, "x", src)
	if err == nil || !strings.Contains(err.Error(), "must be an extension.Subscription literal") {
		t.Fatalf("err = %v, want the wrong-literal-type refusal", err)
	}
}
