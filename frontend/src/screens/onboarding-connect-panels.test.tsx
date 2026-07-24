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
import { ImapConnectPanel } from "./onboarding-connect-panels";
import { installFetchStub, jsonResponse } from "./story-utils";

// The onboarding IMAP panel (G-10): the wizard's connect step for the one
// credential provider must post through the typed client — the nested
// `{imap:{...}}` shape a STANDING connect expects (Task 1) — never a raw
// fetch to the retired transient endpoint. A standing connect answers
// BEFORE any mail is read, so the panel must never fabricate a capture
// count it was never given.

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
  await userEvent.clear(screen.getByLabelText("IMAP host"));
  await userEvent.type(screen.getByLabelText("IMAP host"), "mail.example.org");
  await userEvent.type(screen.getByLabelText("Email"), "lars@example.org");
  await userEvent.type(screen.getByLabelText("App password"), "app-password");
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ImapConnectPanel", () => {
  it("connects IMAP through the typed client, not a raw fetch to a bespoke path", async () => {
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
    const onComplete = vi.fn().mockResolvedValue(undefined);
    render(<ImapConnectPanel onComplete={onComplete} />);
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /connect mailbox/i }),
    );
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].body).toMatchObject({
      imap: {
        host: "mail.example.org",
        port: 993,
        username: "lars@example.org",
        secret: "app-password",
        mailbox: "INBOX",
        max_messages: 30,
      },
    });

    // The panel never claims a one-shot capture summary it was never given:
    // a standing connect answers before any mail is read.
    expect(await screen.findByText(/mailbox connected/i)).toBeInTheDocument();
    expect(screen.queryByText(/captured/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/contacts/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/skipped/i)).not.toBeInTheDocument();
  });

  it("finishes the step (without claiming a connection) when entering the CRM", async () => {
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
    const onComplete = vi.fn().mockResolvedValue(undefined);
    render(<ImapConnectPanel onComplete={onComplete} />);
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /connect mailbox/i }),
    );
    await screen.findByText(/mailbox connected/i);
    await userEvent.click(
      screen.getByRole("button", { name: /enter your crm/i }),
    );
    expect(onComplete).toHaveBeenCalledWith(false);
  });

  it("surfaces a rejected IMAP login without echoing the host back", async () => {
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
    render(
      <ImapConnectPanel onComplete={vi.fn().mockResolvedValue(undefined)} />,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /connect mailbox/i }),
    );
    expect(
      await screen.findByText(/rejected these credentials/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/mail\.example\.org/)).not.toBeInTheDocument();
  });

  it("never retains the password after a failed submit", async () => {
    installFetchStub({
      "POST /connectors/imap/connect": () =>
        jsonResponse({ code: "imap_unreachable", detail: "unreachable" }, 502),
    });
    render(
      <ImapConnectPanel onComplete={vi.fn().mockResolvedValue(undefined)} />,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /connect mailbox/i }),
    );
    await screen.findByText(/could not be reached/i);
    expect(screen.getByLabelText("App password")).toHaveValue("");
  });

  it("skips the step without ever contacting the server", async () => {
    const calls: unknown[] = [];
    installFetchStub({
      "POST /connectors/imap/connect": (body) => {
        calls.push(body);
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
    const onComplete = vi.fn().mockResolvedValue(undefined);
    render(<ImapConnectPanel onComplete={onComplete} />);
    await userEvent.click(
      screen.getByRole("button", { name: /skip for now/i }),
    );
    expect(onComplete).toHaveBeenCalledWith(true);
    expect(calls.length).toBe(0);
  });
});
