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
import {
  ImapConnectPanel,
  OAuthReturnPanel,
} from "./onboarding-connect-panels";
import { installFetchStub, jsonResponse } from "./story-utils";

// The onboarding IMAP panel (G-10): the wizard's connect step for the one
// credential provider must post through the typed client — the nested
// `{imap:{...}}` shape a STANDING connect expects (Task 1) — never a raw
// fetch to the retired transient endpoint. A standing connect answers
// BEFORE any mail is read, so the panel must never fabricate a capture
// count it was never given.

function render(ui: ReactNode, client?: QueryClient) {
  return rtlRender(
    <QueryClientProvider
      client={
        client ??
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
    render(<ImapConnectPanel onComplete={onComplete} onDismiss={() => {}} />);
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
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
    render(<ImapConnectPanel onComplete={onComplete} onDismiss={() => {}} />);
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
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
      <ImapConnectPanel
        onComplete={vi.fn().mockResolvedValue(undefined)}
        onDismiss={() => {}}
      />,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
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
      <ImapConnectPanel
        onComplete={vi.fn().mockResolvedValue(undefined)}
        onDismiss={() => {}}
      />,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
    );
    await screen.findByText(/could not be reached/i);
    expect(screen.getByLabelText("App password")).toHaveValue("");
  });

  it("invalidates the shared connectors query so Settings picks up the new connection immediately", async () => {
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
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    render(
      <ImapConnectPanel
        onComplete={vi.fn().mockResolvedValue(undefined)}
        onDismiss={() => {}}
      />,
      client,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
    );
    await screen.findByText(/mailbox connected/i);
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["connectors"] });
  });

  it("never offers a history read for IMAP, which has no backfiller behind it", async () => {
    const backreadCalls: string[] = [];
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
      "POST /connectors/imap/backfill/preview": () => {
        backreadCalls.push("preview");
        return jsonResponse({ code: "connector_unsupported" }, 422);
      },
    });
    render(
      <ImapConnectPanel
        onComplete={vi.fn().mockResolvedValue(undefined)}
        onDismiss={() => {}}
      />,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
    );
    await screen.findByText(/mailbox connected/i);
    expect(screen.queryByText(/How far back should I read/)).toBeNull();
    expect(backreadCalls).toEqual([]);
  });

  // "Not now" closes THIS dialog without deciding anything — it never
  // contacts the server and never claims the whole required step as
  // skipped, which is a separate, more deliberate action the surface offers
  // beside the provider choice.
  it("dismisses without ever contacting the server or claiming the step skipped", async () => {
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
    const onDismiss = vi.fn();
    render(<ImapConnectPanel onComplete={onComplete} onDismiss={onDismiss} />);
    await userEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(onComplete).not.toHaveBeenCalled();
    expect(calls.length).toBe(0);
  });

  // A successful connect that lands after the reader already backed out
  // would leave a mailbox connected against a "no" the panel already
  // promised — so dismissal has to wait for the in-flight POST to settle.
  it("cannot be dismissed while the connect POST is still in flight", async () => {
    // A box, not a bare `let`: TS's control-flow narrowing otherwise loses
    // the function type across the callback boundary that assigns it.
    const deferred: { resolve: ((r: Response) => void) | null } = {
      resolve: null,
    };
    installFetchStub({
      "POST /connectors/imap/connect": () =>
        new Promise((resolve) => {
          deferred.resolve = resolve;
        }),
    });
    const onDismiss = vi.fn();
    render(
      <ImapConnectPanel
        onComplete={vi.fn().mockResolvedValue(undefined)}
        onDismiss={onDismiss}
      />,
    );
    await fillValidForm();
    await userEvent.click(
      screen.getByRole("button", { name: /test and connect/i }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Not now" })).toBeDisabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(onDismiss).not.toHaveBeenCalled();

    // A success that lands after the (blocked) dismiss attempt replaces the
    // form with the connected result — "Not now" has nothing left to dismiss.
    deferred.resolve?.(
      jsonResponse({
        connection: {
          id: "c1",
          provider: "imap",
          status: "connected",
          scopes: [],
        },
      }),
    );
    await screen.findByText(/mailbox connected/i);
    expect(onDismiss).not.toHaveBeenCalled();
  });
});

