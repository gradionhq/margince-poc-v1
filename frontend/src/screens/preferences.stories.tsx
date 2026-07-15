// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { PreferenceCenterScreen } from "./preferences";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The public, anonymous preference center (G-6/G-7): no session, no
// workspace header — the token in the URL is the whole capability.
// PreferenceCenter is {purposes: [{key, label, state, locked}]} — no
// events — matching preferences.test.tsx's CENTER fixture exactly.

const CENTER = {
  purposes: [
    {
      key: "transactional",
      label: "Deal & service messages",
      state: "granted",
      locked: true,
    },
    {
      key: "marketing_email",
      label: "Product updates",
      state: "granted",
      locked: false,
    },
    { key: "events", label: "Events", state: "unknown", locked: false },
  ],
};

function center(routes: RouteMap) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <PreferenceCenterScreen token="tok-123" />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof PreferenceCenterScreen> = {
  title: "screens/preferences",
  component: PreferenceCenterScreen,
};
export default meta;

type Story = StoryObj<typeof PreferenceCenterScreen>;

export const Default: Story = {
  render: center({
    "GET /public/preferences/tok-123": () => jsonResponse(CENTER),
  }),
};

// A staged (unsaved) toggle: the save bar names exactly what would be sent.
export const Dirty: Story = {
  render: center({
    "GET /public/preferences/tok-123": () => jsonResponse(CENTER),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("switch", { name: /product updates/i }),
    );
  },
};

// G-7: the RFC 8058 one-click landing — every non-locked purpose withdrawn
// in one call, with an explicit-opt-in undo rather than a silent re-grant.
export const OneClickLanding: Story = {
  render: center({
    "GET /public/preferences/tok-123": () => jsonResponse(CENTER),
    "POST /public/preferences/tok-123/unsubscribe": () =>
      jsonResponse({ unsubscribed: ["marketing_email"] }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /unsubscribe from all/i }),
    );
  },
};

// PUT loops choices in separate transactions (handlers_public.go): a
// mid-list failure leaves earlier choices committed, so the card re-reads
// rather than trust the optimistic draft — the honest "may have been saved"
// banner, not a silent success.
export const PartialSave: Story = {
  render: center({
    "GET /public/preferences/tok-123": () => jsonResponse(CENTER),
    "PUT /public/preferences/tok-123": () =>
      jsonResponse(
        {
          title: "not a tracked consent purpose",
          status: 422,
          code: "invalid",
        },
        422,
      ),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("switch", { name: /product updates/i }),
    );
    await userEvent.click(
      canvas.getByRole("button", { name: /save preferences/i }),
    );
    await canvas.findByText(/some of your choices may have been saved/i);
  },
};

// An unknown or revoked token both read as a 404 — this surface must never
// become an oracle for whether an address is known, so the copy is identical
// either way.
export const InvalidLink: Story = {
  render: center({
    "GET /public/preferences/tok-123": () =>
      jsonResponse({ title: "not found", status: 404 }, 404),
  }),
};
