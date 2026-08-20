// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package ownedfile carries the per-platform half of "this file is readable by
// its owner and nobody else".
//
// It exists as a platform package rather than beside its caller because the two
// implementations are OS plumbing — a syscall flag and a Win32 access-control
// list — and the composition layer owns no such thing. A credential file is
// where the promise matters, but the promise itself is technical.
package ownedfile
