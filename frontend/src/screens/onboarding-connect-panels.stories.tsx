// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import {
  GoogleConnectPanel,
  ImapConnectPanel,
} from "./onboarding-connect-panels";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The onboarding connect panels for the fe-uat render gate (G-10): the
// idle IMAP form, its post-connect state (the honest "building itself"
// framing — no fabricated capture counts), and its rejected-login error;
// plus the Google panel's pre-consent idle state.

const meta: Meta<typeof ImapConnectPanel> = {
  title: "screens/onboarding-connect-panels",
  component: ImapConnectPanel,
};
export default meta;
type Story = StoryObj<typeof ImapConnectPanel>;

async function fillAndSubmit(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.clear(canvas.getByLabelText("IMAP host"));
  await userEvent.type(canvas.getByLabelText("IMAP host"), "mail.example.org");
  await userEvent.type(canvas.getByLabelText("Email"), "lars@example.org");
  await userEvent.type(canvas.getByLabelText("App password"), "app-password");
  await userEvent.click(
    canvas.getByRole("button", { name: /connect mailbox/i }),
  );
}

export const ImapIdle: Story = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <ImapConnectPanel onComplete={async () => {}} />
      </StoryProviders>
    );
  },
};

export const ImapConnected: Story = {
  render: () => {
    installFetchStub({
      "POST /connectors/imap/connect": () =>
        jsonResponse({
          connection: {
            id: "c1",
            provider: "imap",
            status: "connected",
            scopes: [],
          },
        }),
    });
    return (
      <StoryProviders>
        <ImapConnectPanel onComplete={async () => {}} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await fillAndSubmit(canvasElement);
    await within(canvasElement).findByText(/mailbox connected/i);
  },
};

export const ImapLoginRejected: Story = {
  render: () => {
    installFetchStub({
      "POST /connectors/imap/connect": () =>
        jsonResponse(
          {
            code: "imap_login_rejected",
            detail: "The mailbox rejected these credentials.",
          },
          422,
        ),
    });
    return (
      <StoryProviders>
        <ImapConnectPanel onComplete={async () => {}} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await fillAndSubmit(canvasElement);
    await within(canvasElement).findByText(/rejected these credentials/i);
  },
};

export const GoogleIdle: Story = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <GoogleConnectPanel onComplete={async () => {}} />
      </StoryProviders>
    );
  },
};
