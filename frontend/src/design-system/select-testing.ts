// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { screen, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

/**
 * The ONE way a test drives a Margince `Select`.
 *
 * `userEvent.selectOptions` only works on a native `<select>`, and this control
 * is a button plus a portalled listbox — so a suite that wants a choice made has
 * to open the popup and click inside it. Every suite doing that by hand would
 * re-derive the same two steps and encode this component's internals in thirty
 * files; when the popup's markup changes, they would all break at once. Import
 * this instead:
 *
 * ```ts
 * await pickOption(user, screen.getByRole("combobox", { name: "Stage" }), "Won");
 * ```
 *
 * `optionLabel` is the label the reader sees, not the value: a test should say
 * what a person would click. Matched exactly by accessible name, so "Won" does
 * not also match "Won (renewal)" — pass a RegExp when a prefix is what you mean.
 *
 * **The control must be CLOSED when this is called, and one call is one attempt.**
 * The first thing it does is click the trigger, which toggles — so a second call,
 * or a `waitFor` retrying this one, closes the popup the previous attempt opened
 * and then looks for a listbox that is no longer there. A choice that is not
 * offered yet is not something to retry: open the control once, await the option
 * (`await screen.findByRole("option", { name })`) and click it.
 *
 * Throws if the popup does not open or the option is not in it, rather than
 * returning quietly: a pick that silently did nothing is how a test ends up
 * asserting the screen's unchanged initial state and passing.
 */
export async function pickOption(
  user: UserEvent,
  control: HTMLElement,
  optionLabel: string | RegExp,
): Promise<void> {
  await user.click(control);
  const listbox = screen.getByRole("listbox");
  await user.click(within(listbox).getByRole("option", { name: optionLabel }));
}
