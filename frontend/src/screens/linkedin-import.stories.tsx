// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { LinkedInImportCard } from "./linkedin-import";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// LinkedInImportCard stories for the fe-uat render gate. The card is a personal
// surface — it reads /me/linkedin-account and writes the caller's own row — so
// no grant gates it and the stories differ only in what the account read says.

function cardStory(account: { profile_url?: string; connected?: boolean }) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture()),
      "GET /me/linkedin-account": () => jsonResponse(account),
    });
    return (
      <StoryProviders>
        <LinkedInImportCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof LinkedInImportCard> = {
  title: "Settings/You/Connections/LinkedIn import",
  component: LinkedInImportCard,
};
export default meta;
type Story = StoryObj<typeof LinkedInImportCard>;

// The profile the onboarding act recorded, shown back so it can be corrected.
export const KnownProfile: Story = {
  render: cardStory({
    profile_url: "https://www.linkedin.com/in/lars-brandt",
    connected: true,
  }),
};

// Nothing recorded yet: the field is empty and the note says the account is not
// connected, which is a different claim from "we have no URL".
export const NoProfileYet: Story = {
  render: cardStory({ profile_url: "", connected: false }),
};

export const AccountReadFailed: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture()),
      "GET /me/linkedin-account": () =>
        jsonResponse(
          {
            title: "Internal Server Error",
            detail: "the profile store is down",
          },
          500,
        ),
    });
    return (
      <StoryProviders>
        <LinkedInImportCard />
      </StoryProviders>
    );
  },
};
