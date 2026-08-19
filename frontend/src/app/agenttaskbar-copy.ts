// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { MarginceCoreState } from "../design-system/margince-core";

/**
 * The one thing about the taskbar's copy that is not a translated string:
 * which states its collapsed line renders in the "working" visual treatment.
 *
 * Everything the bar SAYS lives in the i18n catalogs (`taskbar.*` and
 * `taskbar.task.*` in `en.ts`, mirrored in `de.ts` and `vi.ts`) — this bar
 * renders on every screen for every reader, so its copy is translated like
 * any other product surface.
 */

/** The states whose line above describes work still running. */
export const RUNNING: ReadonlySet<MarginceCoreState> = new Set([
  "ingesting",
  "reasoning",
  "drafting",
]);
