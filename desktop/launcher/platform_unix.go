// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The POSIX half, not the macOS one: the socket, the signals and the path
// shapes here are the same on any unix. The constraint is explicit because only
// GOOS filename suffixes are implicit, and "unix" is not one of them.
//go:build !windows

package main

import (
	"os"
	"strings"
	"syscall"
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

// openNoFollow refuses to create a secret through a symbolic link. O_EXCL
// already refuses any existing final component here, so this is reinforcement
// rather than a guarantee of its own — see writeNewSecret, and the same
// constant in platform_windows.go for what that platform can promise.
const openNoFollow = syscall.O_NOFOLLOW

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
