// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { RecordContextPanel } from "./context";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

const populated = () =>
  jsonResponse({
    anchor: { type: "person", id: "p1" },
    sections: [
      {
        name: "Recent touches",
        items: [
          {
            ref: { type: "deal", id: "d1" },
            summary: "Renewal discussion",
            evidence: [{ snippet: "…renewal…", source: "email:msg-1" }],
          },
        ],
      },
      {
        name: "Related people",
        items: [{ ref: { type: "person", id: "p2" }, summary: "Dana Buyer" }],
      },
    ],
  });

const meta: Meta<typeof RecordContextPanel> = {
  title: "Records/Record context",
  component: RecordContextPanel,
};
export default meta;
type Story = StoryObj<typeof RecordContextPanel>;

export const Populated: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /records/person/p1/context": populated,
    });
    return (
      <StoryProviders>
        <RecordContextPanel entityType="person" id="p1" />
      </StoryProviders>
    );
  },
};

export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /records/person/p1/context": () =>
        jsonResponse({ anchor: { type: "person", id: "p1" }, sections: [] }),
    });
    return (
      <StoryProviders>
        <RecordContextPanel entityType="person" id="p1" />
      </StoryProviders>
    );
  },
};
