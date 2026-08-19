// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// EVERY CLOSED SET THIS UNIT PUBLISHES, HELD EQUAL TO THE GO THAT PRODUCES IT.
//
// WHY THIS FILE EXISTS, stated plainly because it is a real incident and not a
// hypothetical: api/crm.yaml was reverted by a `git checkout` in a parallel session
// while these fields were uncommitted. Every Go test still passed. The contract went on
// describing a save with no capture_mode and an entry verdict with no `none`, while the
// handler decoded one and acted on the other — so a client generated from the published
// document could not express what the code required, and nothing failed until somebody
// read the file.
//
// An enum is the shape of drift that hides best. A missing FIELD breaks a typecheck
// somewhere; a stale enum VALUE is well-formed YAML that quietly lies to every client
// and every model reading the surface. The four sets below are all consent or state
// vocabularies, which is the worst place to be wrong: `capture_mode` decides whether an
// installation reads somebody's whole personal chat life.
//
// SO THE ASSERTION IS BOTH DIRECTIONS AND PRESENCE. A value the code emits that the
// contract omits is a client surprised by an answer it was never told about; a value the
// contract publishes that nothing emits is a promise no code keeps; and a set that has
// vanished from the document entirely must fail rather than pass by finding nothing.

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// publishedEnums reads every closed set out of the contract fragment, keyed by the
// operation that publishes it and the field it belongs to.
//
// It scans lines rather than parsing YAML on purpose: this module imports only the
// published extension surface, and adding a YAML dependency to a unit's go.mod to check
// its own contract would widen the dependency surface of every build to buy one test.
//
// The owner of an `enum:` is the nearest preceding key at a SHALLOWER indent, which is
// what YAML nesting means — and reading it that way is what lets the two different
// `mode:` fields in this document be told apart instead of matched by whichever regex
// won.
func publishedEnums(t *testing.T, path string) map[string]map[string]bool {
	t.Helper()
	fragment, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the contract fragment: %v", err)
	}
	found := map[string]map[string]bool{}
	keyAt := map[int]string{}
	operation, collecting, into := "", 0, ""
	for _, line := range strings.Split(string(fragment), "\n") {
		if op := regexp.MustCompile(`^\s*operationId:\s*(\S+)`).FindStringSubmatch(line); op != nil {
			operation = op[1]
		}
		if collecting > 0 {
			if item := regexp.MustCompile(`^\s*-\s*(\S+)\s*$`).FindStringSubmatch(line); item != nil {
				found[into][item[1]] = true
				continue
			}
			collecting = 0
		}
		key := regexp.MustCompile(`^(\s*)([a-z_0-9']+):(.*)$`).FindStringSubmatch(line)
		if key == nil {
			continue
		}
		indent, name, rest := len(key[1]), key[2], key[3]
		if name != "enum" {
			keyAt[indent] = name
			for deeper := range keyAt {
				if deeper > indent {
					delete(keyAt, deeper)
				}
			}
			continue
		}
		at := operation + "." + ownerOf(keyAt, indent)
		found[at] = map[string]bool{}
		if inline := regexp.MustCompile(`\[(.*)\]`).FindStringSubmatch(rest); inline != nil {
			for _, value := range strings.Split(inline[1], ",") {
				found[at][strings.TrimSpace(value)] = true
			}
			continue
		}
		collecting, into = 1, at
	}
	return found
}

// ownerOf is the field an enum belongs to: the deepest key still shallower than it.
func ownerOf(keyAt map[int]string, indent int) string {
	owner, deepest := "", -1
	for at, name := range keyAt {
		if at < indent && at > deepest {
			owner, deepest = name, at
		}
	}
	return owner
}

