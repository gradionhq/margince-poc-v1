// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LogActivity } from "./logactivity";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The log-a-note/task form embedded in every 360 (person/company/deal/lead).
// It reads GET /me only to gate itself on overlay mode (hidden there — its
// POST /activities writes a mirrored record, unsupported_by_sor); the form
// itself never fetches.
function admin() {
  return () =>
    jsonResponse({
      user: { id: "u1", email: "ada@acme.test", display_name: "Ada" },
      roles: ["admin"],
      teams: [],
    });
}

const meta: Meta<typeof LogActivity> = {
  title: "screens/log-activity",
  component: LogActivity,
};
export default meta;
type Story = StoryObj<typeof LogActivity>;

export const Native: Story = {
  render: () => {
    installFetchStub({ "GET /me": admin() });
    return (
      <StoryProviders>
        <LogActivity entityType="person" entityId="p1" />
      </StoryProviders>
    );
  },
};

// Overlay mode renders nothing — logging writes a mirrored record's native
// table directly, which the incumbent write-back seam does not shadow.
export const HiddenInOverlay: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          user: { id: "u1", email: "ada@acme.test", display_name: "Ada" },
          roles: ["admin"],
          teams: [],
          system_of_record: { mode: "overlay" },
        }),
    });
    return (
      <StoryProviders>
        <LogActivity entityType="person" entityId="p1" />
      </StoryProviders>
    );
  },
};