// The confirmed OAuth connection hands to the backread step: the grant is not
// the history read, and the roster row the panel already holds carries the run,
// so a read in progress shows with no second request and no second start.
describe("OAuthReturnPanel handing off to the backread", () => {
  const rosterWith = (backfill?: Record<string, unknown>) => () =>
    jsonResponse({
      data: [
        {
          id: "g1",
          provider: "gmail",
          status: "connected",
          scopes: ["read"],
          ...(backfill ? { backfill } : {}),
        },
      ],
    });

  it("seeds a running read from the roster row rather than re-reading it", async () => {
    const statusReads: string[] = [];
    installFetchStub({
      "GET /connectors": rosterWith({
        state: "running",
        estimated_messages: 400,
        counts: { messages_scanned: 120 },
      }),
      "GET /connectors/gmail/backfill": () => {
        statusReads.push("gmail");
        return jsonResponse({ state: "running" });
      },
    });
    render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);

    expect(
      await screen.findByRole("heading", { name: "Reading your mailbox" }),
    ).toBeInTheDocument();
    expect(screen.getByText("120 of about 400 messages")).toBeInTheDocument();
    expect(statusReads).toEqual([]);
    // The backread owns the exit while it runs; a second "enter" button beside
    // it would finish the step without the read's own leave copy.
    expect(
      screen.queryByRole("button", { name: /enter your crm/i }),
    ).toBeNull();
  });

  it("asks for the window when the mailbox has no read yet", async () => {
    installFetchStub({
      "GET /connectors": rosterWith({ state: "none" }),
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse({
          window: "6m",
          estimated_messages: 4820,
          computed_at: "2026-07-31T09:00:00Z",
        }),
    });
    render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);

    expect(
      await screen.findByRole("heading", {
        name: "How far back should I read?",
      }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("About 4,820 messages in that window."),
    ).toBeInTheDocument();
  });

  it("keeps the plain exit when no connection could be confirmed", async () => {
    installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
    render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);

    await screen.findByText("We couldn't confirm the connection.");
    expect(
      screen.getByRole("button", { name: /enter your crm/i }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/How far back should I read/)).toBeNull();
  });

  // Without this gate the escape reads on `live === undefined`, which is also
  // true of the roster's very first render — a reader could click straight
  // through before the OAuth connection was ever confirmed live.
  it("withholds the Enter CRM escape while the roster is still verifying", async () => {
    // A box, not a bare `let`: TS's control-flow narrowing otherwise loses
    // the function type across the callback boundary that assigns it.
    const deferred: { resolve: ((r: Response) => void) | null } = {
      resolve: null,
    };
    installFetchStub({
      "GET /connectors": () =>
        new Promise((resolve) => {
          deferred.resolve = resolve;
        }),
    });
    render(<OAuthReturnPanel outcome="ok" onComplete={vi.fn()} />);

    await screen.findByText("Confirming the connection…");
    expect(
      screen.queryByRole("button", { name: /enter your crm/i }),
    ).not.toBeInTheDocument();

    deferred.resolve?.(jsonResponse({ data: [] }));
    expect(
      await screen.findByRole("button", { name: /enter your crm/i }),
    ).toBeInTheDocument();
  });

  // The confirmation callback is how the act above knows whether the honest
  // skip/retry exit still has to stay open — it must never fire `true` before
  // a live mailbox for THIS returning provider is actually found.
  it("tells the caller only once a live mailbox is confirmed, never while unresolved", async () => {
    installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
    const onConfirmedChange = vi.fn();
    render(
      <OAuthReturnPanel
        outcome="ok"
        onComplete={vi.fn()}
        onConfirmedChange={onConfirmedChange}
      />,
    );

    await screen.findByText("We couldn't confirm the connection.");
    expect(onConfirmedChange).toHaveBeenCalledWith(false);
    expect(onConfirmedChange).not.toHaveBeenCalledWith(true);
  });

  it("tells the caller true once the roster confirms a live mailbox", async () => {
    installFetchStub({
      "GET /connectors": rosterWith({ state: "none" }),
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse({
          window: "6m",
          estimated_messages: 10,
          computed_at: "2026-07-31T09:00:00Z",
        }),
    });
    const onConfirmedChange = vi.fn();
    render(
      <OAuthReturnPanel
        outcome="ok"
        onComplete={vi.fn()}
        onConfirmedChange={onConfirmedChange}
      />,
    );

    await waitFor(() => expect(onConfirmedChange).toHaveBeenCalledWith(true));
  });
});