func TestEveryClosedSetTheContractPublishesMatchesTheGoBehindIt(t *testing.T) {
	t.Parallel()
	published := publishedEnums(t, "api/crm.yaml")

	// The three consent and state vocabularies, each named by the operation that
	// publishes it so the two `mode` fields cannot be confused for one another.
	expected := map[string]map[string]bool{
		// HOW capture works, on the read side and on the write side. They are two
		// declarations of one decision and both are checked, because a client writes
		// what the save publishes and renders what the status publishes.
		"zaloPersonalStatus.capture_mode": {
			captureEveryoneExcept: true, captureOnlyChosen: true,
		},
		"zaloPersonalSaveAllowlist.capture_mode": {
			captureEveryoneExcept: true, captureOnlyChosen: true,
		},
		// What one entry says about one person. `none` is on BOTH sides: the read side
		// reports a person nobody has decided about, and the write side takes one off
		// the list — which is how removal is expressed, and was inexpressible on the
		// wire while this file was reverted even though the handler implemented it.
		"zaloPersonalSaveAllowlist.mode": {
			string(verdictAllow): true, string(verdictBlock): true, string(verdictNone): true,
		},
		"zaloPersonalContacts.mode": {
			string(verdictAllow): true, string(verdictBlock): true, string(verdictNone): true,
		},
		// The connection's own lifecycle, which the tick writes.
		"zaloPersonalStatus.status": {
			statusConnected: true, statusNeedsReconnect: true, statusDisconnected: true,
		},
	}
	for at, want := range expected {
		got, declared := published[at]
		if !declared {
			t.Fatalf("%s publishes no closed set at all — a field that has vanished from the contract must fail here rather than pass by finding nothing", at)
		}
		compareSets(t, at, want, got)
	}

	// The error classes are derived by CALLING the function that produces them rather
	// than from a list of constants, because the set is what failureClass answers and
	// not what somebody wrote down beside it.
	compareSets(t, "zaloPersonalStatus.last_error_class",
		everyFailureClass(), published["zaloPersonalStatus.last_error_class"])
}

// compareSets holds a published set and a Go set equal in both directions, naming which
// direction failed — the two failures mean different things to whoever reads them.
func compareSets(t *testing.T, at string, want, got map[string]bool) {
	t.Helper()
	for value := range want {
		if !got[value] {
			t.Fatalf("%s: the code produces %q and the contract does not publish it — a client is told nothing about a value it will be sent",
				at, value)
		}
	}
	for value := range got {
		if !want[value] {
			t.Fatalf("%s: the contract publishes %q and nothing in the code produces it — a promise no code keeps",
				at, value)
		}
	}
}

// everyFailureClass is every class failureClass can answer, obtained by asking it. A
// hand-written list here would be a third copy of the vocabulary and could drift from
// both the contract and the code it is meant to hold together.
func everyFailureClass() map[string]bool {
	classes := map[string]bool{}
	for _, cause := range failuresWorthClassing() {
		classes[failureClass(cause).Class] = true
	}
	return classes
}

// failuresWorthClassing is one cause per branch failureClass draws, so the set above is
// every class it can answer rather than every class somebody remembered.
func failuresWorthClassing() []error {
	return []error{
		extension.ErrForbidden, extension.ErrInvalid, context.DeadlineExceeded,
		errUnanswered, errors.New("something else entirely"),
	}
}

// THE PARTS OF THAT SAME LOSS THAT HAVE NO ENUM TO GUARD THEM.
//
// The enum check above is the canary for a reverted block, but three of the fields the
// handler depends on are SHAPE rather than vocabulary, and each one breaks a different
// thing when it goes:
//
//   - `capture_mode` REQUIRED on the save. Optional, the strict decoder still refuses a
//     document without it, so the contract would advertise a call that always 422s.
//   - `entries` admitting the EMPTY list. At minItems 1 the commonest first save under
//     everyone_except — "everything, nobody left out yet" — is unpublishable, and the
//     handler accepts it, so a generated client could not make the call the screen makes.
//   - no `last_msg_id` on the connection. The reading positions moved to their own table,
//     so a field describing a column that no longer exists is a promise the read cannot
//     keep.
func TestTheSaveShapeTheHandlerRequiresIsTheShapeTheContractPublishes(t *testing.T) {
	t.Parallel()
	fragment, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading the contract fragment: %v", err)
	}
	document := string(fragment)

	// The save's own required list, taken from the block that follows its operationId
	// so a `required:` belonging to another operation cannot answer for it.
	at := strings.Index(document, "operationId: zaloPersonalSaveAllowlist")
	if at < 0 {
		t.Fatal("the fragment declares no zaloPersonalSaveAllowlist operation at all, so the save the screen makes reaches nothing")
	}
	save := document[at:]
	required := regexp.MustCompile(`required: \[([^\]]*)\]`).FindStringSubmatch(save)
	if required == nil || !strings.Contains(required[1], "capture_mode") {
		t.Fatalf("the save does not require capture_mode: %v — there is no default mode, so a save without one is not a call this unit can honour", required)
	}
	if !regexp.MustCompile(`entries:\n\s*type: array\n(?:\s*#.*\n)*\s*minItems: 0`).MatchString(save) {
		t.Fatal("the save's entries list does not admit an empty one — under everyone_except a mode with nobody left out is the commonest save there is, and the handler accepts it")
	}
	if strings.Contains(document, "last_msg_id") {
		t.Fatal("the contract still describes last_msg_id on the connection; the reading positions live in ext_zalo_personal_conversation_cursor, one row per conversation")
	}
}

