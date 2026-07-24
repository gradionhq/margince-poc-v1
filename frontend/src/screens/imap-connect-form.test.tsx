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
import { LocaleProvider } from "../i18n";
import { ImapConnectForm } from "./imap-connect-form";
import { installFetchStub, jsonResponse } from "./story-utils";

// The inline IMAP connect form (Task 6): first-connect and reconnect for the
// one credential provider, done through the typed client — never a raw
// fetch — and never claiming a connection the server hasn't confirmed.

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

async function fillValidForm() {
  await userEvent.type(
    screen.getByLabelText("IMAP server"),
    "mail.example.org",
  );
  await userEvent.type(
    screen.getByLabelText("Email address"),
    "lars@example.org",
  );
  await userEvent.type(screen.getByLabelText("App password"), "app-password");
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ImapConnectForm", () => {
  it("posts the imap block through the typed client, not a raw fetch", async () => {
    const calls: { url: string; body: unknown }[] = [];
    installFetchStub({
      "POST /connectors/imap/connect": (body) => {
        calls.push({ url: "POST /connectors/imap/connect", body });
        return jsonResponse({
          connection: {
            id: "c1",
            provider: "imap",
            status: "connected",
            scopes: [],
          },
        });
      },
    });
    const onConnected = vi.fn();
    render(
      <ImapConnectForm open onClose={() => {}} onConnected={onConnected} />,
    );
    await fillValidForm();
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].body).toMatchObject({
      imap: {
        host: "mail.example.org",
        port: 993,
        username: "lars@example.org",
        secret: "app-password",
        mailbox: "INBOX",
        max_messages: 50,
      },
    });
    await waitFor(() => expect(onConnected).toHaveBeenCalled());
  });

  it("surfaces a rejected login without echoing the host back", async () => {
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
    render(<ImapConnectForm open onClose={() => {}} />);
    await fillValidForm();
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(
      await screen.findByText(/rejected these credentials/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/mail\.example\.org/)).not.toBeInTheDocument();
  });

  it("surfaces an unreachable server as a host/port problem", async () => {
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
    render(<ImapConnectForm open onClose={() => {}} />);
    await fillValidForm();
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(
      await screen.findByText(/could not be reached/i),
    ).toBeInTheDocument();
  });

  it("never retains the secret after a failed submit", async () => {
    installFetchStub({
      "POST /connectors/imap/connect": () =>
        jsonResponse({ code: "imap_unreachable", detail: "unreachable" }, 502),
    });
    render(<ImapConnectForm open onClose={() => {}} />);
    await fillValidForm();
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    await screen.findByText(/could not be reached/i);
    expect(screen.getByLabelText("App password")).toHaveValue("");
  });
});
