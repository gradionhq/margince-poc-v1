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
