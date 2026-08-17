// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useEffect } from "react";
import { ChangePasswordCard } from "./passwordcard";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

/**
 * Changing the password you sign in with, from Settings → Account.
 *
 * The card had no story, which is most of why it shipped with three inputs
 * carrying no CSS class at all: `.input` never applied, Tailwind's preflight
 * had already stripped the browser's own chrome, and the result was three
 * borderless, backgroundless, half-height fields on the one screen where a
 * signed-in person rotates their own credential. Nothing failed. Nobody looked.
 *
 * So the states below are the ones worth looking AT: a refusal that has to read
 * as a refusal, a success that has to be distinguishable from one, and the two
 * field-level rules the form states before the server has to.
 */
const meta: Meta<typeof ChangePasswordCard> = {
  title: "Settings/You/Account/Change password",
  component: ChangePasswordCard,
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj<typeof ChangePasswordCard>;

/**
 * Types into the card the way a reader would, so a story can show a state the
 * form only reaches through its own inputs.
 *
 * Native setters rather than assigning `.value`: React tracks the last value it
 * wrote to a controlled input and swallows a change event whose value it
 * believes it already has, so a plain assignment updates the DOM and never the
 * component.
 */
function useTypedFields(values: Readonly<Record<string, string>>) {
  useEffect(() => {
    const setValue = Object.getOwnPropertyDescriptor(
      globalThis.HTMLInputElement.prototype,
      "value",
    )?.set;
    for (const [name, value] of Object.entries(values)) {
      const input = document.querySelector<HTMLInputElement>(
        `input[name="${name}"]`,
      );
      if (!input || !setValue) continue;
      setValue.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }
  }, [values]);
}

function Typed({ values }: Readonly<{ values: Record<string, string> }>) {
  useTypedFields(values);
  return <ChangePasswordCard />;
}

/** Nothing typed: the rule stated once, and a submit that is not yet live. */
export const Empty: Story = {
  render: () => (
    <StoryProviders>
      <ChangePasswordCard />
    </StoryProviders>
  ),
};

/**
 * The new password is under the floor.
 *
 * The refusal reads in the danger tone with a danger boundary on the field it
 * belongs to, and the grey rule it replaces is gone — a second copy of the same
 * sentence underneath would be noise. Both used to be the same `--textMeta`
 * grey, which is what made a broken rule and a satisfied one look alike.
 */
export const TooShort: Story = {
  name: "New password too short",
  render: () => (
    <StoryProviders>
      <Typed
        values={{
          "current-password": "the old one, which was long enough",
          "new-password": "short",
        }}
      />
    </StoryProviders>
  ),
};

/**
 * The confirmation disagrees.
 *
 * Two refusals can be on screen at once, and the second one keeps its rule
 * beside it — what the field wants is still true while it is wrong.
 */
export const Mismatch: Story = {
  name: "Confirmation does not match",
  render: () => (
    <StoryProviders>
      <Typed
        values={{
          "current-password": "the old one, which was long enough",
          "new-password": "a long enough new password",
          "confirm-password": "a different long enough password",
        }}
      />
    </StoryProviders>
  ),
};

/**
 * The server refused it — the current password was wrong.
 *
 * A `Callout` in the danger tone, not a grey line: this used to render in
 * `--textMeta`, one element below a SUCCESS line in the same grey, so the only
 * thing telling a reader which had happened was reading the sentence.
 */
export const Refused: Story = {
  name: "Server refused the change",
  render: () => {
    installFetchStub({
      // The house problem shape, at the status the server answers a wrong
      // current password with — the card reads `detail` through
      // `problemMessageOf`, so this is what a reader actually sees.
      "POST /auth/change-password": () =>
        jsonResponse(
          {
            type: "about:blank",
            title: "Forbidden",
            status: 403,
            detail: "That is not your current password.",
          },
          403,
        ),
    });
    return (
      <StoryProviders>
        <Typed
          values={{
            "current-password": "not actually the current one",
            "new-password": "a long enough new password",
            "confirm-password": "a long enough new password",
          }}
        />
      </StoryProviders>
    );
  },
};

/**
 * It landed — and the card says so in the success tone, having already said,
 * before the button was pressed, that the change ends every session including
 * this one. A person who is not told that reads the sign-in screen that follows
 * as being kicked out.
 */
export const Changed: Story = {
  render: () => {
    installFetchStub({
      "POST /auth/change-password": () => jsonResponse({}),
      "GET /me": () => jsonResponse({}),
    });
    return (
      <StoryProviders>
        <Typed
          values={{
            "current-password": "the old one, which was long enough",
            "new-password": "a long enough new password",
            "confirm-password": "a long enough new password",
          }}
        />
      </StoryProviders>
    );
  },
};
