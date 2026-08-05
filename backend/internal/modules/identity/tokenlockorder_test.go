// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Lock ORDER between the set-password token paths, pinned at the source.
//
// Redemption locks the `auth_token` row and then writes `app_user`. An issuer
// that took an `app_user` row lock and then wrote `auth_token` would take the
// same two locks in the opposite order, so a redeem racing an issue could each
// hold what the other waits for: Postgres aborts one with `deadlock detected`,
// and in the forgot-password case it does so silently, because that mint runs
// detached from the request.
//
// This is a SOURCE check rather than a concurrency test, and deliberately. A
// two-goroutine race test for this passed just as happily against the
// deadlocking lock order — the window is too narrow to hit reliably — so it
// asserted nothing while reading as though it did. The invariant is structural,
// so it is checked structurally: the issuers take no app_user row lock at all,
// and serialize on the advisory lock instead, which also covers the case a row
// lock never could — a member with no outstanding token to lock.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tokenIssuers are the functions that mint a set-password token. Both write
// auth_token, so both would close the cycle if they held an app_user row lock.
// gatekit:fixture the functions under test, mapped to the file each lives in — not waived costs
var tokenIssuers = map[string]string{
	"supersedeSetPasswordTokens": "userpasswordlink.go", // the admin-issued link
	"CreatePasswordReset":        "reset.go",            // the emailed reset
}

// memberRowLocks are the ways a statement takes a row lock on the member.
// Naming only FOR UPDATE would leave the guard green against the very shape
// redemption uses to acquire that lock — a bare `UPDATE app_user`, which locks
// the row just as surely as an explicit clause does. A guard that misses the
// most likely spelling of the defect is worse than none, because it reads as
// though the invariant were covered.
var memberRowLocks = []string{
	"FOR UPDATE",
	"FOR NO KEY UPDATE",
	"FOR SHARE",
	"FOR KEY SHARE",
	"UPDATE app_user",
	"DELETE FROM app_user",
}

var funcStart = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)

// funcBody returns the source of one top-level function, from its signature to
// the closing brace in column zero.
func issuerBody(t *testing.T, file, name string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	for _, loc := range funcStart.FindAllSubmatchIndex(source, -1) {
		if string(source[loc[2]:loc[3]]) != name {
			continue
		}
		rest := string(source[loc[0]:])
		end := strings.Index(rest, "\n}\n")
		if end < 0 {
			t.Fatalf("%s: %s has no closing brace in column zero", file, name)
		}
		return rest[:end]
	}
	t.Fatalf("%s: no function named %s — this guard no longer matches the code", file, name)
	return ""
}

func TestSetPasswordTokenIssuersTakeNoRowLockOnTheMember(t *testing.T) {
	for name, file := range tokenIssuers {
		body := issuerBody(t, file, name)

		for _, lock := range memberRowLocks {
			if strings.Contains(body, lock) {
				t.Errorf(
					"%s uses %q, which takes a row lock on the member while it writes "+
						"auth_token — redemption locks auth_token FIRST and then writes "+
						"app_user, so this inverts the order and a redeem racing an issue can "+
						"deadlock. Serialize with lockMemberForTokenIssue instead.", name, lock)
			}
		}
		if !strings.Contains(body, "lockMemberForTokenIssue") {
			t.Errorf(
				"%s mints a set-password token without lockMemberForTokenIssue — two issuers "+
					"racing would each miss the other's uncommitted insert and both leave a live "+
					"token, so the one-outstanding-token rule would hold only when nobody raced.",
				name)
		}
	}
}
