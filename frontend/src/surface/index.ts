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
export { useCan, useCanWrite } from "../app/capability";
export { formatDateTime } from "../format/format";
export { useLocale, useT } from "../i18n";
