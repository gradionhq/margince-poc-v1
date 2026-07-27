// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { MirrorUserMapCard } from "./overlay-usermap";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// MirrorUserMapCard stories for the fe-uat render gate: one per visual state
// the card can honestly reach — a clean mapped table, each unmapped reason's
// chip and explanation, a manual override whose incumbent user has vanished, a
// shared seat seen from the by-owner side, the truncated-directory warning
// next to the picker, and the calm empty/native/unconfigured states. All off
// the same wire shapes overlay-usermap.test.tsx exercises.

type Entry = components["schemas"]["OverlayUserMapEntry"];
type Owner = components["schemas"]["OverlayOwner"];

function admin() {
  return () =>
    jsonResponse({
      user: { id: "me-1", email: "admin@acme.test" },
      roles: ["admin"],
      teams: [],
    });
}

const directory: Owner[] = [
  { incumbent_user_id: "o1", name: "Ada Lovelace", email: "ada@acme.test" },
  { incumbent_user_id: "o2", name: "Grace Hopper", email: "grace@acme.test" },
  { incumbent_user_id: "o3", name: "Alan Turing", email: "alan@acme.test" },
];

function mapped(
  userId: string,
  name: string,
  email: string,
  ownerId: string,
  ownerName: string,
  ownerEmail: string,
  source: NonNullable<Entry["match_source"]> = "email",
): Entry {
  return {
    user_id: userId,
    name,
    email,
    incumbent_user_id: ownerId,
    incumbent_user_name: ownerName,
    incumbent_user_email: ownerEmail,
    match_source: source,
    unmapped_reason: "none",
  };
}

function unmapped(
  userId: string,
  name: string,
  email: string,
  reason: Entry["unmapped_reason"],
): Entry {
  return { user_id: userId, name, email, unmapped_reason: reason };
}

const allMapped: Entry[] = [
  mapped(
    "me-1",
    "Admin Person",
    "admin@acme.test",
    "o1",
    "Ada Lovelace",
    "ada@acme.test",
  ),
  mapped(
    "u2",
    "Grace's Seat",
    "grace.seat@acme.test",
    "o2",
    "Grace Hopper",
    "grace@acme.test",
    "manual",
  ),
];

// Every reason the contract defines, so the gallery shows each chip and its
// explanation side by side rather than one representative case.
const everyReason: Entry[] = [
  unmapped("u1", "No Match", "nomatch@acme.test", "no_email_match"),
  unmapped("u2", "Ambiguous", "shared@acme.test", "ambiguous_email"),
  unmapped("u3", "Blocked", "blocked@acme.test", "blocked_by_admin"),
  unmapped("u4", "Not Synced", "new@acme.test", "not_yet_synced"),
  unmapped("u5", "No Diagnosis", "unknown@acme.test", "directory_unavailable"),
];

// A manual override pointing at an incumbent user the directory no longer
// lists: it grants nothing, and nothing revokes it automatically.
const staleManual: Entry[] = [
  {
    ...mapped(
      "u1",
      "Stale Override",
      "stale@acme.test",
      "o-gone",
      "Departed Owner",
      "departed@acme.test",
      "manual",
    ),
    stale_owner_ref: true,
  },
];

// Two workspace users on ONE incumbent seat — the finding the by-owner view
// exists for, invisible in the by-user list where both rows look correct.
const sharedSeat: Entry[] = [
  mapped(
    "u1",
    "First Rep",
    "first@acme.test",
    "o1",
    "Ada Lovelace",
    "ada@acme.test",
  ),
  mapped(
    "u2",
    "Second Rep",
    "second@acme.test",
    "o1",
    "Ada Lovelace",
    "ada@acme.test",
    "manual",
  ),
  unmapped("u3", "Left Out", "left@acme.test", "no_email_match"),
];

function routes(
  entries: Entry[],
  options: { truncated?: boolean; ownersFail?: boolean } = {},
) {
  return {
    "GET /me": admin(),
    "GET /overlay/user-map": () =>
      jsonResponse({ incumbent: "hubspot", entries }),
    "GET /overlay/owners": () =>
      options.ownersFail
        ? jsonResponse(
            {
              code: "upstream_unavailable",
              detail: "the HubSpot directory could not be read",
            },
            502,
          )
        : jsonResponse({
            incumbent: "hubspot",
            owners: directory,
            truncated: options.truncated ?? false,
          }),
  };
}

function card(
  entries: Entry[],
  options: { truncated?: boolean; ownersFail?: boolean } = {},
) {
  installFetchStub(routes(entries, options));
  return (
    <StoryProviders>
      <MirrorUserMapCard />
    </StoryProviders>
  );
}

const meta: Meta<typeof MirrorUserMapCard> = {
  title: "screens/overlay-usermap",
  component: MirrorUserMapCard,
};
export default meta;
type Story = StoryObj<typeof MirrorUserMapCard>;

export const AllMapped: Story = {
  render: () => card(allMapped),
};

export const EveryUnmappedReason: Story = {
  render: () => card(everyReason),
};

export const StaleManualOverride: Story = {
  render: () => card(staleManual),
};

export const SharedSeatByOwner: Story = {
  render: () => card(sharedSeat),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "By HubSpot user" }),
    );
    await canvas.findByText(/Shared seat/);
  },
};

// The truncation warning lives next to the picker, so the story has to open
// the picker for the gallery to show it.
export const TruncatedDirectory: Story = {
  render: () => card(everyReason, { truncated: true }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const mapButtons = await canvas.findAllByRole("button", { name: "Map…" });
    await userEvent.click(mapButtons[0]);
    await canvas.findByText(/longer than this list/);
  },
};

export const DirectoryUnreadable: Story = {
  render: () => card(everyReason, { ownersFail: true }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const mapButtons = await canvas.findAllByRole("button", { name: "Map…" });
    await userEvent.click(mapButtons[0]);
    await canvas.findByText(/could not be read/);
  },
};

// Unmapping yourself blanks your own CRM — survivable, so a confirm rather
// than a block. This captures the dialog open, before any confirm click.
export const UnmapSelfConfirm: Story = {
  render: () => card(allMapped),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const unmapButtons = await canvas.findAllByRole("button", {
      name: "Unmap",
    });
    await userEvent.click(unmapButtons[0]);
    await canvas.findByText(/You will stop seeing every mirrored record/);
  },
};

export const NoUsers: Story = {
  render: () => card([]),
};

export const NativeWorkspace: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /overlay/user-map": () =>
        jsonResponse(
          { code: "mode_not_overlay", detail: "workspace is native" },
          404,
        ),
      "GET /overlay/owners": () =>
        jsonResponse(
          { code: "mode_not_overlay", detail: "workspace is native" },
          404,
        ),
    });
    return (
      <StoryProviders>
        <MirrorUserMapCard />
      </StoryProviders>
    );
  },
};

export const NonAdminSeat: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          user: { id: "rep-1", email: "rep@acme.test" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <MirrorUserMapCard />
      </StoryProviders>
    );
  },
};
