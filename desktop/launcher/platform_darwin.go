// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// busBinary is the event bus this platform ships.
//
// Valkey rather than Redis: the bundle redistributes this binary inside a
// BUSL-1.1 product, and Redis 7.4 onward is RSALv2/SSPL. Valkey is the
// BSD-licensed fork of the same lineage and speaks the same protocol, so
// platform/events needs no change to talk to it. Windows has no Valkey build
// at all, which is why the constant is per-platform — see platform_windows.go.
const busBinary = "valkey-server"

// exeName is what an executable is called on disk. Unix has no suffix.
func exeName(name string) string { return name }

// localTimezone reports the IANA zone name macOS records, so the first-run
// organization is created in the user's own time rather than UTC.
func localTimezone() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "UTC"
	}
	// /var/db/timezone/zoneinfo/Europe/Berlin -> Europe/Berlin
	const marker = "/zoneinfo/"
	if idx := strings.Index(target, marker); idx >= 0 {
		return target[idx+len(marker):]
	}
	return "UTC"
}

// holdConsole does nothing here: Finder opens "Start Margince.command" in
// Terminal, which keeps the window on screen after the process exits, so a
// failure message is still readable without being asked for.
func holdConsole() {}

// openBrowser is a convenience, never a requirement: the URL is printed by the
// caller, so a failure to launch a browser is not worth interrupting a working
// start for.
func openBrowser(url string) {
	if err := exec.Command("open", url).Start(); err != nil {
		fmt.Printf("  (could not open your browser automatically — visit %s)\n\n", url)
	}
}
