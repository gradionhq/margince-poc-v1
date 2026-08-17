// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { AddDocumentDialog } from "./adddocument";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Adding a document. The state worth looking at is not the empty form — it is
// the refusal, which has to say WHICH thing is missing, and the deal list,
// which is what makes the extraction panel reachable at all.

const meta: Meta = {
  title: "Records/Company 360/Add a document",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const page = { has_more: false, next_cursor: null };

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
];

function Dialog({
  seat = "full",
  dealsFail = false,
}: Readonly<{ seat?: "full" | "read"; dealsFail?: boolean }>) {
  installFetchStub({
    "GET /me": meRoute(
      { deal: ["update"], organization: ["update"] },
      { seat },
    ),
    "GET /deals": () =>
      dealsFail
        ? jsonResponse({ title: "Server error", status: 500 }, 500)
        : jsonResponse({ data: deals, page }),
  });
  return (
    <StoryProviders>
      <AddDocumentDialog orgId="o-1" open onClose={() => {}} />
    </StoryProviders>
  );
}

/** The form as it opens: the company preselected, and Upload refused until a
 * file is chosen — with the refusal naming what is missing. */
export const Default: Story = { render: () => <Dialog /> };

/** A read seat holding the same grants. The refusal changes from "choose a
 * file" to "you may not add documents here", which is a different sentence
 * about a different problem. */
export const ReadOnlySeat: Story = { render: () => <Dialog seat="read" /> };

/** The deals could not be read. The dialog says so rather than quietly
 * offering the company as the only option, which would look identical to an
 * account that genuinely has no deals. */
export const DealsUnavailable: Story = {
  render: () => <Dialog dealsFail />,
};
