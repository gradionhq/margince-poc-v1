// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ContactsScreen, PersonScreen } from "./people";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ContactsScreen (list) and PersonScreen (360 Overview) both read through
// the api client on mount — fixtures mirror people.test.tsx's `anna` +
// dormant-strength default (the Overview tab fires the strength GET
// unconditionally).
const meta: Meta = {
  title: "Screens/People",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const anna = {
  id: "p-1",
  workspace_id: "w-1",
  full_name: "Anna Weber",
  title: "Head of Procurement",
  emails: [{ id: "e-1", email: "anna.weber@brandt.example", is_primary: true }],
  captured_by: "connector:gmail",
  source: "gmail",
  version: 1,
};

const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

export const ContactsList: Story = {
  render: () => {
    installFetchStub({
      "GET /people": () =>
        jsonResponse({
          data: [anna],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

export const PersonOverview: Story = {
  render: () => {
    installFetchStub({
      "GET /people/p-1": () => jsonResponse(anna),
      "GET /people/p-1/strength": () => jsonResponse(dormantStrength),
      "GET /activities": () => jsonResponse({ data: [] }),
      "GET /records/person/p-1/context": () =>
        jsonResponse({ anchor: { type: "person", id: "p-1" }, sections: [] }),
    });
    return (
      <StoryProviders>
        <PersonScreen id="p-1" />
      </StoryProviders>
    );
  },
};
