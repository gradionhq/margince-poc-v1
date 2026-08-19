// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// The files a captured record carried, and the ones it could not.
//
// DEFINED on the published surface (backend/pkg/extension) and aliased here, the
// arrangement ports/jurisdiction already uses for the pack contract: a file
// handed over by an extension unit and one handed over by a core connector are
// the same type, so the bounded/renamed/content-typed guarantee cannot be
// re-decided per producer. Core call sites keep the names they had.

import "github.com/gradionhq/margince/backend/pkg/extension"

// Part is one file a captured record carried — the published extension.InboundFile.
type Part = extension.InboundFile

// PartDrop is how many files one bound refused, and why — the published
// extension.FileDrop.
type PartDrop = extension.FileDrop
