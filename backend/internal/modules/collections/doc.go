// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package collections owns the organizational surfaces over the four
// core record types: lists (static sets and dynamic segments) and tags.
// Membership and tag application name row-scoped records, so every
// client-supplied entity reference passes the visibility probe (H1) —
// a list cannot become a side channel onto rows the caller cannot read.
//
// List and tag mutations are audited but carry no domain event: the
// events.md closed catalog defines no list.*/tag.* types, and §5.3c
// ratifies list/tag mutations as audit-only for V1 — so these ride the
// same audit-only lane as pipeline config.
//
// Tables owned: list, list_member, tag, taggable. Imports shared +
// platform only; never a sibling module.
package collections
