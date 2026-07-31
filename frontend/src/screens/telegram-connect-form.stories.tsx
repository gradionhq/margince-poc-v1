// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import { TelegramConnectForm } from "./telegram-connect-form";

// TelegramConnectForm stories for the fe-uat render gate: the idle
// first-connect form, the resolved-username success view, the edit-in-place
// form on a pending connection (rendered as pending, never as connected —
// design §9.1), and the webhook-conflict refusal (§5) — each captured after
// a play() drives the actual interaction, not just the empty form.

type ChannelConnection = components["schemas"]["ChannelConnection"];

const pendingConnection: ChannelConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000d1",
  provider: "telegram",
  channelId: "555000111",
  channelLabel: "acme_sales_bot",
  status: "pending",
  version: 1,
};

const connectedConnection: ChannelConnection = {
  ...pendingConnection,
  status: "connected",
};

const meta: Meta<typeof TelegramConnectForm> = {
  title: "screens/telegram-connect-form",
  component: TelegramConnectForm,
};
export default meta;
type Story = StoryObj<typeof TelegramConnectForm>;

async function fillAndSubmit(canvasElement: HTMLElement, cta: string) {
  const canvas = within(canvasElement);
  await userEvent.type(
    canvas.getByLabelText("Bot token"),
    "555000111:AAG-fake-bot-father-token",
  );
  await userEvent.click(canvas.getByRole("button", { name: cta }));
}

export const Idle: Story = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <TelegramConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
};

export const Connected: Story = {
  render: () => {
    installFetchStub({
      "POST /channel-connections": () => jsonResponse(connectedConnection, 201),
    });
    return (
      <StoryProviders>
        <TelegramConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await fillAndSubmit(canvasElement, "Connect");
    await within(canvasElement).findByText(/@acme_sales_bot/);
  },
};

export const EditPendingConnection: Story = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <TelegramConnectForm
          open
          onClose={() => {}}
          connection={pendingConnection}
        />
      </StoryProviders>
    );
  },
};

export const WorkspaceAlreadyBound: Story = {
  render: () => {
    installFetchStub({
      "POST /channel-connections": () =>
        jsonResponse(
          {
            code: "channel_workspace_already_bound",
            detail:
              "Another bot is already connected to this workspace. Disconnect it first, or replace its token to point it at a different bot.",
          },
          409,
        ),
    });
    return (
      <StoryProviders>
        <TelegramConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await fillAndSubmit(canvasElement, "Connect");
    await within(canvasElement).findByText(
      /already connected to this workspace/i,
    );
  },
};
