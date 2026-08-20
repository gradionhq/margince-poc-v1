// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"errors"
	"fmt"
	"os/exec"
)

// statusDLLNotFound is the NT status Windows reports when a process cannot
// start because a DLL it imports is missing. It arrives as the exit code, with
// no output at all — the process never reached main.
const statusDLLNotFound = 0xC0000135

// explainStartFailure turns an exit status that names nothing into a sentence
// that names the cause.
//
// 0xC0000135 is the one worth translating. A user sees it when a shipped binary
// imports a DLL the folder does not carry, and the raw form —
//
//	initdb failed: exit status 0xc0000135
//
// reads like a corrupt download rather than a missing file. It has one cause in
// this bundle and one remedy, so the message says both. The build now refuses to
// produce a folder with that gap (Test-NativeDependencies), which makes this the
// second line of defence rather than the first.
func explainStartFailure(what string, err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || uint32(exitErr.ExitCode()) != statusDLLNotFound {
		return err
	}
	return fmt.Errorf(
		"%s could not start: a system library it needs is missing (0xC0000135).\n"+
			"This installation is incomplete — download it again.\n"+
			"If it keeps happening, install the Microsoft Visual C++ x64 runtime:\n"+
			"  https://aka.ms/vs/17/release/vc_redist.x64.exe\n"+
			"  (original error: %w)",
		what, err)
}
