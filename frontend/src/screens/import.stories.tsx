// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ImportCard } from "./import";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Bringing a customer's file in. On the settings page the import is one row —
// an import is an ACT, not an answer this installation holds — and the wizard
// that performs it is the dialog behind the row's verb. So most of what this
// card can show needs the dialog opened, which is what the `play` below does; a
// story without one screenshots the row and nothing else.
//
// The flow past the wizard's first step needs a real file drop, which a story
// cannot perform. What is catalogued here is the row, the two answers the card
// gives before anybody chooses a file, and the one later state a story CAN
// reach: the run an earlier visit left parked.
function story(allow: Parameters<typeof meRoute>[0]) {
  return () => {
    installFetchStub({ "GET /me": meRoute(allow) });
    return (
      <StoryProviders>
        <ImportCard />
      </StoryProviders>
    );
  };
}

// Opening the wizard. The verb is the card's only button, and it is in the
// canvas — the dialog it opens is portalled to the body, past `canvasElement`.
const openWizard: NonNullable<Story["play"]> = async ({ canvasElement }) => {
  await userEvent
    .setup()
    .click(within(canvasElement).getByRole("button", { name: /Start/ }));
};

const OPERATOR = { import_run: ["create", "read", "update"] } as const;

const meta: Meta<typeof ImportCard> = {
  title: "Settings/Admin settings/Maintenance/Import",
  component: ImportCard,
};
export default meta;
type Story = StoryObj<typeof ImportCard>;

// The card at rest: one row, naming the act on the left and carrying the verb on
// the right, at the same x as every other row on the page. Nothing about a file
// is on screen yet, because nothing about a file is a setting.
export const TheRow: Story = { render: story(OPERATOR) };

// Maintenance opens on the admin role OR an embedding-reindex read, so a seat
// holding only the latter reaches this page. It is told the import exists and is
// not theirs to run — an absent card would say the installation cannot import.
export const Withheld: Story = {
  render: story({ embedding_reindex: ["read"] }),
};

// The wizard's first step: what the rows are, and the file to read them from.
export const ChoosingAFile: Story = {
  render: story(OPERATOR),
  play: openWizard,
};

// The first step in dark, and the reason it is dark rather than narrow: this
// card's own sheet opens by declaring that every quiet line on it reads --textMeta
// and not --textMuted, because --textMuted measures 1.54:1 here while --textMeta
// is the canonical AA small-text role — a rule written against the LIGHT palette
// and, until this story, never looked at once both tokens re-resolved. The lines
// under test are the object hint and the file-format sentence, sitting beside a
// SegmentedControl whose selected segment is the loudest thing in the dialog.
//
// A narrow variant would prove less: the flow past the first step needs a real
// file drop, so the wide mapping table and its .import__scroll box — the parts
// that have a width problem to have — are not reachable from a story at all.
export const ChoosingAFileDark: Story = {
  globals: { theme: "dark" },
  render: story(OPERATOR),
  play: openWizard,
};

// The one state past the first step a story CAN reach, and the reason it can is
// the point of the state: it needs no file, only the run id an earlier visit
// left in storage and the two reads that answer for a run by id. This is what a
// reader sees coming back from the Leads list after editing the one row they do
// not want reversed — the outcome, the notice saying where it came from, and the
// undo that used to vanish the moment they navigated away.
//
// It carries no `play`, and that is the state under test: an operator who does
// not know their last import stopped half-way cannot finish it, so the wizard
// opens ITSELF for a run it picked up rather than waiting behind a verb.
//
// The reference it plants is self-clearing: every other story routes these reads
// to the empty fallback, which is not a run in a state worth reopening, so the
// card forgets it and shows its resting row.
export const PickedUpFromEarlier: Story = {
  render: () => {
    globalThis.localStorage.setItem("margince.import.run", "019ff-run");
    installFetchStub({
      "GET /me": meRoute(OPERATOR),
      "GET /imports/019ff-run/report": () =>
        jsonResponse({
          run_id: "019ff-run",
          status: "complete",
          rows_read: 4,
          disposition: { created: 3, updated: 0, unchanged: 0, skipped: 1 },
          issues: [],
          source_key_used: "Email",
        }),
      "GET /imports/019ff-run": () =>
        jsonResponse({
          id: "019ff-run",
          connector: "csv",
          object: "lead",
          status: "complete",
          checkpoint: 4,
          source: "import_api",
          created_at: "2026-08-17T14:12:00Z",
          updated_at: "2026-08-17T14:12:40Z",
        }),
    });
    return (
      <StoryProviders>
        <ImportCard />
      </StoryProviders>
    );
  },
};
