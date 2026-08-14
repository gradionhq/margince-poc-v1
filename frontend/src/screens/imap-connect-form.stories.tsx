// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent } from "storybook/test";
import { ImapConnectForm } from "./imap-connect-form";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ImapConnectForm stories for the fe-uat render gate: the idle form, and the
// two IMAP-specific error states it branches on by problemCode — each
// captured after a play() fills the required fields and submits, so the
// screenshot shows the actual error sentence, not just the empty form.

const meta: Meta<typeof ImapConnectForm> = {
  title: "Settings/You/Connections/IMAP connect form",
  component: ImapConnectForm,
};
export default meta;
type Story = StoryObj<typeof ImapConnectForm>;

// `screen`, not the story canvas: the form is inside a portalled Modal.
async function fillAndSubmit() {
  const canvas = screen;
  await userEvent.type(
    canvas.getByLabelText("IMAP server *"),
    "mail.example.org",
  );
  await userEvent.type(
    canvas.getByLabelText("Email address *"),
    "lars@example.org",
  );
  await userEvent.type(canvas.getByLabelText("App password *"), "app-password");
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
  play: async () => {
    await fillAndSubmit();
    await screen.findByText(/rejected these credentials/i);
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
  play: async () => {
    await fillAndSubmit();
    await screen.findByText(/could not be reached/i);
  },
};

// A rejected login in dark. This is the only form in the settings catalogue that
// is a portalled dialog rather than a card, so it is the only place three layers
// composite at once: the scrim, the elevated modal on top of it, and inside that
// six labelled fields, three required marks, the caption explaining the app
// password, and the error sentence the play() brings on screen. Dark is where a
// scrim and an elevated surface stop being two things — an overlay that darkens a
// page which is already dark leaves the dialog to separate itself.
export const LoginRejectedDark: Story = {
  globals: { theme: "dark" },
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
  play: async () => {
    await fillAndSubmit();
    await screen.findByText(/rejected these credentials/i);
  },
};

// The dialog at 390px. `.modal` is `width: min(440px, 100vw - 40px)`, so a phone
// takes it to 350px — and `max-height: calc(100dvh - 40px)` with its own
// `overflow-y: auto` is what keeps the Connect button reachable when six fields
// plus a caption outrun the screen. That guard exists because a dialog centred in
// the viewport puts its actions off both ends, where nothing can reach them. What
// to check is that the actions row is inside the dialog's own scroll rather than
// below the fold of the page.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const IdlePhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <ImapConnectForm open onClose={() => {}} />
      </StoryProviders>
    );
  },
};
