// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";

// The import surface's wire shapes, named once so the card and its mapping
// table cannot drift apart about what a profile or a report is.
export type ImportObject = components["schemas"]["ImportObject"];
export type ImportProfile = components["schemas"]["ImportSourceProfile"];
export type ImportColumn = components["schemas"]["ImportColumn"];
export type ImportRun = components["schemas"]["ImportRun"];
export type ImportReport = components["schemas"]["ImportRunReport"];

// DONT_IMPORT is the mapping table's resting choice: a column nobody has
// assigned a destination to is LEFT OUT, never guessed into a field. It is a
// sentinel for the picker only — the request carries the mapped columns and
// nothing else.
export const DONT_IMPORT = "";

// identifyingFieldFor names the field a run recognizes a row by, per object.
// The same defaults the server applies, stated here so the screen can say which
// column will identify a row BEFORE the server has to refuse a mapping that
// identifies none.
export function identifyingFieldFor(object: ImportObject): string {
  return object === "lead" ? "email" : "display_name";
}
