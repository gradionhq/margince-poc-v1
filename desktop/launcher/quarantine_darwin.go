// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "os/exec"

// quarantineAttr is the extended attribute a downloader stamps on a file it
// did not originate. Gatekeeper assesses only files that carry it, and it does
// so at the moment each one is EXECUTED, not when the folder is opened.
const quarantineAttr = "com.apple.quarantine"

// xattrTool is addressed absolutely for the same reason /usr/bin/open is: a
// writable directory earlier in PATH must not get to choose what this runs.
//
// It is a genuine system binary, not one of the Command Line Tools stubs that
// share an inode across /usr/bin — invoking one of those on a Mac without Xcode
// opens an installer prompt, which would be a worse first launch than the
// dialogs this exists to remove.
const xattrTool = "/usr/bin/xattr"

// clearQuarantine removes the download quarantine from the programs this
// launcher is about to spawn.
//
// A browser stamps the attribute on the archive it downloads and the
// unarchiver copies it onto what it extracts, so a bundle that arrived that way
// carries it across the tree. Because Gatekeeper asks at exec time, a single
// start then becomes a queue of identical dialogs — initdb, postgres,
// valkey-server, migrate, api, worker — each blocking the boot until answered,
// and none of them explaining why answering the last one did not count.
//
// Only runtime/ is cleared. That is exactly what ships and what gets spawned,
// and it excludes data/, which holds the user's own records and a live Postgres
// socket that the call fails against.
//
// This removes a repeated prompt, not the reason for it. The bundle is ad-hoc
// signed, so a published build still needs a Developer ID and notarization —
// after which nothing here would reach Gatekeeper at all.
func clearQuarantine(l layout) {
	if !anyQuarantined(l) {
		return
	}

	say("Clearing the download quarantine, so macOS asks about Margince once rather than once per program…\n")
	// The whole of runtime/, not just the executables probed below: Postgres
	// loads its extensions as dylibs, and a quarantined library is refused on
	// load exactly as a quarantined program is on exec.
	//
	// #nosec G204 -- both arguments are absolute paths this launcher computes from its own location
	if err := exec.Command(xattrTool, "-r", "-d", quarantineAttr, l.runtimeDir()).Run(); err != nil {
		say("  (could not clear it: %v)\n", err)
		say("  macOS may now ask you to allow each program in turn. To stop that, quit and run:\n")
		say("    xattr -dr %s %q\n\n", quarantineAttr, l.runtimeDir())
	}
}

// anyQuarantined reports whether any program the launcher spawns still carries
// the attribute.
//
// It asks about every one of them rather than trusting a single sentinel,
// because approving a program through System Settings clears that file alone.
// A user who has already answered two dialogs by hand has a bundle that is
// half-marked, and one probe against the wrong half would read it as clean and
// leave the remaining dialogs in place — the exact situation this is for.
//
// Reading the attribute has to go through the tool as well: Go's syscall
// package exposes no xattr call on darwin.
func anyQuarantined(l layout) bool {
	for _, program := range []string{
		l.pgBin("initdb"), l.pgBin("postgres"), l.pgBin("pg_isready"), l.pgBin("psql"),
		l.appBin(busBinary), l.appBin("migrate"), l.appBin("api"), l.appBin("worker"),
	} {
		// A non-zero exit is the attribute being absent — and also a missing
		// file or a missing tool, neither of which this can act on either.
		//
		// #nosec G204 -- both arguments are absolute paths this launcher computes from its own location
		if err := exec.Command(xattrTool, "-p", quarantineAttr, program).Run(); err == nil {
			return true
		}
	}
	return false
}
