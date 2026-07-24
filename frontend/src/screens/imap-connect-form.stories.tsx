// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { ImapConnectForm } from "./imap-connect-form";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ImapConnectForm stories for the fe-uat render gate: the idle form, and the
// two IMAP-specific error states it branches on by problemCode — each
// captured after a play() fills the required fields and submits, so the
// screenshot shows the actual error sentence, not just the empty form.

const meta: Meta<typeof ImapConnectForm> = {
  title: "screens/imap-connect-form",
  component: ImapConnectForm,
};
export default meta;
type Story = StoryObj<typeof ImapConnectForm>;

async function fillAndSubmit(canvasElement: HTMLElement) {
  const canvas = within(canvasElement);
  await userEvent.type(
    canvas.getByLabelText("IMAP server"),
    "mail.example.org",
  );
  await userEvent.type(
    canvas.getByLabelText("Email address"),
    "lars@example.org",
  );
  await userEvent.type(canvas.getByLabelText("App password"), "app-password");
  await userEvent.click(canvas.getByRole("button", { name: "Connect" }));
}

export const Idle: Story = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <ImapConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
};

export const LoginRejected: Story = {
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
        <ImapConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await fillAndSubmit(canvasElement);
    await within(canvasElement).findByText(/rejected these credentials/i);
  },
};

export const Unreachable: Story = {
  render: () => {
    installFetchStub({
      "POST /connectors/imap/connect": () =>
        jsonResponse(
          {
            code: "imap_unreachable",
            detail: "The mail server could not be reached.",
          },
          502,
        ),
    });
    return (
      <StoryProviders>
        <ImapConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await fillAndSubmit(canvasElement);
    await within(canvasElement).findByText(/could not be reached/i);
  },
};
