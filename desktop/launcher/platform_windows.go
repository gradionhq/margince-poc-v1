// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// busBinary is the event bus this platform ships.
//
// Redis rather than Valkey, and not by preference: Valkey has no Windows build
// and upstream declines to add one, pointing Windows users at WSL — which a
// bundle whose whole promise is "no prerequisites" cannot ask for. Redis 7.2
// is the last BSD-3 line before the RSALv2/SSPL change, the same lineage
// Valkey forked from, so it is redistributable inside a BUSL-1.1 product and
// speaks the protocol platform/events already uses. It also has the commands
// the outbox relay actually needs, XAUTOCLAIM among them, which the older
// Windows Redis ports (stuck on 5.0) do not.
const busBinary = "redis-server"

// exeName is what an executable is called on disk.
func exeName(name string) string { return name + ".exe" }

// localTimezone reports UTC, because Windows does not record an IANA zone.
//
// It stores a Windows-specific identifier ("W. Europe Standard Time") and the
// mapping to the IANA name margince.yaml wants lives in CLDR data no stdlib
// call exposes. Shipping a copy of that table to guess at a value the user can
// see and correct in one line is not worth the drift, so the first run is
// created in UTC and the launcher's own start message points at margince.yaml,
// where the timezone is a documented field.
func localTimezone() string { return "UTC" }

// holdConsole keeps the window open long enough to read the failure above.
//
// "Start Margince.cmd" hands the launcher its own console window, and Windows
// destroys that window the moment the process exits. Without this, a failed
// start is a box that appears and vanishes: the one place the reason was
// printed is gone before anyone can read it, and every failure looks alike.
func holdConsole() {
	fmt.Fprint(os.Stderr, "\nPress Enter to close this window.")
	// A line, or an error because there is no console attached at all: either
	// way the answer is to stop waiting and let the window close. There is
	// nothing here worth reporting to someone already looking at the real
	// error.
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return
	}
}

// openBrowser is a convenience, never a requirement: the URL is printed by the
// caller, so a failure to launch a browser is not worth interrupting a working
// start for.
//
// rundll32 rather than `cmd /c start`: it takes the URL as a plain argument
// instead of handing it to a shell that would need it quoted correctly.
//
// It is addressed by absolute path, resolved from SystemRoot. Letting PATH
// decide would hand any writable directory ahead of System32 the choice of
// what this launches, and it launches with the user's own privileges at the
// end of a successful start.
func openBrowser(url string) {
	if err := exec.Command(systemBin("rundll32.exe"), "url.dll,FileProtocolHandler", url).Start(); err != nil {
		fmt.Printf("  (could not open your browser automatically — visit %s)\n\n", url)
	}
}

// systemBin is the absolute path to one of Windows' own executables.
//
// SystemRoot is set on every Windows installation; the literal fallback is for
// the case where something has stripped the environment, and is the path that
// variable holds on effectively every machine anyway.
func systemBin(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}
