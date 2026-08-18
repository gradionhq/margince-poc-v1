// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "os/exec"

// openBrowser is a convenience, never a requirement: the URL is printed by the
// caller, so a failure to launch a browser is not worth interrupting a working
// start for.
//
// The absolute path is deliberate. Resolving "open" through PATH would let any
// writable directory ahead of /usr/bin decide what this launches, and it
// launches with the user's own privileges at the end of a successful start.
func openBrowser(url string) {
	// #nosec G204 -- /usr/bin/open is an absolute system path and url is this launcher's own loopback address
	if err := exec.Command("/usr/bin/open", url).Start(); err != nil {
		say("  (could not open your browser automatically — visit %s)\n\n", url)
	}
}
