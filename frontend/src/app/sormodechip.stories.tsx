// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { SorModeChip } from "./sormodechip";

function story(mode: "native" | "overlay") {
  return () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          user: {
            id: "u1",
            email: "ada@acme.test",
            display_name: "Ada",
          },
          roles: ["admin"],
          teams: [],
          system_of_record: { mode },
        }),
    });
    return (
      <StoryProviders>
        <SorModeChip />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof SorModeChip> = {
  title: "app/sor-mode-chip",
  component: SorModeChip,
};
export default meta;
type Story = StoryObj<typeof SorModeChip>;
export const Overlay: Story = { render: story("overlay") };
export const Native: Story = { render: story("native") };
