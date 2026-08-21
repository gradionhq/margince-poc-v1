// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { meFixture } from "../app/mefixture";
import { LinkedInImportCard } from "./linkedin-import";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// LinkedInImportCard stories for the fe-uat render gate. The card is a personal
// surface — it reads /me/linkedin-account and writes the caller's own row — so
// no grant gates it and the stories differ only in what the account read says.
//
// The card is two SettingRows now: the profile URL as a value with an Edit verb,
// and the file picker. The edit FIELD lives in a Modal, which renders into the
// document body rather than into the story canvas — so the story that shows it
// scopes its `play` past `canvasElement`.
//
// ONE description above them, and only one. A second full-width paragraph used
// to stand between it and the first row — where to get the file, and what
// happens to it — and both halves of that belong to the import row rather than
// to the card, so they read in that row's description now. What to check here is
// that nothing on the card is prose before the list.

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

/** Open the profile row's Edit dialog. */
async function openProfileEditor(canvasElement: HTMLElement) {
  const body = within(canvasElement.ownerDocument.body);
  await userEvent.click(await body.findByRole("button", { name: "Edit" }));
  await body.findByRole("textbox", { name: /LinkedIn profile URL/ });
}

// The profile the onboarding act recorded, shown back so it can be corrected.
// This is the row shape the whole tab is built from: what it is on the left,
// what it is set to at the same x as every other answer, and the verb beside it.
export const KnownProfile: Story = {
  render: cardStory({
    profile_url: "https://www.linkedin.com/in/lars-brandt",
    connected: true,
  }),
};

// Nothing recorded yet: the row says "Not recorded yet" rather than leaving the
// value blank, which is a different claim from "this member has no profile". The
// description beside it says what RECORDING one does — it used to open "Not
// connected yet", which is the same fact the value already carries, stated twice
// on one row in two different words.
export const NoProfileYet: Story = {
  render: cardStory({ profile_url: "", connected: false }),
};

// The field itself, in the dialog the Edit verb opens. Save is refused until the
// URL actually changes — the dialog opens on the stored value, so an unchanged
// one is a write with nothing in it.
export const EditingTheProfile: Story = {
  render: cardStory({
    profile_url: "https://www.linkedin.com/in/lars-brandt",
    connected: true,
  }),
  play: async ({ canvasElement }) => {
    await openProfileEditor(canvasElement);
  },
};

// The same dialog in dark. Everything in it now comes from the design system —
// Modal, Field, TextInput, both Buttons — so what to check is the dialog's own
// ground against the card behind it, and that the refused Save still reads as
// refused rather than as absent.
export const EditingTheProfileDark: Story = {
  globals: { theme: "dark" },
  render: cardStory({
    profile_url: "https://www.linkedin.com/in/lars-brandt",
    connected: true,
  }),
  play: async ({ canvasElement }) => {
    await openProfileEditor(canvasElement);
  },
};

// What the import PRODUCED, which is the one thing on this card that is not a
// row: four counts under the list, reporting an act that has already happened.
// Skipped rows are in the picture on purpose — a file half-ignored under a
// success message is worse than a refusal, and this is the story that says
// whether the skipped count reads as a finding or as a fourth statistic.
export const ImportSummary: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture()),
      "GET /me/linkedin-account": () =>
        jsonResponse({
          profile_url: "https://www.linkedin.com/in/lars-brandt",
          connected: true,
        }),
      "POST /me/linkedin-connections": () =>
        jsonResponse({
          rows: 4120,
          imported: 4108,
          skipped: 12,
          confirmed: 37,
          suggested: 214,
        }),
    });
    return (
      <StoryProviders>
        <LinkedInImportCard />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const body = within(canvasElement.ownerDocument.body);
    // The input is visually hidden but focusable and real — the label beside it
    // is what a reader presses. Waiting on the RESULT rather than on a duration:
    // a story that slept would pass on a fast machine and screenshot a spinner
    // on a slow one.
    await userEvent.upload(
      body.getByTestId("linkedin-import-file"),
      new File(["First Name,Last Name\n"], "Connections.csv", {
        type: "text/csv",
      }),
    );
    await body.findByTestId("linkedin-import-result");
  },
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

// The card in dark. One control on it is still hand-rolled: a native file input
// carries a browser-drawn button whose label cannot be set, so it is hidden and
// a real <label> — `.li-import-button` in linkedin-import.css — stands in for
// it. `FileDropzone` is the design system's file control and does not fit here:
// it is a full-width dashed drop zone with its own `Field` label, so it cannot
// sit in a row's right column, and it passes no `accept` through to the input,
// which this card needs because LinkedIn's export archive holds a dozen CSVs.
// That hand-rolled control fills itself with --bgElevated, the same token the
// card under it uses, and its only separation from that ground is one
// --borderSubtle hairline: what to check is whether it still reads as a
// pressable thing rather than as a sentence, with the ghost Edit button one row
// above it as the design system's own comparison standing in the same picture.
export const KnownProfileDark: Story = {
  globals: { theme: "dark" },
  render: cardStory({
    profile_url: "https://www.linkedin.com/in/lars-brandt",
    connected: true,
  }),
};