// THE GOVERNANCE FIELDS, DERIVED FROM THE DOCUMENT RATHER THAN LISTED.
//
// Two mistakes are worth a fitness function here because both have already been made
// in this unit's own history, and neither shows up as a failing build:
//
//   - A SERVED 🟡. compose/extensiontools.go refuses a confirmation_required tool that
//     ships a handler outright, because the admission gate stages an approval only for
//     tools implementing the registry's staging seam and the data-only extension
//     adapter does not — so a 🟡 declared here is not a staged operation, it is one
//     refused on every call, behind a route that answers 501. That is issue #1651, and
//     it was declared and reverted once on this branch.
//   - A MUTATING GET. The human seat ceiling classifies a mutation by its METHOD, so a
//     GET carrying a non-`read` scope is a read seat's route to a write.
//     validateMethodAuthority refuses one, again at boot rather than in a test.
//
// Every operation in the fragment is checked rather than a remembered list of them, so
// the operation added next is covered by the fact that it exists.
func TestEveryOperationDeclaresATierAndMethodTheSeamWillServe(t *testing.T) {
	t.Parallel()
	governance := publishedGovernance(t, "api/crm.yaml")
	if len(governance) != len(New().Tools) {
		t.Fatalf("the fragment declares %d operation(s) and the unit ships %d tool handler(s); a declared verb with no handler mounts a route that answers 501, and a handler with no declaration is unreachable",
			len(governance), len(New().Tools))
	}
	for operation, declared := range governance {
		if declared.tier != "auto_execute" {
			t.Fatalf("%s declares tier %q: the extension seam cannot stage an approval for a served tool (issue #1651), so this is not a staged operation but one refused on every call",
				operation, declared.tier)
		}
		if declared.method == "get" && declared.scope != "read" {
			t.Fatalf("%s is a GET carrying scope %q — the seat ceiling classifies a mutation by method, so this is a read seat's route to a write",
				operation, declared.scope)
		}
		if declared.method != "get" && declared.scope == "read" {
			t.Fatalf("%s is a %s carrying scope read, so an operation that changes something is billed to a read seat",
				operation, strings.ToUpper(declared.method))
		}
	}
}

// governanceOf is the three fields the seam judges an operation by.
type governanceOf struct{ method, tier, scope string }

// publishedGovernance reads each operation's method, tier and scope out of the
// fragment, keyed by operationId. It scans lines for the reason publishedEnums does:
// this module imports only the published extension surface, and a YAML dependency in a
// unit's go.mod to check its own contract widens every build to buy one test.
//
// The METHOD is the key that opens an operation's block, which is the line before its
// operationId — so it is remembered as it goes past rather than searched for backwards.
func publishedGovernance(t *testing.T, path string) map[string]governanceOf {
	t.Helper()
	fragment, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the contract fragment: %v", err)
	}
	found, method, at := map[string]governanceOf{}, "", ""
	verb := regexp.MustCompile(`^\s{6}(get|put|post|patch|delete):\s*$`)
	field := regexp.MustCompile(`^\s*(operationId|tier|scope):\s*(\S+)`)
	for _, line := range strings.Split(string(fragment), "\n") {
		if opened := verb.FindStringSubmatch(line); opened != nil {
			method = opened[1]
			continue
		}
		named := field.FindStringSubmatch(line)
		if named == nil {
			continue
		}
		switch named[1] {
		case "operationId":
			at = named[2]
			found[at] = governanceOf{method: method}
		case "tier":
			found[at] = governanceOf{method: found[at].method, tier: named[2], scope: found[at].scope}
		case "scope":
			found[at] = governanceOf{method: found[at].method, tier: found[at].tier, scope: named[2]}
		}
	}
	return found
}
