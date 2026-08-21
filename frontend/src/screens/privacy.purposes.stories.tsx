// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ConsentPurposesCard } from "./privacy";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The consent registry (the Privacy & audit tab's ConsentPurposesCard). Its own
// file rather than a second component in privacy.stories.tsx, so each surface
// keeps one story title: `fe-uat` keys on the co-located name, and a card with
// no story of its own is a card nobody looks at in either theme.
//
// The two states worth reading are the two the card actually has: a seat that
// may append to the registry, and one that may only read it — where the create
// verb is ABSENT from the header and the registry row's own description says so.

const PURPOSES = {
  data: [
    {
      id: "p1",
      key: "marketing",
      label: "Marketing",
      requires_double_opt_in: true,
    },
    {
      id: "p2",
      key: "transactional",
      label: "Transactional",
      requires_double_opt_in: false,
    },
    {
      id: "p3",
      key: "product_updates",
      label: "Product updates",
      requires_double_opt_in: false,
    },
  ],
};

// Appending a purpose is admin/ops, and the registry is readable by every seat —
// so the ROLES on the session, not an object grant, are what decide whether the
// header carries a verb (useHoldsConsentAdminRole).
function purposes(roles: string[], purposeList: unknown = PURPOSES) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({}, { roles }),
      "GET /consent-purposes": () => jsonResponse(purposeList),
    });
    return (
      <StoryProviders>
        <ConsentPurposesCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof ConsentPurposesCard> = {
  title: "Settings/Admin settings/Privacy/Consent purposes",
  component: ConsentPurposesCard,
};
export default meta;

type Story = StoryObj<typeof ConsentPurposesCard>;

// An ops seat: the registry, and `Add purpose` in the card header above it.
export const Registry: Story = { render: purposes(["ops"]) };

// A rep: the same registry, no verb, and the read-only posture as the registry
// row's own description — the sentence sits at the label's x rather than as a
// loose paragraph between the card's line and the badges.
export const ReadOnly: Story = { render: purposes(["rep"]) };

// Dark, because the registry is a run of badges and one of them carries `warn`
// for a double-opt-in purpose: a tinted badge against `--bgElevated` is the pair
// most likely to collapse when the ground goes dark.
export const RegistryDark: Story = {
  globals: { theme: "dark" },
  render: purposes(["ops"]),
};

// Nothing registered yet — the honest empty answer to the row's question, which
// takes a row's interval rather than a page-sized plate.
export const Empty: Story = {
  render: purposes(["ops"], { data: [] }),
};

// The append-only warning is the dialog's first line, said exactly once and
// beside the key it is about.
export const AddPurpose: Story = {
  render: purposes(["ops"]),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /add purpose/i }),
    );
  },
};
