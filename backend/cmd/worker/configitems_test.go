// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The worker's half of the completeness obligation. See
// cmd/api/configitems_test.go for why it is derived from the flags rather than
// checked against a list.
//
// "No secret carries a default" is not asserted here: the registry itself
// refuses that combination, so it holds for every role including ones that do
// not exist yet (internal/platform/config).

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/cliflags"
	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// workerSurface rebuilds the role's flag set and its declared items together,
// the way boot does.
func workerSurface(t *testing.T) (*cliflags.Env, []config.Item) {
	t.Helper()
	fs, env, _, err := workerFlagSet()
	if err != nil {
		t.Fatalf("building the worker flag set: %v", err)
	}
	items, err := workerConfigItems(fs, env)
	if err != nil {
		t.Fatalf("assembling the worker registry: %v", err)
	}
	return env, items
}

func TestEveryWorkerFlagBoundVariableIsDeclared(t *testing.T) {
	env, items := workerSurface(t)
	declared := make(map[string]bool, len(items))
	for _, item := range items {
		declared[item.Name] = true
	}
	for _, name := range env.EnvKeys() {
		if !declared[name] {
			t.Errorf("%s is read by a flag binding but declared by no config item — a generated template would omit it", name)
		}
	}
}

func TestTheWorkerMarksItsCredentials(t *testing.T) {
	// Named explicitly because nothing mechanical can tell a path from a
	// password. The default is already to withhold (cliflags.Items), so this
	// catches the opposite mistake: a credential wrongly listed as public.
	_, items := workerSurface(t)
	secret := make(map[string]bool, len(items))
	for _, item := range items {
		secret[item.Name] = item.Secret
	}
	for _, name := range []string{
		"MARGINCE_DSN", "MARGINCE_GMAIL_CLIENT_SECRET", "MARGINCE_GRAPH_CLIENT_SECRET",
		"MARGINCE_WEBHOOK_KEY", "MARGINCE_KEYVAULT_ROOT_KEY", "MARGINCE_LICENSE",
		"BRAVE_SEARCH_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
	} {
		if !secret[name] {
			t.Errorf("%s authenticates something and is not marked Secret", name)
		}
	}
	// And the converse, so the marking means something: a value that
	// authenticates nobody stays visible, because an operator debugging a boot
	// wants to read it.
	for _, name := range []string{"MARGINCE_LOG_LEVEL", "MARGINCE_PUBLIC_BASE_URL", "MARGINCE_ENV"} {
		if secret[name] {
			t.Errorf("%s is marked Secret but authenticates nothing; hiding it only makes a boot harder to debug", name)
		}
	}
}
