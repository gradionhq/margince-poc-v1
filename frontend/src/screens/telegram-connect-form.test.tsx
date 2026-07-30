/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { installFetchStub, jsonResponse } from "./story-utils";
import { TelegramConnectForm } from "./telegram-connect-form";

// The inline Telegram connect form (Task 17, design §9.1/§9.2): the one
// messaging-channel bot connects for the whole workspace through the same
// "paste a credential, submit" shape imap-connect-form.tsx established, but
// stays editable in place afterwards — a token replacement goes through
// PATCH, never a disconnect-reconnect cycle.

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

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("TelegramConnectForm", () => {
  it("submits the bot token and shows the resolved bot username", async () => {
    const calls: { url: string; body: unknown }[] = [];
    installFetchStub({
      "POST /channel-connections": (body) => {
        calls.push({ url: "POST /channel-connections", body });
        return jsonResponse(connectedConnection, 201);
      },
    });
    const onConnected = vi.fn();
    render(
      <TelegramConnectForm open onClose={() => {}} onConnected={onConnected} />,
    );
    await userEvent.type(
      screen.getByLabelText("Bot token"),
      "555000111:AAG-fake-bot-father-token",
    );
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].body).toEqual({
      provider: "telegram",
      botToken: "555000111:AAG-fake-bot-father-token",
    });
    expect(await screen.findByText(/@acme_sales_bot/)).toBeInTheDocument();
    // The success view is the server's own confirmation, not a claim made
    // ahead of it — onConnected only fires once that confirmation renders.
    expect(onConnected).not.toHaveBeenCalled();
  });

  it("renders a pending connection as pending, never as connected", async () => {
    installFetchStub({});
    render(
      <TelegramConnectForm
        open
        onClose={() => {}}
        connection={pendingConnection}
      />,
    );
    expect(await screen.findByText(/Pending/)).toBeInTheDocument();
    expect(screen.queryByText("Capturing")).not.toBeInTheDocument();
  });

  it("allows replacing the token without disconnecting", async () => {
    const calls: { url: string; body: unknown }[] = [];
    installFetchStub({
      "PATCH /channel-connections/018f3a1b-0000-7000-8000-0000000000d1": (
        body,
      ) => {
        calls.push({
          url: "PATCH /channel-connections/018f3a1b-0000-7000-8000-0000000000d1",
          body,
        });
        return jsonResponse({
          ...connectedConnection,
          version: 2,
        });
      },
    });
    render(
      <TelegramConnectForm
        open
        onClose={() => {}}
        connection={connectedConnection}
      />,
    );
    await userEvent.type(
      screen.getByLabelText("Bot token"),
      "555000111:BBH-rotated-token",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Replace token" }),
    );
    await waitFor(() => expect(calls.length).toBe(1));
    // Replacing the token is a PATCH against the existing connection id —
    // never a DELETE followed by a fresh POST.
    expect(calls[0].body).toEqual({ botToken: "555000111:BBH-rotated-token" });
    expect(await screen.findByText(/@acme_sales_bot/)).toBeInTheDocument();
  });

  it("surfaces a webhook-conflict refusal with its reason", async () => {
    installFetchStub({
      "POST /channel-connections": () =>
        jsonResponse(
          {
            code: "channel_webhook_owned_elsewhere",
            detail:
              "This bot already delivers its updates to another installation. Use a different bot, or disconnect it there first.",
          },
          409,
        ),
    });
    render(<TelegramConnectForm open onClose={() => {}} />);
    await userEvent.type(
      screen.getByLabelText("Bot token"),
      "555000111:AAG-fake-bot-father-token",
    );
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(
      await screen.findByText(
        /already delivers its updates to another installation/i,
      ),
    ).toBeInTheDocument();
    // The token is never retained after a failed submit.
    expect(screen.getByLabelText("Bot token")).toHaveValue("");
  });
});
