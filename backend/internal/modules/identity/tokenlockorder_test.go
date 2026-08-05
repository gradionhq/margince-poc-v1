// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Lock ORDER between the set-password token paths, pinned at the source.
//
// Two rows are contended: the member (`app_user`) and their token
// (`auth_token`). Redemption takes the token row first and then writes the
// member. Anything that took them the other way round would let a redeem racing
// an issue each hold what the other waits for — Postgres aborts one with
// `deadlock detected`, and for the forgot-password mint it would do so
// silently, because that runs detached from the request.
//
// So the invariant is an ORDER, not the absence of a lock: whoever touches both
// must reach the token row first. That is what this file checks, by reading the
// SQL each function actually issues and comparing positions.
//
// It is a source check rather than a concurrency test, deliberately. A
// two-goroutine race test for this passed just as happily against the
// deadlocking order — the window is too narrow to hit reliably — so it asserted
// nothing while reading as though it did.
//
// The population is DERIVED from the package rather than listed. An earlier cut
// named two functions by hand and missed `IssuePasswordLink`, which owns the
// transaction and is the likeliest place a future edit would add a member lock:
// the guard stayed green exactly where it mattered most.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// A top-level func, and the SQL literals inside it. Statements are
	// backtick-quoted throughout this package.
	funcStart = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)
	sqlText   = regexp.MustCompile("(?s)`[^`]*`")

	// The clauses that take a row lock on an EXISTING row. An INSERT is absent
	// on purpose: it creates a row rather than contending for one, so a mint
	// for a brand-new member is not part of the cycle.
	rowLockClauses   = regexp.MustCompile(`FOR UPDATE|FOR NO KEY UPDATE|FOR SHARE|FOR KEY SHARE`)
	updatesOrDeletes = func(sql, table string) bool {
		return strings.Contains(sql, "UPDATE "+table) || strings.Contains(sql, "DELETE FROM "+table)
	}
)

// locksRow reports whether one statement takes a row lock on the named table,
// either by an explicit clause or by being an UPDATE/DELETE against it.
func locksRow(sql, table string) bool {
	if !strings.Contains(sql, table) {
		return false
	}
	return rowLockClauses.MatchString(sql) || updatesOrDeletes(sql, table)
}

type identityFunc struct {
	name string
	file string
	body string
}

// packageFuncs returns every top-level function in the package's non-test
// sources. Deriving the set is the point: a function added tomorrow is checked
// without anyone remembering to add it here.
func packageFuncs(t *testing.T) []identityFunc {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []identityFunc
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(source)
		locs := funcStart.FindAllSubmatchIndex(source, -1)
		for i, loc := range locs {
			end := len(text)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			out = append(out, identityFunc{
				name: text[loc[2]:loc[3]], file: name, body: text[loc[0]:end],
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("found no package functions — the guard is vacuous")
	}
	return out
}

func TestTokenPathsReachTheTokenRowBeforeTheMemberRow(t *testing.T) {
	checked := 0
	for _, fn := range packageFuncs(t) {
		firstMember, firstToken := -1, -1
		for i, sql := range sqlText.FindAllString(fn.body, -1) {
			if firstMember < 0 && locksRow(sql, "app_user") {
				firstMember = i
			}
			// An INSERT counts here: it is the write the member lock would be
			// held across, which is what closes the cycle.
			if firstToken < 0 && (locksRow(sql, "auth_token") || strings.Contains(sql, "INSERT INTO auth_token")) {
				firstToken = i
			}
		}
		if firstMember < 0 || firstToken < 0 {
			continue // touches at most one of the two rows: not part of the cycle
		}
		checked++
		if firstMember < firstToken {
			t.Errorf(
				"%s:%s locks the member row before it reaches auth_token — redemption takes "+
					"the token row FIRST, so this inverts the order and a redeem racing this can "+
					"deadlock. Hold no app_user row lock and serialize with "+
					"lockMemberForTokenIssue instead.", fn.file, fn.name)
		}
	}
	if checked == 0 {
		t.Fatal("no function was found touching both rows — the guard no longer matches the code")
	}
}

func TestTokenSupersedersSerializeOnTheMember(t *testing.T) {
	checked := 0
	for _, fn := range packageFuncs(t) {
		// Superseding-then-inserting is the shape that needs serializing: two
		// racers at READ COMMITTED each miss the other's uncommitted insert and
		// both leave a live token. A mint that supersedes nothing (a brand-new
		// member) has no such race and is excluded by this condition, not by a
		// hand-maintained exemption.
		if !strings.Contains(fn.body, "UPDATE auth_token SET used_at") ||
			!strings.Contains(fn.body, "INSERT INTO auth_token") {
			continue
		}
		checked++
		if !strings.Contains(fn.body, "lockMemberForTokenIssue") {
			t.Errorf(
				"%s:%s supersedes and re-mints a member's token without "+
					"lockMemberForTokenIssue — two issuers racing would both leave a live "+
					"token, so the one-outstanding-token rule would hold only when nobody "+
					"raced.", fn.file, fn.name)
		}
	}
	if checked == 0 {
		t.Fatal("no supersede-then-mint function found — the guard no longer matches the code")
	}
}
