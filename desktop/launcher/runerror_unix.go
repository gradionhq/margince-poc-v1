// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The POSIX side of the pair. The constraint is explicit because only GOOS
// filename suffixes are implicit, and "unix" is not one of them.
//go:build !windows

package main

// explainStartFailure returns the error unchanged here.
//
// The Windows half translates one exit status that names nothing —
// STATUS_DLL_NOT_FOUND — into the file it is really about. A unix loader
// already says which library it could not find, so there is nothing to add.
func explainStartFailure(_ string, err error) error { return err }
