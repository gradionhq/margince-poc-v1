// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ClientSurfaceScreen } from "./client";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The extension client surface: rail-less, its own dark "Back to Margince" bar
// instead of the shell, a sender lookup, and the isolation footer that states
// the one thing a reader inside somebody else's inbox needs to know — this
// surface talks only to their OWN workspace API.
//
// The lookup is a mutation with no auto-run, so every state past the resting one
// is reached by driving the control in play() rather than by seeding a cache.
// Its single read is GET /search, filtered to person hits.
const SEARCH = "GET /search";

const person = {
  id: "p-1042",
  type: "person",
  title: "Bettina Krause",
  snippet: "Head of Fleet · Brandt Automotive GmbH",
};

// A company hit in the same payload: the surface is a SENDER lookup, so it keeps
// only the people. Leaving the organization in the fixture is what proves the
// filter, rather than a payload that could not have shown the bug.
const organization = {
  id: "o-1",
  type: "organization",
  title: "Brandt Automotive GmbH",
  snippet: "Automotive · Munich",
};

function searchPage(hits: readonly unknown[]) {
  return () =>
    jsonResponse({ data: hits, page: { next_cursor: null, has_more: false } });
}

async function lookUpSender({ canvasElement }: { canvasElement: HTMLElement }) {
  const canvas = within(canvasElement);
  const user = userEvent.setup();
  await user.type(canvas.getByLabelText("Sender"), "bettina@brandt.example");
  await user.click(canvas.getByRole("button", { name: "Look up" }));
}

const meta: Meta<typeof ClientSurfaceScreen> = {
  title: "Screens/Client surface",
  component: ClientSurfaceScreen,
};
export default meta;
type Story = StoryObj<typeof ClientSurfaceScreen>;

function story(routes: RouteMap) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <ClientSurfaceScreen />
      </StoryProviders>
    );
  };
}

// Nothing looked up yet: the chrome, the lookup control with its submit
// disabled on an empty field, and the isolation footer.
export const Resting: Story = { render: story({}) };

// A recognized sender: the mini record, with the way through to the full 360.
export const RecognizedSender: Story = {
  render: story({ [SEARCH]: searchPage([person, organization]) }),
  play: lookUpSender,
};

// The honest unknown-sender state. A search that matched nobody is not an error
// and does not pretend to be one — it offers the one action that makes sense
// from inside an inbox, capturing the sender as a lead.
export const UnknownSender: Story = {
  render: story({ [SEARCH]: searchPage([]) }),
  play: lookUpSender,
};

// Dark is the theme this surface is most often SEEN in — it lives beside a mail
// client, not inside the app shell — and its chrome bar is painted --bgRail,
// which in dark sits a hair off --bgPage. If that step collapses, the surface
// loses the one band that says whose product this is and where "back" lives.
// The mini-record card and the isolation badge in the footer are the other two
// grounds on trial.
export const RecognizedSenderDark: Story = {
  globals: { theme: "dark" },
  render: story({ [SEARCH]: searchPage([person, organization]) }),
  play: lookUpSender,
};

// The read itself failed. The surface says so in place instead of leaving the
// field looking as though nobody had asked — an unknown sender and an unreachable
// workspace must not read the same.
export const LookupFailed: Story = {
  render: story({
    [SEARCH]: () =>
      jsonResponse(
        {
          title: "Search is unavailable",
          status: 503,
          detail: "The search index is being rebuilt. Try again shortly.",
        },
        503,
      ),
  }),
  play: lookUpSender,
};
