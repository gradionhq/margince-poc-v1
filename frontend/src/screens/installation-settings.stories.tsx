// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { InstallationSettingsCard } from "./installation-settings";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

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

const MANAGER = { installation_settings: ["read", "update"] } as const;
const READER = { installation_settings: ["read"] } as const;

const meta: Meta<typeof InstallationSettingsCard> = {
  title: "Settings/Organization/General/Installation",
  component: InstallationSettingsCard,
};
export default meta;
type Story = StoryObj<typeof InstallationSettingsCard>;

export const Editable: Story = { render: story(SETTINGS, MANAGER) };

// The base currency stops being changeable the moment a deal freezes a rate
// against it, which is a fact about the DATA rather than about the reader — so
// it disables that one field and leaves the rest alone.
export const BaseCurrencyLocked: Story = {
  render: story({ ...SETTINGS, base_currency_locked: true }, MANAGER),
};

// A permission, not a lock: every field goes inert together and the reason is
// attached to each rather than floating under the card.
export const ReadOnly: Story = { render: story(SETTINGS, READER) };

// The form in dark, and the locked fixture rather than the plain one because it
// is the only story where a disabled field sits beside two live ones. That is the
// pairing dark gets wrong: a disabled TextInput says so with a fill and an ink a
// step off the enabled field's, and one step is exactly what a darker palette
// compresses. Three more things are on trial with it — the Field labels, the
// hints under them (which here carry a lock REASON, a sentence a reader has to be
// able to read), and the primary Save in the panel head.
export const BaseCurrencyLockedDark: Story = {
  globals: { theme: "dark" },
  render: story({ ...SETTINGS, base_currency_locked: true }, MANAGER),
};

// The form at 390px. The fields are a one-column form-stack and stack at any
// width, so what narrow actually tests here is the DISTANCE this card's own
// source claims: the Save rides in the panel's action band "directly under the
// last field it commits". Every field here carries a hint, and at 390px each hint
// is three or four lines rather than one — so the interval between the last field
// and the button that writes it roughly triples, and "directly under" becomes
// "under a paragraph". Whether that still reads as one form with one commit is
// the judgement this story exists to put in front of somebody.
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
