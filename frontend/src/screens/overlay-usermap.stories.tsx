// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { MirrorUserMapCard } from "./overlay-usermap";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// MirrorUserMapCard stories for the fe-uat render gate: one per visual state
// the card can honestly reach — a clean mapped table, each unmapped reason's
// chip and explanation, a manual override whose incumbent user has vanished, a
// shared seat seen from the by-owner side, the owner picker and the two things
// it can say instead of a list, and the calm empty/native/unconfigured states.
// All off the same wire shapes overlay-usermap.test.tsx exercises.
//
// The card speaks the settings row language now: the grouping toggle is an
// answer in the right column, the roster is the subject and takes the full
// width below its naming, and choosing an owner happens in a dialog. Which
// means three stories reach a portalled surface — see OwnerPicker's note.

type Entry = components["schemas"]["OverlayUserMapEntry"];
type Owner = components["schemas"]["OverlayOwner"];

// The grants are not enough on their own: the card reads `useSorMode()` first and
// draws "this workspace reads from native tables" for anything that is not
// overlay, before either query matters. meFixture states no mode, which is
// native — so without this line every story in the file drew that one empty
// state, the mapped table this file exists to picture appeared in none of them,
// and the play() cases were asserting against a card that was never there.
function admin() {
  return () =>
    jsonResponse({
      ...meFixture({ allow: { overlay_connection: ["read", "update"] } }),
      user: { ...meFixture().user, id: "me-1", email: "admin@acme.test" },
      system_of_record: { mode: "overlay" },
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
  title: "Settings/Admin settings/Integrations/Mirror user map",
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

// The picker itself, which is a dialog now rather than a block inside the row.
// This is the state the gallery could not previously show at all: the row states
// the answer, and choosing a new one happens here.
//
// `screen` for everything inside it, never the canvas — Modal portals to
// document.body, so a canvas-scoped query rejects, and a rejecting play() used
// to report after the gate had already screenshotted and passed the story. The
// UnmapSelfConfirm story below hit this first; the picker now shares it.
export const OwnerPicker: Story = {
  render: () => card(everyReason),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const mapButtons = await canvas.findAllByRole("button", { name: "Map" });
    await userEvent.click(mapButtons[0]);
    const picker = await screen.findByRole("dialog");
    await userEvent.type(
      within(picker).getByLabelText(/Search HubSpot users/),
      "ada",
    );
    await within(picker).findByRole("button", { name: /Ada Lovelace/ });
  },
};

// The truncation warning lives with the picker, so the story has to open the
// picker for the gallery to show it.
export const TruncatedDirectory: Story = {
  render: () => card(everyReason, { truncated: true }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const mapButtons = await canvas.findAllByRole("button", { name: "Map" });
    await userEvent.click(mapButtons[0]);
    await screen.findByText(/longer than this list/);
  },
};

export const DirectoryUnreadable: Story = {
  render: () => card(everyReason, { ownersFail: true }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const mapButtons = await canvas.findAllByRole("button", { name: "Map" });
    await userEvent.click(mapButtons[0]);
    await screen.findByText(/could not be read/);
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
    // `screen`, not the canvas: ConfirmModal portals to document.body, so a
    // canvas-scoped query for its body rejects — and a rejecting play() used to
    // report after the gate had already screenshotted and passed the story.
    await screen.findByText(/You will stop seeing every mirrored record/);
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
          ...meFixture({ roles: ["rep"] }),
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

// Every unmapped reason in dark. Five rows of chips is the densest colour this
// card ever shows, and each Badge composites its tint over --bgElevated whatever
// the surface under it actually is (atoms.css), so dark is where a chip either
// separates from its row or stops reading as a chip. The pairing to read is chip
// against explanation: every reason chip sits beside a body sentence saying what
// to do about it, and the chip has to stay the loud half of the pair.
export const EveryUnmappedReasonDark: Story = {
  globals: { theme: "dark" },
  render: () => card(everyReason),
};

// The picker in dark, because a dialog is the one surface whose ground is not
// the page's: the modal fills itself, the card behind it dims, and the pair has
// to stay separable. The candidate list is plain buttons on that fill, so what
// to check is whether a hovered candidate still reads as pickable against it.
export const OwnerPickerDark: Story = {
  globals: { theme: "dark" },
  render: () => card(everyReason),
  play: OwnerPicker.play,
};

// The mapped table at 390px, which is the width this card's layout was rebuilt
// for. `.usermap-identity` is `flex: 1 1 12rem` and the actions column wraps
// within itself rather than holding its full nowrap width — so at a phone the
// identity gets roughly 12rem and everything else has to fold into it: a name, an
// address with nothing to break on, the "You" chip on the reader's own row, the
// incumbent identity it is mapped to, and a match-source chip. Two buttons follow,
// and a Button never wraps its own label (base.css `.btn`), so what to check is
// whether Change and Unmap sit under the identity as a pair or one ends up alone.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const AllMappedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => card(allMapped),
};
