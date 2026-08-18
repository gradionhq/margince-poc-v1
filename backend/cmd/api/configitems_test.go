// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The registry is only worth having if it is COMPLETE. A declaration set that
// silently misses a variable is worse than no declaration set, because the
// generated template and schema it feeds would then describe a surface smaller
// than the real one — and an operator would trust them.
//
// So the obligation is derived from the flags themselves rather than from a
// list: every environment binding parseAPIFlags registers must appear, with the
// FlagSet's own default and usage text carried through.

import (
	"flag"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/cliflags"
	"github.com/gradionhq/margince/backend/internal/platform/config"
)

// apiSurface rebuilds the role's flag set and its declared items together, the
// way boot does.
func apiSurface(t *testing.T) (*flag.FlagSet, *cliflags.Env, []config.Item) {
	t.Helper()
	fs, env, _, err := apiFlagSet()
	if err != nil {
		t.Fatalf("building the api flag set: %v", err)
	}
	registry, err := apiConfigItems(fs, env)
	if err != nil {
		t.Fatalf("assembling the api registry: %v", err)
	}
	return fs, env, registry.Items()
}

func TestEveryFlagBoundVariableIsDeclared(t *testing.T) {
	_, env, items := apiSurface(t)
	declared := make(map[string]config.Item, len(items))
	for _, item := range items {
		declared[item.Name] = item
	}
	for _, name := range env.EnvKeys() {
		if _, ok := declared[name]; !ok {
			t.Errorf("%s is read by a flag binding but declared by no config item — a generated template would omit it", name)
		}
	}
}

func TestDeclaredDocsComeFromTheFlagsThemselves(t *testing.T) {
	// The point of reading the usage text back rather than restating it: `-h`
	// and the generated reference cannot disagree. Every binding, not one — a
	// single-flag version passes having checked nothing the day that flag is
	// renamed.
	fs, env, items := apiSurface(t)
	byName := make(map[string]config.Item, len(items))
	for _, item := range items {
		byName[item.Name] = item
	}
	checked := 0
	for _, name := range env.EnvKeys() {
		item, declared := byName[name]
		if !declared {
			continue // TestEveryFlagBoundVariableIsDeclared owns that failure
		}
		f := fs.Lookup(item.FlagName)
		if f == nil {
			continue
		}
		if item.Doc != f.Usage {
			t.Errorf("%s doc = %q, flag -%s usage = %q — they must be one sentence", name, item.Doc, f.Name, f.Usage)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("compared no bindings at all; the check proved nothing")
	}
}

func TestTheCredentialsThisRoleReadsAreMarkedSecret(t *testing.T) {
	// Named explicitly because nothing mechanical can tell a path from a
	// password. The default is already to withhold (cliflags.Items), so this
	// catches the opposite mistake: a credential wrongly listed as public.
	_, _, items := apiSurface(t)
	secret := make(map[string]bool, len(items))
	for _, item := range items {
		secret[item.Name] = item.Secret
	}
	for _, name := range []string{
		"MARGINCE_DSN", "MARGINCE_SCHEMA_DSN",
		"MARGINCE_GMAIL_CLIENT_SECRET", "MARGINCE_GMAIL_PUSH_TOKEN",
		"MARGINCE_GRAPH_CLIENT_SECRET", "MARGINCE_HUBSPOT_APP_SECRET",
		"MARGINCE_CONNECTOR_STATE_KEY", "MARGINCE_WEBHOOK_KEY", "MARGINCE_METRICS_TOKEN",
		"MARGINCE_KEYVAULT_ROOT_KEY", "MARGINCE_LICENSE",
		"MARGINCE_BLOBSTORE_ACCESS_KEY", "MARGINCE_BLOBSTORE_SECRET_KEY",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
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

// The api's half of the wiring check; see the worker's copy for why the rule
// itself is proven in platform/config rather than here.
func TestAMisspelledVariableReachesTheBootReport(t *testing.T) {
	// Assembled rather than written whole: it is a MISSPELLING, and the
	// tree-wide documentation gate reads a quoted MARGINCE_* literal as a real
	// variable somebody must document.
	const misspelled = "MARGINCE_" + "REDDIS"
	t.Setenv(misspelled, "localhost:6379")
	cfg, err := parseAPIFlags([]string{"--dsn", "postgres://user@localhost:5432/db"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(cfg.unknownVars, misspelled) {
		t.Errorf("unknownVars = %v; the misspelling never reached the boot report", cfg.unknownVars)
	}
	if slices.Contains(cfg.unknownVars, "MARGINCE_DSN") {
		t.Error("a variable this role reads was reported as unread")
	}
}
