// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Copy, locale and capability — the app-level surface a unit screen needs to
// render honestly rather than merely to render.
//
// The capability hooks are surface rather than an internal detail on purpose:
// a screen that shows a control the caller may not use has told them something
// false, and the alternative to exporting these is every unit inventing its
// own read of /me. They are UX honesty and never enforcement — the server
// refuses independently (`extensionTool.Handle`), which is what makes it safe
// to hand a unit the same hooks the core screens use.
//
// `useT` reads the merged catalogue, so a unit's own copy resolves through the
// same lookup as core's rather than through a second mechanism.
import { type ExtensionMessageKey, useT as useCoreT } from "../i18n";
import type { MessageKey } from "../i18n/en";

export { useCan, useCanWrite } from "../app/capability";
export { formatDateTime } from "../format/format";
// LocaleProvider is here for the TESTS: a unit's screen calls useT, so a test
// that renders it without the provider the app mounts around it renders
// nothing useful. Exporting the provider is what lets a unit test its own
// screen the way the app actually runs it.
export { LocaleProvider, useLocale } from "../i18n";

/**
 * The catalogue lookup, widened to a unit's own keys.
 *
 * Core's `useT` is narrow — its key is a closed union, so a typo in a core key
 * is a compile error, and `ReturnType<typeof useT>` is the parameter type some
 * two dozen core helpers take a translator as. Widening it THERE would make
 * every core-only test fake stop being assignable, for a capability no core
 * helper has any use for. So the widening lives here, on the surface a unit
 * imports, which is the only place that needs it.
 *
 * A unit's half cannot be a closed union: this file cannot enumerate what an
 * installation enabled. The real rule is checked by the generator, which
 * refuses any key a unit ships that is not namespaced to that unit.
 */
export function useT(): (
  key: MessageKey | ExtensionMessageKey,
  params?: Record<string, string | number>,
) => string {
  return useCoreT() as (
    key: MessageKey | ExtensionMessageKey,
    params?: Record<string, string | number>,
  ) => string;
}
