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
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrap = (node: ReactNode) => (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{node}</LocaleProvider>
    </QueryClientProvider>
  );
  const view = rtlRender(wrap(ui));
  // The form is mounted once for the life of the Settings card and driven by
  // its `open` prop, so re-opening it is a rerender, never a fresh mount.
  return { ...view, rerender: (node: ReactNode) => view.rerender(wrap(node)) };
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

  it("asks for a token again when reopened after a successful connect", async () => {
    installFetchStub({
      "POST /channel-connections": () => jsonResponse(connectedConnection, 201),
    });
    const { rerender } = render(
      <TelegramConnectForm open onClose={() => {}} />,
    );
    await userEvent.type(
      screen.getByLabelText("Bot token"),
      "555000111:AAG-fake-bot-father-token",
    );
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(await screen.findByText(/@acme_sales_bot/)).toBeInTheDocument();

    rerender(<TelegramConnectForm open={false} onClose={() => {}} />);
    rerender(<TelegramConnectForm open onClose={() => {}} />);

    // Rotating the token is the next thing this form is for, and a stale
    // success view offers only a Done button — no way back to the field
    // short of reloading the page.
    expect(await screen.findByLabelText("Bot token")).toBeInTheDocument();
    expect(screen.queryByText(/@acme_sales_bot/)).not.toBeInTheDocument();
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
