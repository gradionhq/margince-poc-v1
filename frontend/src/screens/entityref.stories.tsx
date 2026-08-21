// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { EntityRef } from "./entityref";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// EntityRef resolves a record id to its display name and backlinks to the
// 360, and its unresolved readings are all captured here because they are
// deliberately NOT the same sentence. A read that answered without a name keeps
// the id, which on an audit row is the one traceable fact left. A read that
// FAILED says so: painting the id there would state as settled a question that
// nothing answered. Neither becomes a link — a name that did not arrive cannot
// be trusted as a destination. A user reference adds a fourth case: the roster
// is paged, and one whose walk ran out of pages has said nothing about the id
// it does not carry, so it reads as a name that did not load rather than as a
// user who is not in the list.
const meta: Meta<typeof EntityRef> = {
  title: "Patterns/Entity reference",
  component: EntityRef,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => {
      installFetchStub({
        "GET /organizations/o-1": () =>
          jsonResponse({ id: "o-1", display_name: "Brandt Automotive GmbH" }),
        // Refused rather than missing: a 404 is an answer (the record is gone
        // or hidden) and keeps the id, while a 403 is the read never arriving.
        "GET /organizations/o-refused": () =>
          jsonResponse({ title: "permission denied" }, 403),
        // A roster that offers another cursor for every page it answers: the
        // walk spends its page budget and still has not reached the end, which
        // is the state the fourth reading exists for.
        "GET /users": () =>
          jsonResponse({
            data: [{ id: "u-1", display_name: "Priya Shah" }],
            page: { next_cursor: "more", has_more: true },
          }),
      });
      return (
        <StoryProviders>
          <Story />
        </StoryProviders>
      );
    },
  ],
};
export default meta;

type Story = StoryObj<typeof EntityRef>;

export const ResolvedBacklink: Story = {
  args: { kind: "organization", id: "o-1" },
};

export const UnresolvedFallsBackToId: Story = {
  args: { kind: "organization", id: "o-unknown" },
};

export const FailedReadSaysSo: Story = {
  args: { kind: "organization", id: "o-refused" },
};

export const RosterStoppedShortSaysNameDidNotLoad: Story = {
  args: { kind: "user", id: "u-past-the-walk" },
};
