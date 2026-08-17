// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package config_test

// The registry's own spec, next to the registry. It lived in one role's
// package main, which meant every other role exercised these refusals only by
// accident.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// The names here are deliberately not spelled MARGINCE_*: the tree-wide
// documentation gate reads every quoted one of those as a variable somebody
// must document, and these are arguments to a constructor.
func TestTheRegistryRefusesWhatItCannotDescribeHonestly(t *testing.T) {
	for name, group := range map[string][]config.Item{
		// A default is echoed wherever the surface is described, so a secret
		// carrying one publishes a credential by construction.
		"a secret with a default": {{
			Name: "AN_ITEM", Kind: config.KindString, Secret: true, Default: "hunter2", Doc: "x",
		}},
		// Two packages claiming one variable, whose docs must then disagree.
		"declared twice": {
			{Name: "A_REPEATED_ITEM", Kind: config.KindString, Doc: "one"},
			{Name: "A_REPEATED_ITEM", Kind: config.KindString, Doc: "another"},
		},
		"no doc":  {{Name: "AN_UNDOCUMENTED_ITEM", Kind: config.KindString}},
		"no kind": {{Name: "AN_UNTYPED_ITEM", Doc: "w"}},
		"no name": {{Kind: config.KindString, Doc: "w"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.NewRegistry(group); err == nil {
				t.Fatal("the registry accepted a set it cannot describe honestly")
			} else if !strings.Contains(err.Error(), "config:") {
				t.Errorf("error %q does not name the package an operator should look at", err)
			}
		})
	}
}

func TestTheRefusalNeverEchoesTheSecretItRefused(t *testing.T) {
	// The one place this package prints an item's value. A secret with a
	// default is exactly the case where that value is a credential, so the
	// error names the variable and never what it holds.
	_, err := config.NewRegistry([]config.Item{{
		Name: "AN_ITEM", Kind: config.KindString, Secret: true, Default: "hunter2", Doc: "x",
	}})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the refusal printed the credential it was refusing: %v", err)
	}
	if !strings.Contains(err.Error(), "AN_ITEM") {
		t.Errorf("the refusal does not name the item to fix: %v", err)
	}
}

func TestItemsAreOrderedSoAGeneratedArtefactDoesNotChurn(t *testing.T) {
	r, err := config.NewRegistry([]config.Item{
		{Name: "C_ITEM", Kind: config.KindString, Doc: "c"},
		{Name: "A_ITEM", Kind: config.KindString, Doc: "a"},
		{Name: "B_ITEM", Kind: config.KindString, Doc: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range r.Items() {
		got = append(got, item.Name)
	}
	if strings.Join(got, ",") != "A_ITEM,B_ITEM,C_ITEM" {
		t.Errorf("Items() = %v; a map's order would make every regenerated artefact a diff", got)
	}
}
