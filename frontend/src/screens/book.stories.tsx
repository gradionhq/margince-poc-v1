// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { formatDateTime } from "../format/format";
import { BookingScreen } from "./book";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// BookingScreen is rail-less and has two variants behind one prop: the
// anonymous PUBLIC page (#/book/<host_slug>, over the /public/booking surface)
// when `hostSlug` is set, and the session-authed page (#/book) when it is not.
// Both are here, because both are what the component draws.
//
// The public page's invariant is that a confirmation is never fabricated: a slot
// is only submittable once the visitor has given a name, an email AND consent,
// and the wording shown at the checkbox is byte-for-byte the wording submitted.
// So the resting public story shows slots that are deliberately not yet
// clickable, and the confirmed story earns the click through play().
const HOST_SLUG = "ada-lovelace";

const SLOTS = [
  { start: "2026-07-20T09:00:00Z", end: "2026-07-20T09:30:00Z" },
  { start: "2026-07-20T11:00:00Z", end: "2026-07-20T11:30:00Z" },
  { start: "2026-07-21T13:30:00Z", end: "2026-07-21T14:00:00Z" },
  { start: "2026-07-22T08:00:00Z", end: "2026-07-22T08:30:00Z" },
];

const offered = { slots: SLOTS, truncated: false };
const nothingFree = { slots: [], truncated: false };

const PUBLIC_AVAILABILITY = `GET /public/booking/${HOST_SLUG}/availability`;
const PUBLIC_BOOK = `POST /public/booking/${HOST_SLUG}`;

// The slot button's accessible name IS its formatted label, so the play()
// lookup formats the same instant with the same locale and zone the component
// uses. Whitespace is normalized because the accessible-name matcher collapses
// it, and Intl separators are not always the plain space it collapses to.
function slotButtonName(slot: { start: string }): string {
  return formatDateTime(slot.start, "en", "Europe/Berlin").replace(/\s+/g, " ");
}

async function bookAsVisitor({
  canvasElement,
}: {
  canvasElement: HTMLElement;
}) {
  const canvas = within(canvasElement);
  const user = userEvent.setup();
  await user.type(canvas.getByLabelText("Your name"), "Nina Weber");
  await user.type(canvas.getByLabelText("Your email"), "nina@brandt.example");
  await user.click(canvas.getByRole("checkbox"));
  await user.click(
    canvas.getByRole("button", { name: slotButtonName(SLOTS[0]) }),
  );
}

const meta: Meta<typeof BookingScreen> = {
  title: "Screens/Booking page",
  component: BookingScreen,
};
export default meta;
type Story = StoryObj<typeof BookingScreen>;

// One render helper per variant, so a dark twin differs from the story it
// re-captures by its theme alone rather than by a second copy of the routes.
function publicStory(routes: RouteMap) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <BookingScreen hostSlug={HOST_SLUG} />
      </StoryProviders>
    );
  };
}

const offeredRoutes: RouteMap = {
  [PUBLIC_AVAILABILITY]: () => jsonResponse(offered),
};
const bookableRoutes: RouteMap = {
  ...offeredRoutes,
  [PUBLIC_BOOK]: () => jsonResponse(SLOTS[0], 201),
};
const slotTakenRoutes: RouteMap = {
  ...offeredRoutes,
  [PUBLIC_BOOK]: () =>
    jsonResponse(
      {
        title: "Slot no longer available",
        status: 409,
        code: "slot_taken",
        detail: "Somebody booked this slot first.",
      },
      409,
    ),
};

// Slots on offer, consent not yet given: every slot is drawn and every slot is
// disabled. That pairing is the point — the visitor sees what is available
// before handing over anything, and the page cannot take a booking until the
// consent line has actually been read and ticked.
export const PublicSlotsOffered: Story = { render: publicStory(offeredRoutes) };

// The confirmed state, reached the only way the product allows: name, email,
// consent, then a slot. The confirmation card echoes the slot the SERVER
// returned, not the one that was clicked.
export const PublicBookingConfirmed: Story = {
  render: publicStory(bookableRoutes),
  play: bookAsVisitor,
};

// Dark, on the confirmed frame, because that is where this page's whole point
// lands: the confirmation card has to be the loudest surface on a rail-less page
// whose ground has just darkened under it.
export const PublicBookingConfirmedDark: Story = {
  globals: { theme: "dark" },
  render: publicStory(bookableRoutes),
  play: bookAsVisitor,
};

// A host with nothing free in the window. The availability read succeeded and
// returned zero slots, which is a different fact from a read that failed — the
// page says so with the empty state rather than an error.
export const PublicNoAvailability: Story = {
  render: publicStory({
    [PUBLIC_AVAILABILITY]: () => jsonResponse(nothingFree),
  }),
};

// The slot went while the visitor was filling the form (409 slot_taken). The
// page says nothing was scheduled and re-asks for availability — it never draws
// a confirmation it did not receive. Dark as well as light, because the disabled
// slot buttons and this refusal card are two greys that a darker palette
// compresses toward each other.
export const PublicSlotTaken: Story = {
  render: publicStory(slotTakenRoutes),
  play: bookAsVisitor,
};

export const PublicSlotTakenDark: Story = {
  globals: { theme: "dark" },
  render: publicStory(slotTakenRoutes),
  play: bookAsVisitor,
};

// The session-authed variant (#/book, no host slug): no consent gate, because
// the booker is the signed-in user, and an attendee field that recognizes a
// known contact on blur. Slots are clickable from the first render.
export const SessionSlotsOffered: Story = {
  render: () => {
    installFetchStub({ "GET /availability": () => jsonResponse(offered) });
    return (
      <StoryProviders>
        <BookingScreen />
      </StoryProviders>
    );
  },
};
