// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The axis separation, held as obligations DERIVED from the system rather than
// as lists somebody has to remember to update (repo rule 2). Each of these
// fails the moment the two vocabularies start leaking into each other again,
// which is the failure ADR-0107/A158 exists to prevent and the one no unit test
// can see: it lives in the schema, the contract document, and the registry at
// once.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// contractEnumRe finds enum members spelled anywhere in the contract document.
// Deliberately a scan of the raw text rather than a parse of the four nodes
// anyone currently remembers: a provider name reappearing in a FIFTH enum added
// later is exactly the regression this guards, and a parser aimed at the known
// nodes would not see it.
//
// BOTH YAML sequence styles, because the document uses both: flow
// (`enum: [a, b]`) and block (`enum:` then `- a`). Matching only flow would
// leave four existing block-style enums unchecked and the guard would report
// clean over a document it had not fully read.
var (
	contractEnumRe      = regexp.MustCompile(`enum:\s*\[([^\]]*)\]`)
	contractBlockEnumRe = regexp.MustCompile(`(?m)^\s*enum:\s*\n((?:\s*-\s*[^\n]*\n)+)`)
)

// contractEnums returns every enum's member set, from both spellings.
func contractEnums(raw string) [][]string {
	var out [][]string
	for _, m := range contractEnumRe.FindAllStringSubmatch(raw, -1) {
		out = append(out, strings.Split(m[1], ","))
	}
	for _, m := range contractBlockEnumRe.FindAllStringSubmatch(raw, -1) {
		var members []string
		for _, line := range strings.Split(m[1], "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "-") {
				members = append(members, strings.TrimSpace(strings.TrimPrefix(line, "-")))
			}
		}
		out = append(out, members)
	}
	return out
}

func contractPath(t *testing.T, name string) string {
	t.Helper()
	// The test binary runs in its package directory; the contract lives at the
	// backend module root.
	return filepath.Join("..", "..", "api", name)
}

// A registered transport must not appear as a member of any kind-style enum in
// the contract. Derived from channel_provider — the registry IS the list, so a
// provider registered tomorrow is covered without touching this test.
//
// Written as ONE assertion with a NAMED exception set rather than a headline
// plus a proviso: the two workspace-bot enums legitimately contain `telegram`
// because they bind a bot to a workspace, a surface an extension unit never
// writes (ADR-0107 §6). Anything outside that set is the conflation returning.
func TestNoContractEnumSpellsARegisteredTransport(t *testing.T) {
	integration.Setup(t)
	owner := integration.OwnerConn(t)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `SELECT provider FROM channel_provider ORDER BY provider`)
	if err != nil {
		t.Fatalf("reading the provider registry: %v", err)
	}
	var providers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scanning a provider: %v", err)
		}
		providers = append(providers, p)
	}
	rows.Close()
	if len(providers) == 0 {
		t.Fatal("the provider registry is empty, so this test would pass by having nothing to check")
	}

	raw, err := os.ReadFile(contractPath(t, "crm.yaml"))
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}

	// The workspace-bot binding surface: ChannelConnection.provider and
	// ConnectChannelRequest.provider both legitimately enumerate the core bot
	// connectors. Identified by the enum's exact content rather than by a
	// schema name, because the name is not visible in a raw scan and a
	// content match cannot silently widen.
	allowed := map[string]bool{"telegram": true}

	for _, members := range contractEnums(string(raw)) {
		trimmed := make(map[string]bool, len(members))
		for _, m := range members {
			trimmed[strings.Trim(strings.TrimSpace(m), `'"`)] = true
		}
		// A closed bot-binding enum is exactly the allowed set and nothing else.
		if len(trimmed) == len(allowed) {
			onlyAllowed := true
			for m := range trimmed {
				if !allowed[m] {
					onlyAllowed = false
				}
			}
			if onlyAllowed {
				continue
			}
		}
		for _, provider := range providers {
			if trimmed[provider] {
				t.Errorf("a contract enum %v spells the registered transport %q; a provider vocabulary is a deployment fact and cannot live in an installation-independent enum (ADR-0107/A158) — use ProviderRef",
					members, provider)
			}
		}
	}
}

// activity_kind holds EXACTLY the contract's kind vocabulary. One statement
// binding the table to the document, so the two cannot drift into a state where
// a contract-valid kind fails an FK it has no way to know about — the failure
// mode 0240's own seed comment describes — or where the table carries a kind
// nothing can be written with.
//
// The contract side is read through the GENERATED Valid(), not by scraping the
// YAML: the generator already decided what the enum is, a text scan cannot tell
// Activity.kind from an unrelated `kind` enum elsewhere in the document (the
// legal-page kinds are home/impressum/team), and a test that has to be taught
// which node to look at is a list again.
func TestActivityKindTableMatchesTheContractKindEnum(t *testing.T) {
	integration.Setup(t)
	owner := integration.OwnerConn(t)
	ctx := context.Background()

	rows, err := owner.Query(ctx, `SELECT kind FROM activity_kind ORDER BY kind`)
	if err != nil {
		t.Fatalf("reading activity_kind: %v", err)
	}
	var inTable []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scanning a kind: %v", err)
		}
		inTable = append(inTable, k)
	}
	rows.Close()
	if len(inTable) == 0 {
		t.Fatal("activity_kind is empty, so this test would pass by having nothing to check")
	}

	// Every kind the table carries must be one the contract admits.
	for _, k := range inTable {
		if !crmcontracts.ActivityKind(k).Valid() {
			t.Errorf("activity_kind carries %q and the contract does not admit it: a kind nothing can be written with is a vocabulary nobody maintains", k)
		}
	}

	// And the reverse, which is the direction that breaks a caller: every kind
	// the contract admits must exist in the table, or a contract-valid request
	// fails a foreign key the caller cannot see.
	//
	// This half IS a hand-written list, unlike everything else in this file: the
	// generator emits no All-slice to walk, and Valid() answers per value rather
	// than enumerating. A seventh kind added to the contract without a row here
	// would pass — stated rather than glossed, because a test that looks derived
	// and is not is worse than one that admits it.
	seeded := make(map[string]bool, len(inTable))
	for _, k := range inTable {
		seeded[k] = true
	}
	for _, k := range []crmcontracts.ActivityKind{
		crmcontracts.ActivityKindCall,
		crmcontracts.ActivityKindEmail,
		crmcontracts.ActivityKindMeeting,
		crmcontracts.ActivityKindMessage,
		crmcontracts.ActivityKindNote,
		crmcontracts.ActivityKindTask,
	} {
		if !seeded[string(k)] {
			t.Errorf("the contract admits kind %q and activity_kind does not carry it: a contract-valid request would fail a foreign key the caller cannot see", k)
		}
	}
}
