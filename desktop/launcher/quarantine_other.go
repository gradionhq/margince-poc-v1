// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Everything that is not macOS. Quarantine is a macOS mechanism: Windows warns
// through SmartScreen instead, once per binary it does not recognise, and
// nothing an unprivileged program does from inside the bundle changes that.
//
// This is also what lets the package type-check on Linux, which is where the
// lint gate runs — a symbol defined only in a darwin-tagged file is undefined
// there, and the gate reports a module that does not compile rather than a
// module that is clean.
//go:build !darwin

package main

func clearQuarantine(_ layout) {}
