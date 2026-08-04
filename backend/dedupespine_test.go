// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The identity-spine fitness functions.
//
// Duplicate people, organizations and leads do not get in through the
// matching engine — that engine (PO-F-1/PO-F-2 in
// internal/modules/people/dedupe.go) is good. They get in through a NEW create
// path that forgets to ask it, or asks it with half the inputs. Every real
// duplicate found in a live workspace so far arrived exactly that way: capture
// asked PO-F-2 about a domain and never about a name; cold start asked nothing
// at all; lead promotion probed one column by hand.
//
// A code review cannot hold that line, because the offending change always
// looks locally reasonable. These tests hold it instead: they derive the set of
// INSERT sites from the tree, so a new one has to be argued for here, in a file
// whose whole subject is why the list is short.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// mintSite matches an INSERT into one of the three identity tables. The
// trailing [\s(] is what keeps `person_email`, `organization_domain` and
// `lead_routing_rule` out: Go's \b treats `_` as a word character, so it would
// match every child table too.
var mintSite = regexp.MustCompile(`(?is)INSERT\s+INTO\s+(person|organization|lead)[\s(]`)

// sanctionedMintSites is the whole list of files allowed to mint an identity
// row, each with the reason it is on it. Adding an entry is a deliberate
// decision about where duplicates can enter the system, which is why it is
// spelled out rather than pattern-matched.
var sanctionedMintSites = gatekit.Waive(map[string]string{
	// The chokepoint. createPerson/createOrganization take the PO-F ladder's
	// verdict as an argument, so a create that never consulted it cannot be
	// written, and they refuse outright when the verdict is an exact-key
	// collision.
	"internal/modules/people/resolvecreate.go": "the identity chokepoint itself",

	// The lead's second write shape. Leads carry exact keys of their own
	// (ADR-0008 keeps them out of person matching), and a captured lead is
	// created behind guards that live above it in capture.Sink.Upsert — the
	// connector-principal check, the RC-2 exclusion gate and the raw_capture
	// evidence write. Its identity probes are storekit's, shared with the
	// direct create; see TestLeadIdentityProbesAreSingleSourced below.
	"internal/modules/capture/sinklead.go": "the captured-lead write shape",

	// The direct lead create: same probes, different policy on a hit (409 with
	// the incumbent's id rather than a staged merge).
	"internal/modules/people/lead.go": "the direct lead write shape",

	// Test seeding, not a product path.
	"internal/compose/integration/harness.go": "integration-harness row seeding",
})

func TestEveryIdentityInsertGoesThroughTheChokepoint(t *testing.T) {
	defer sanctionedMintSites.AssertAllMatched(t)
	for _, path := range goSourceFiles(t, ".") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)
		if isGenerated(path, text) || !mintSite.MatchString(text) {
			continue
		}
		rel := filepath.ToSlash(path)
		if !sanctionedMintSites.Waived(t, rel) {
			t.Errorf("%s mints a person, organization or lead row directly.\n"+
				"Identity rows are minted only through the chokepoint in "+
				"internal/modules/people/resolvecreate.go, which requires the PO-F-1/PO-F-2 "+
				"verdict and refuses an exact-key collision. Route the insert through it — "+
				"or, if this really is a new write shape, enroll it in sanctionedMintSites "+
				"with the reason it cannot.", rel)
		}
	}
}

// leadClaimCheckSQL matches a hand-rolled CLAIM CHECK — "is this identity key
// already taken?", which answers with an id and nothing else.
//
// It deliberately does not match a read that returns the lead itself under the
// caller's row scope (FindLeadByLinkedInURL): showing someone the record they
// are allowed to see is a different question from deciding whether a key is
// free, and the two correctly have different queries.
var leadClaimCheckSQL = regexp.MustCompile(`(?is)SELECT\s+id\s+FROM\s+lead\s+WHERE\s+(email|linkedin_url)`)

// TestLeadIdentityProbesAreSingleSourced keeps the two lead write shapes asking
// ONE question. They answer a claimed identity differently on purpose — the
// direct create refuses, capture stages a merge — but when each owned its own
// probe they drifted: the LinkedIn key had a probe written for it that nothing
// ever called, so two imports of one person under different addresses both
// landed.
func TestLeadIdentityProbesAreSingleSourced(t *testing.T) {
	const home = "internal/platform/database/storekit/leadidentity.go"
	for _, path := range goSourceFiles(t, ".") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		rel := filepath.ToSlash(path)
		if rel == home {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if isGenerated(path, string(body)) {
			continue
		}
		if leadClaimCheckSQL.MatchString(string(body)) {
			t.Errorf("%s decides whether a lead identity key is claimed with its own SQL.\n"+
				"Both lead write shapes share one definition of \"already claimed\" — "+
				"use storekit.LiveLeadByEmail / storekit.LiveLeadByLinkedInURL (%s) "+
				"and apply this path's own policy to the answer.", rel, home)
		}
	}
}

// mergedIntoWrite matches a write of the merge redirect pointer.
var mergedIntoWrite = regexp.MustCompile(`(?is)(UPDATE\s+(person|organization)\s+SET[^;]*merged_into_id\s*=|INSERT\s+INTO\s+(person|organization)\b[^;]*merged_into_id)`)

// sanctionedMergeWriters own the redirect pointer that retires one record into
// another.
var sanctionedMergeWriters = gatekit.Waive(map[string]string{
	"internal/modules/people/mergerelink.go":        "the person merge path's satellite relink",
	"internal/modules/people/merge_organization.go": "the organization merge path",
})

// TestOnlyTheMergePathRetiresARecord is the structural half of the
// no-silent-auto-merge guarantee (data-hygiene: fuzzy candidates never
// auto-merge, at any confidence). Detection files a review row and nothing
// else; only a human's disposition, executed by the merge path, may point one
// record at another.
func TestOnlyTheMergePathRetiresARecord(t *testing.T) {
	defer sanctionedMergeWriters.AssertAllMatched(t)
	for _, path := range goSourceFiles(t, ".") {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		rel := filepath.ToSlash(path)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if isGenerated(path, string(body)) {
			continue
		}
		if !mergedIntoWrite.MatchString(string(body)) {
			continue
		}
		if sanctionedMergeWriters.Waived(t, rel) {
			continue
		}
		t.Errorf("%s writes merged_into_id.\n"+
			"Retiring one record into another is the merge path's alone "+
			"(internal/modules/people/merge.go, merge_organization.go), reached only by a "+
			"human's disposition on the dedupe queue — DEDUPE_FUZZY_AUTOMERGE is pinned never.", rel)
	}
}

// goSourceFiles lists every .go file under root, sorted so failures report in
// a stable order.
func goSourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			out = append(out, filepath.Clean(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("no Go files under %s — the fitness function would pass vacuously", root)
	}
	sort.Strings(out)
	return out
}
