// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The design-system surface a unit screen may build from.
//
// A re-export file rather than a deep import into atoms.tsx, because this list
// IS the promise: what is named here a unit may use and the core may not break
// casually, and what is not named here is core-internal however exported it
// happens to be. It is this side's `//margince:extension-surface` — the Go
// tier gets its boundary from the compiler and a marker test, and a bundler
// gives none at all, so the surface has to be a declared thing before a gate
// can hold anyone to it.
//
// Widening it is a reviewed act. Adding a name here says the core will keep
// rendering it for units it did not write, which is a different promise from
// "this component exists".
export {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
