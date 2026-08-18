// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Every unix that is not macOS. This is NOT a third shipping target — the
// bundle is built for macOS and Windows only. It is what lets the module
// type-check on Linux, which is where the lint gate runs: files tagged darwin
// and windows alone leave every symbol in this package undefined there, and the
// gate reports a module that does not compile rather than a module that is
// clean.
//
// It names the right opener rather than borrowing the macOS one. `open` on
// Linux is an unrelated binary where it exists at all, so reusing it would put
// a line in the tree that reads as working code and is not.
//go:build !windows && !darwin

package main

import "os/exec"

// openBrowser is a convenience, never a requirement: the URL is printed by the
// caller, so a failure to launch a browser is not worth interrupting a working
// start for.
func openBrowser(url string) {
	// #nosec G204 -- xdg-open is addressed by absolute path and url is this launcher's own loopback address
	if err := exec.Command("/usr/bin/xdg-open", url).Start(); err != nil {
		say("  (could not open your browser automatically — visit %s)\n\n", url)
	}
}
