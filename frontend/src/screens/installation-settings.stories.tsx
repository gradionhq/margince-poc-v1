// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { InstallationSettingsCard } from "./installation-settings";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Settings → Admin settings → General → Installation. Two surfaces in one card:
// three ROWS that read what the installation is set to, and the one dialog that
// edits all three together (the server takes one sparse PATCH, so there is one
// save). The stories worth looking at are therefore split between the two —
// what the rows say, and what the form under them looks like once it is open.

const SETTINGS = {
  name: "Brandt Automotive GmbH",
  timezone: "Europe/Berlin",
  base_currency: "EUR",
  base_currency_locked: false,
};

function story(
  settings: Record<string, unknown>,
  allow: Parameters<typeof meRoute>[0],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /installation/settings": () => jsonResponse(settings),
    });
    return (
      <StoryProviders>
        <InstallationSettingsCard />
      </StoryProviders>
    );
  };
}

// Opens the dialog the way a reader does — from one row's own Edit verb, which
// is also what puts focus on that row's field.
async function openFrom(canvasElement: HTMLElement, fact: RegExp) {
  const canvas = within(canvasElement);
  await userEvent.click(await canvas.findByRole("button", { name: fact }));
}

const MANAGER = { installation_settings: ["read", "update"] } as const;
const READER = { installation_settings: ["read"] } as const;

const meta: Meta<typeof InstallationSettingsCard> = {
  title: "Settings/Admin settings/General/Installation",
  component: InstallationSettingsCard,
};
export default meta;
type Story = StoryObj<typeof InstallationSettingsCard>;

// The card at rest: three answers in one column, each with the verb that
// changes it. This is the reading the row language exists for — an operator
// auditing the installation travels one column instead of parsing a form.
export const Editable: Story = { render: story(SETTINGS, MANAGER) };

// The form the rows lead to, opened from the name row, so focus is in the name
// field. Three fields, one heading for the currency rule, one Save.
export const ProfileDialog: Story = {
  render: story(SETTINGS, MANAGER),
  play: async ({ canvasElement }) => {
    await openFrom(canvasElement, /edit organization name/i);
  },
};

// The base currency stops being changeable the moment a deal freezes a rate
// against it, which is a fact about the DATA rather than about the reader — so
// the currency row carries the server's own reason and the field inside the
// dialog is the only inert one.
export const BaseCurrencyLocked: Story = {
  render: story({ ...SETTINGS, base_currency_locked: true }, MANAGER),
};

// A permission, not a lock. Every Edit verb is refused together and the reason
// is stated ONCE, with each refused button pointing at it — printing it beside
// three buttons would say one fact three times.
export const ReadOnly: Story = { render: story(SETTINGS, READER) };

// The locked dialog in dark, because that is the pairing dark gets wrong: a
// disabled TextInput says so with a fill and an ink a step off the enabled
// field's, and one step is exactly what a darker palette compresses. Three more
// things are on trial with it — the Field labels, the hints under them (which
// here carry a lock REASON, a sentence a reader has to be able to read), and the
// primary Save in the dialog's action row.
export const BaseCurrencyLockedDark: Story = {
  globals: { theme: "dark" },
  render: story({ ...SETTINGS, base_currency_locked: true }, MANAGER),
  play: async ({ canvasElement }) => {
    await openFrom(canvasElement, /edit base currency/i);
  },
};

// The rows in dark. What is on trial is the hairline between them and the muted
// value column against the card ground: both are color-mix()es that follow the
// dark accent lift, and a value that goes as quiet as its own description stops
// reading as the answer.
export const EditableDark: Story = {
  globals: { theme: "dark" },
  render: story(SETTINGS, MANAGER),
};

// The card at 390px. The row gives up its two columns before it gives up the
// reading, so label, description, value and verb stack — and what narrow tests
// here is whether the VALUE still reads as this row's answer once it is no
// longer aligned in a column of its own, with a three-line hint above it.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const EditablePhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story(SETTINGS, MANAGER),
};
