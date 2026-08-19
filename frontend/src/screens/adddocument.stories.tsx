// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { AddDocumentDialog } from "./adddocument";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Adding a document. The states worth looking at are not the empty form — they
// are the refusal, which has to say WHICH thing is missing; the deal question,
// which is what makes the extraction panel reachable at all; and the contact's
// version of the same dialog, which has no deal question to ask.
//
// The dialog is a Modal, so it renders into the document body rather than into
// the story canvas: a `play` has to scope past `canvasElement` or it never sees
// the form at all.

const meta: Meta = {
  title: "Records/Company 360/Add a document",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const page = { has_more: false, next_cursor: null };

// More deals than anybody reads in a dropdown, which is the whole of what issue
// 1536 was about: the control is a search over the account's deals, so the two
// named ones below are found by typing part of a name rather than by scrolling
// past sixty framework agreements.
const deals = [
  {
    id: "deal-1",
    name: "Pallet Handling Programme — Graz",
    organization_id: "o-1",
    status: "open",
  },
  {
    id: "deal-2",
    name: "Wash cycle retrofit",
    organization_id: "o-1",
    status: "open",
  },
  ...Array.from({ length: 60 }, (_unused, index) => ({
    id: `deal-bulk-${index}`,
    name: `Spare parts framework ${2020 + (index % 6)} — lot ${index}`,
    organization_id: "o-1",
    status: "open",
  })),
];

function Dialog({
  seat = "full",
  onPerson = false,
  dealsFail = false,
}: Readonly<{
  seat?: "full" | "read";
  onPerson?: boolean;
  dealsFail?: boolean;
}>) {
  installFetchStub({
    "GET /me": meRoute(
      { deal: ["update"], organization: ["update"], person: ["update"] },
      { seat },
    ),
    "GET /deals": () =>
      dealsFail
        ? jsonResponse({ title: "Server error", status: 500 }, 500)
        : jsonResponse({ data: deals, page }),
  });
  const anchor = onPerson
    ? ({ record: "person", id: "p-1" } as const)
    : ({ record: "organization", id: "o-1" } as const);
  return (
    <StoryProviders>
      <AddDocumentDialog anchor={anchor} open onClose={() => {}} />
    </StoryProviders>
  );
}

/** Choose "A deal", then search for one by name. */
async function searchForADeal(canvasElement: HTMLElement, term: string) {
  const body = within(canvasElement.ownerDocument.body);
  await userEvent.click(await body.findByRole("radio", { name: /A deal/ }));
  await userEvent.type(
    await body.findByRole("searchbox", { name: /Search this account/ }),
    term,
  );
}

/** The form as it opens on an account: the company chosen, and Upload refused
 * until a file is picked — with the refusal naming what is missing. */
export const Default: Story = { render: () => <Dialog /> };

/** "A deal" chosen and a name typed. The candidate is a pick, and the sentence
 * under the field says how far the search reaches — stated whatever the
 * account's size, because a caption that only appeared once a walk ran out
 * would be a claim about the last search rather than about the control. */
export const FilingAgainstADeal: Story = {
  render: () => <Dialog />,
  play: async ({ canvasElement }) => {
    await searchForADeal(canvasElement, "graz");
    await within(canvasElement.ownerDocument.body).findByRole("button", {
      name: /Pallet Handling Programme/,
    });
  },
};

/** The same dialog opened from a CONTACT's file library. There is no deal
 * question: a deal hangs off a company, and nothing on a contact's page names
 * one — so the file is filed against the contact and the form asks nothing it
 * has no second answer for. */
export const OnAContact: Story = { render: () => <Dialog onPerson /> };

/** A read seat holding the same grants. The refusal changes from "choose a
 * file" to "you may not add documents here", which is a different sentence
 * about a different problem. */
export const ReadOnlySeat: Story = { render: () => <Dialog seat="read" /> };

/** The deal search cannot reach the server. The picker reports the failure on
 * its own line and offers no candidates, rather than an empty result set that
 * reads as an account with no matching deal. */
export const DealSearchUnavailable: Story = {
  render: () => <Dialog dealsFail />,
  play: async ({ canvasElement }) => {
    await searchForADeal(canvasElement, "graz");
  },
};
