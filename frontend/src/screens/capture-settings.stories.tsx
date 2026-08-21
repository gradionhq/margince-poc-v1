// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { CaptureSettingsCard } from "./capture-settings";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The posture toggle, in the three answers it can give a reader. The write gate
// is `capture_settings:update`, so the denied fixture names a seat that holds
// the read and not the write — `meRoute({})` would be an ADMIN with no object
// grants, which is a different principal and draws a different card.
function story(auto_enrich: boolean, allow: Parameters<typeof meRoute>[0]) {
  return () => {
    installFetchStub({
      "GET /me": meRoute(allow),
      "GET /capture/settings": () => jsonResponse({ auto_enrich }),
    });
    return (
      <StoryProviders>
        <CaptureSettingsCard />
      </StoryProviders>
    );
  };
}

const MANAGER = { capture_settings: ["read", "update"] } as const;
const READER = { capture_settings: ["read"] } as const;

const meta: Meta<typeof CaptureSettingsCard> = {
  title: "Settings/Admin settings/Capture/Capture posture",
  component: CaptureSettingsCard,
};
export default meta;
type Story = StoryObj<typeof CaptureSettingsCard>;

export const EnrichOn: Story = { render: story(true, MANAGER) };

export const EnrichOff: Story = { render: story(false, MANAGER) };

// The state the seeded demo cannot show: the switch keeps the setting readable,
// refuses the change, and says why — with the explanation tied to the control by
// `aria-describedby` rather than sitting beside it. This is the one
// accessibility-wired denial in the tree, so it is worth a picture.
export const CannotChange: Story = { render: story(true, READER) };

// The denial in dark, next to `CannotChange` for comparison. A refused switch
// says so twice: the sentence beneath it, and the track — which is desaturated
// rather than dimmed, because the on-state has to stay legible. Desaturation is
// the fragile half of that: the dark palette's live green sits about where the
// light palette's muted one does, so this is the story that shows whether a
// reader in dark can still tell a refused switch from an operable one before
// clicking it.
export const CannotChangeDark: Story = {
  globals: { theme: "dark" },
  render: story(true, READER),
};
