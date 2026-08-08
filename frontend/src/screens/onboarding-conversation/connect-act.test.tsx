/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import { en } from "../../i18n/en";
import { installFetchStub, jsonResponse } from "../story-utils";
import { ConnectAct } from "./connect-act";
import type { ConversationState } from "./conversation-machine";
import { initialConversationState } from "./conversation-machine";

// The Microsoft chip must open the SAME live OAuth panel as Google (no more
// disabled "Soon" badge), and the post-consent return view no longer depends
// on which chip was open when the redirect left — it reads the roster fresh.
//
// The act also owns the finish: the backread that follows a confirmed
// connection can be left running or declined, and either way the step
// completes exactly once — CONNECT_DONE is the only event this act dispatches
// and a second one would move the machine out from under the wizard.

function renderConnectAct(
  outcome?: string,
  linkedinStatus: ConversationState["linkedinStatus"] = "pending",
  phase: ConversationState["phase"] = "cn.consent",
  returningProvider?: string,
) {
  const state: ConversationState = {
    ...initialConversationState,
    act: "connect",
    phase,
    linkedinStatus,
  };
  const dispatch = vi.fn();
  const persist = vi.fn(async () => true);
  const view = render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <ConnectAct
          state={state}
          dispatch={dispatch}
          persist={persist}
          outcome={outcome}
          returningProvider={returningProvider}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...view, dispatch, persist };
}

const rosterWith = (backfill: Record<string, unknown>) => () =>
  jsonResponse({
    data: [
      {
        id: "g1",
        provider: "gmail",
        status: "connected",
        scopes: ["read"],
        backfill,
      },
    ],
  });

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});
// Explicit, because auto-cleanup only runs with vitest globals enabled and
// this suite does not use them: without it each test inherits the previous
// render's DOM and every chip query finds several matches.
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  // The OAuth-attempt mark lives in sessionStorage (see
  // onboarding-connect-panels.tsx) — jsdom shares it across tests in this
  // same process, so a mark one test writes must not leak into the next.
  sessionStorage.clear();
});

it("offers Microsoft as a live card and opens its dialog", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  renderConnectAct();
  // The card names the provider AND what connecting it grants, so the
  // accessible name is the whole card, not the brand alone.
  const ms = screen.getByRole("button", { name: /Microsoft/ });
  // Idle until the roster confirms nothing is connected yet — the fetch
  // above resolves asynchronously, so the card starts disabled.
  await waitFor(() => expect(ms).not.toBeDisabled(), { timeout: 3000 });
  await userEvent.click(ms);
  // The ask opens as its OWN dialog on the surface, never an inline panel
  // growing under the card.
  expect(await screen.findByRole("dialog")).toBeTruthy();
  expect(
    await screen.findByRole("button", {
      name: "Allow access to my Microsoft",
    }),
  ).toBeTruthy();
});

// A card left clickable while the roster is unread would let a reader connect
// a second mailbox before the fetch ever reports the first — the scene's own
// "pick one" rule depends on the roster actually being verified first.
it("withholds every mail provider card until the roster load settles", async () => {
  // A box, not a bare `let`: TS's control-flow narrowing otherwise loses the
  // function type across the callback boundary that assigns it.
  const deferred: { resolve: ((r: Response) => void) | null } = {
    resolve: null,
  };
  installFetchStub({
    "GET /connectors": () =>
      new Promise((resolve) => {
        deferred.resolve = resolve;
      }),
  });
  renderConnectAct();

  for (const name of [/Google/, /Microsoft/, /Any inbox/]) {
    expect(screen.getByRole("button", { name })).toBeDisabled();
  }

  deferred.resolve?.(jsonResponse({ data: [] }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /Google/ })).not.toBeDisabled(),
  );
});

// A roster fetch that failed reports the same "nothing confirmed yet" fact as
// one still loading — actionable cards here would offer to connect a second
// mailbox the failed read simply never got to report.
it("withholds every mail provider card when the roster fetch fails", async () => {
  installFetchStub({
    "GET /connectors": () => jsonResponse({ code: "internal" }, 500),
  });
  renderConnectAct();

  await waitFor(() =>
    expect(screen.getByRole("button", { name: /Google/ })).toBeDisabled(),
  );
  expect(screen.getByRole("button", { name: /Microsoft/ })).toBeDisabled();
  expect(screen.getByRole("button", { name: /Any inbox/ })).toBeDisabled();
});

// The mark is what tells a genuine return apart from a stale or bookmarked
// outcome URL (see `attemptedProvider` in connect-act.tsx) — it has to be
// written before the redirect actually leaves, not after.
it("marks this tab's own attempt before the real redirect fires", async () => {
  installFetchStub({
    "GET /connectors": () => jsonResponse({ data: [] }),
    "POST /connectors/graph/connect": () =>
      jsonResponse({ authorize_url: "https://login.microsoftonline/x" }),
  });
  const assign = vi.fn();
  vi.stubGlobal("location", { ...globalThis.location, assign });
  renderConnectAct();
  await userEvent.click(screen.getByRole("button", { name: /Microsoft/ }));
  await userEvent.click(
    await screen.findByRole("button", { name: "Allow access to my Microsoft" }),
  );
  await waitFor(() => expect(assign).toHaveBeenCalled());
  expect(sessionStorage.getItem("ob.connect.oauthAttempt")).toBe("graph");
});

describe("returning to the dialog a proven attempt left from", () => {
  it("reopens the same dialog, showing the result rather than a fresh ask", async () => {
    sessionStorage.setItem("ob.connect.oauthAttempt", "graph");
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [
            {
              id: "g1",
              provider: "graph",
              status: "connected",
              scopes: ["read"],
              backfill: { state: "done" },
            },
          ],
        }),
    });
    renderConnectAct("ok", "pending", "cn.consent", "graph");

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeTruthy();
    // The plain provider name, not the pre-consent "access needed" ask —
    // nothing is being requested any more.
    expect(
      screen.getByRole("heading", { name: "Microsoft" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("Live and capturing")).toBeTruthy();
    // Consumed on read: a reload of this same URL would find no mark.
    expect(sessionStorage.getItem("ob.connect.oauthAttempt")).toBeNull();
  });

  it("falls back to the plain inline result when the URL's provider does not match this tab's own attempt", async () => {
    sessionStorage.setItem("ob.connect.oauthAttempt", "gmail");
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [
            {
              id: "g1",
              provider: "graph",
              status: "connected",
              scopes: ["read"],
              backfill: { state: "done" },
            },
          ],
        }),
    });
    renderConnectAct("ok", "pending", "cn.consent", "graph");

    expect(await screen.findByText("Live and capturing")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("falls back to the plain inline result when no mark is recorded at all — a stale or bookmarked link", async () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({
          data: [
            {
              id: "g1",
              provider: "graph",
              status: "connected",
              scopes: ["read"],
              backfill: { state: "done" },
            },
          ],
        }),
    });
    renderConnectAct("ok", "pending", "cn.consent", "graph");

    expect(await screen.findByText("Live and capturing")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

it("renders the provider-agnostic return view on OAuth return", async () => {
  installFetchStub({
    "GET /connectors": () =>
      jsonResponse({
        data: [
          {
            id: "g1",
            provider: "graph",
            status: "connected",
            scopes: ["read"],
            backfill: { state: "done" },
          },
        ],
      }),
  });
  renderConnectAct("ok");
  expect(await screen.findByText("Live and capturing")).toBeTruthy();
});

it("asks how far back to read once the mailbox is confirmed", async () => {
  installFetchStub({
    "GET /connectors": rosterWith({ state: "none" }),
    "POST /connectors/gmail/backfill/preview": () =>
      jsonResponse({
        window: "6m",
        estimated_messages: 900,
        computed_at: "2026-07-31T10:00:00Z",
      }),
  });
  renderConnectAct("ok");
  expect(
    await screen.findByRole("heading", {
      name: "How far back should I read?",
    }),
  ).toBeTruthy();
  // Exactly one history-read decision on the surface — the standalone
  // Settings-style backfill panel is not a second one stacked beside it.
  expect(screen.queryByText("Import your mail history")).toBeNull();
  expect(
    screen.getAllByRole("heading", { name: "How far back should I read?" }),
  ).toHaveLength(1);
});

it("finishes the step once, leaving a running backread to the server", async () => {
  installFetchStub({
    "GET /connectors": rosterWith({
      state: "running",
      counts: { messages_scanned: 12 },
    }),
  });
  const { dispatch, persist } = renderConnectAct("ok");

  await userEvent.click(
    await screen.findByRole("button", { name: "Explore Margince meanwhile" }),
  );

  await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(1));
  expect(dispatch).toHaveBeenCalledWith({ type: "CONNECT_DONE" });
  // The mailbox IS connected on this path, so the connect step was not skipped.
  expect(persist).toHaveBeenCalledWith(
    expect.objectContaining({ connectSkipped: false }),
  );
});

it("finishes the step once when the history read is declined", async () => {
  const starts: unknown[] = [];
  installFetchStub({
    "GET /connectors": rosterWith({ state: "none" }),
    "POST /connectors/gmail/backfill/preview": () =>
      jsonResponse({
        window: "6m",
        estimated_messages: 900,
        computed_at: "2026-07-31T10:00:00Z",
      }),
    "POST /connectors/gmail/backfill": (body) => {
      starts.push(body);
      return jsonResponse({ state: "queued" }, 202);
    },
  });
  const { dispatch } = renderConnectAct("ok");

  await userEvent.click(
    await screen.findByRole("button", { name: "Do not read history now" }),
  );

  await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(1));
  expect(dispatch).toHaveBeenCalledWith({ type: "CONNECT_DONE" });
  expect(starts).toEqual([]);
});

// "Skip connecting" is offered while connecting is still the open question.
it("offers to skip connecting before any consent round trip", () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  renderConnectAct();
  expect(
    screen.getByRole("button", { name: "Skip connecting for now" }),
  ).toBeTruthy();
});

// After a successful consent it is no longer true, and recording the step as
// skipped would persist a fact contradicted by the roster.
it("stops offering to skip connecting once consent has returned", async () => {
  installFetchStub({
    "GET /connectors": rosterWith({ state: "done" }),
  });
  renderConnectAct("ok");
  await screen.findByText("Live and capturing");
  expect(
    screen.queryByRole("button", { name: "Skip connecting for now" }),
  ).toBeNull();
});

// A returning "ok" whose provider the roster never confirms is NOT a
// completed connection — the panel's own "Enter Margince" fallback would
// otherwise be the only way out of a mailbox that is not actually connected.
// The honest exit (skip, recorded truthfully) has to stay reachable until a
// live mailbox is confirmed.
it("keeps the skip exit open when consent returned but no mailbox could be confirmed", async () => {
  installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
  renderConnectAct("ok");
  await screen.findByText("We couldn't confirm the connection.");
  expect(
    screen.getByRole("button", { name: "Skip connecting for now" }),
  ).toBeInTheDocument();
});

// LinkedIn lives beside mail on the same screen now: connecting or skipping
// it never touches the mail flow above, and neither dispatch is CONNECT_DONE
// or a wizard-state write — the mail gate owns those exclusively.
describe("the LinkedIn card", () => {
  beforeEach(() => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
      "PUT /me/linkedin-account": () =>
        jsonResponse({ connected: true, connections: 0 }),
    });
  });

  it("keeps the authorization form closed until its card is clicked", () => {
    renderConnectAct();
    expect(
      screen.queryByRole("button", { name: "Authorize with LinkedIn" }),
    ).toBeNull();
  });

  it("will not authorize until the profile it attributes the network to is given", async () => {
    const { dispatch } = renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));

    // The ask opens as its own dialog, same as a mail provider's.
    expect(await screen.findByRole("dialog")).toBeTruthy();
    const button = screen.getByRole("button", {
      name: "Authorize with LinkedIn",
    });
    expect(button).toBeDisabled();
    // The disclosure a member is owed before handing over their address
    // book: exactly what is read, and the promise it never becomes a contact.
    expect(screen.getByText(en["ob.conv.linkedin.scope1Rest"])).toBeTruthy();
    expect(screen.getByText(en["ob.conv.linkedin.neverContacts"])).toBeTruthy();

    await userEvent.type(
      screen.getByLabelText("Your LinkedIn profile URL"),
      "https://www.linkedin.com/in/lars",
    );
    expect(button).not.toBeDisabled();
    await userEvent.click(button);

    await waitFor(() =>
      expect(dispatch).toHaveBeenCalledWith({
        type: "LINKEDIN_CONNECTED",
        profile: "https://www.linkedin.com/in/lars",
      }),
    );
  });

  it("can be declined from the open dialog, without a profile", async () => {
    const { dispatch } = renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "Skip LinkedIn for now" }),
    );
    expect(dispatch).toHaveBeenCalledWith({ type: "LINKEDIN_SKIPPED" });
  });

  it("admits that nothing syncs yet once the dialog is open", async () => {
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    expect(screen.getByText(en["ob.conv.linkedin.appPending"])).toBeTruthy();
  });

  // The three consent guarantees the dialog owes a reader before they click
  // Authorize, each proven on the RENDERED dialog: the sentences come from the
  // catalog so a rewording is free, but a guarantee that stops being rendered
  // at all still fails here rather than in review.
  it("states that connections never become contacts", async () => {
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    expect(screen.getByText(en["ob.conv.linkedin.neverContacts"])).toBeTruthy();
  });

  it("states that nothing is sent to a connection", async () => {
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    expect(screen.getByText(en["ob.conv.linkedin.scope4Rest"])).toBeTruthy();
  });

  it("states that no connections sync yet", async () => {
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    expect(screen.getByText(en["ob.conv.linkedin.appPending"])).toBeTruthy();
  });

  it("shows the resolved state and offers no further action once skipped", () => {
    renderConnectAct(undefined, "skipped");
    expect(screen.getByText("Skipped: add it later in Settings")).toBeTruthy();
    expect(screen.getByRole("button", { name: /LinkedIn/ })).toBeDisabled();
  });

  it("shows the resolved state and offers no further action once connected", () => {
    renderConnectAct(undefined, "connected");
    expect(screen.getByText("Connected")).toBeTruthy();
    expect(screen.getByRole("button", { name: /LinkedIn/ })).toBeDisabled();
  });

  // A failed authorization must stay visible and retryable, not vanish
  // behind a dialog that already closed on the click that failed.
  it("keeps the dialog open and shows the failure when authorization fails", async () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
      "PUT /me/linkedin-account": () =>
        jsonResponse({ detail: "LinkedIn refused the profile." }, 422),
    });
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    await userEvent.type(
      screen.getByLabelText("Your LinkedIn profile URL"),
      "https://www.linkedin.com/in/lars",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Authorize with LinkedIn" }),
    );

    expect(
      await screen.findByText("LinkedIn refused the profile."),
    ).toBeInTheDocument();
    // Still open and retryable — not silently dismissed on the failed click.
    expect(
      screen.getByRole("button", { name: "Authorize with LinkedIn" }),
    ).toBeInTheDocument();
  });
});

// The finish gate is read and acted on in the same place: the work surface
// the reader has been looking at, not a chip surfaced beside the transcript.
describe("the cn.done finish action", () => {
  it("renders on the connect surface, in its own pinned foot — never as a thread chip", () => {
    installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
    renderConnectAct(undefined, "pending", "cn.done");
    const enter = screen.getByRole("button", { name: "Enter Margince" });
    expect(enter.closest(".ob-triage-continue")).toBeTruthy();
    expect(enter.closest(".mw-thread")).toBeNull();
  });
});

// The four step-level consent guarantees used to be a two-column table
// squeezed into the rail's ~250px column, wrapping into broken-looking text.
// They now render on the artifact surface, where the reader actually passes
// through them before authorizing anything — and OPEN, which is the part that
// matters: they are foldable so a returning reader can move past them, never
// so a first-time reader has to go looking for them.
describe("the consent guarantees", () => {
  it("render open on the surface before any provider dialog opens, not in the rail", () => {
    installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
    renderConnectAct();

    for (const key of [
      "ob.s4.scope1Lead",
      "ob.s4.scope2Lead",
      "ob.s4.scope3Lead",
      "ob.s4.scope4Lead",
    ] as const) {
      const found = screen.getByText(en[key]);
      const fold = found.closest("details");
      expect(fold?.open).toBe(true);
      expect(found.closest(".mw-thread")).toBeNull();
    }
  });
});

describe("the IMAP dialog", () => {
  it("carries only the real contract's fields — no invented SMTP host, port or TLS toggle", async () => {
    installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /Any inbox/ }));

    expect(await screen.findByRole("dialog")).toBeTruthy();
    expect(screen.getByLabelText("IMAP host")).toBeTruthy();
    expect(screen.getByLabelText("Port")).toBeTruthy();
    expect(screen.getByLabelText("Email")).toBeTruthy();
    expect(screen.getByLabelText("App password")).toBeTruthy();
    expect(screen.getByLabelText("Mailbox")).toBeTruthy();
    // The prototype this dialog adapts shows SMTP host/port and a "Require
    // TLS" checkbox; the real connector contract has neither, so this form
    // does not invent widgets that would submit nothing.
    expect(screen.queryByLabelText(/smtp/i)).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("closes on 'Not now' without touching the required-step skip", async () => {
    installFetchStub({ "GET /connectors": () => jsonResponse({ data: [] }) });
    const { dispatch, persist } = renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /Any inbox/ }));
    await userEvent.click(screen.getByRole("button", { name: "Not now" }));

    expect(screen.queryByRole("dialog")).toBeNull();
    // Backing out of ONE provider's ask is not the same decision as skipping
    // the whole required mailbox step — neither fires here.
    expect(dispatch).not.toHaveBeenCalled();
    expect(persist).not.toHaveBeenCalled();
    // The card grid is still there, ready for a different pick.
    expect(screen.getByRole("button", { name: /Microsoft/ })).toBeTruthy();
  });
});

// The disabled "Not now"/"Skip" buttons only guard their own click. The
// dialog's other dismissal routes — X, Escape, backdrop — all resolve to the
// SAME close handler (`ConnectDialog` → `Modal`), so a success landing after
// one of those already backed the reader out would leave a connection stored
// against a decision the panel already promised. Each provider serializes
// its own decision by keeping that one handler from acting while its own
// save is in flight.
describe("dismissal during an in-flight connect request", () => {
  it("keeps the OAuth dialog open against Escape while its connect POST is pending", async () => {
    const deferred: { resolve: ((r: Response) => void) | null } = {
      resolve: null,
    };
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
      "POST /connectors/graph/connect": () =>
        new Promise((resolve) => {
          deferred.resolve = resolve;
        }),
    });
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /Microsoft/ }));
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Allow access to my Microsoft",
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Not now" })).toBeDisabled(),
    );

    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeTruthy();

    deferred.resolve?.(jsonResponse({}));
  });

  it("keeps the IMAP dialog open against Escape while its connect POST is pending", async () => {
    const deferred: { resolve: ((r: Response) => void) | null } = {
      resolve: null,
    };
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
      "POST /connectors/imap/connect": () =>
        new Promise((resolve) => {
          deferred.resolve = resolve;
        }),
    });
    renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /Any inbox/ }));
    await userEvent.type(screen.getByLabelText("Email"), "me@example.com");
    await userEvent.type(screen.getByLabelText("App password"), "secret");
    await userEvent.click(
      screen.getByRole("button", { name: "Test and connect" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Not now" })).toBeDisabled(),
    );

    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeTruthy();

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
  });

  // LinkedIn's skip and its save race the same way mail's dismiss does: a
  // successful PUT landing after a skip already dispatched would leave the
  // account connected on the server against a machine state that says
  // skipped, with no way to tell the later LINKEDIN_CONNECTED dispatch apart
  // from a stale one.
  it("keeps the LinkedIn dialog open and Skip disabled while its save is pending", async () => {
    const deferred: { resolve: ((r: Response) => void) | null } = {
      resolve: null,
    };
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
      "PUT /me/linkedin-account": () =>
        new Promise((resolve) => {
          deferred.resolve = resolve;
        }),
    });
    const { dispatch } = renderConnectAct();
    await userEvent.click(screen.getByRole("button", { name: /LinkedIn/ }));
    await userEvent.type(
      screen.getByLabelText("Your LinkedIn profile URL"),
      "https://www.linkedin.com/in/lars",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Authorize with LinkedIn" }),
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Skip LinkedIn for now" }),
      ).toBeDisabled(),
    );

    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(dispatch).not.toHaveBeenCalledWith({ type: "LINKEDIN_SKIPPED" });

    deferred.resolve?.(jsonResponse({ connected: true, connections: 0 }));
    await waitFor(() =>
      expect(dispatch).toHaveBeenCalledWith({
        type: "LINKEDIN_CONNECTED",
        profile: "https://www.linkedin.com/in/lars",
      }),
    );
  });
});

// A roster invalidation (IMAP's own successful connect fires one) puts the
// query back into flight exactly like the first load did — the "pick one"
// rule the cards enforce cannot tell that refetch apart from a first read
// still pending, so both must withhold every card, not only the first.
it("keeps mail provider cards disabled during a roster refetch, not just its first load", async () => {
  const deferred: { resolve: ((r: Response) => void) | null } = {
    resolve: null,
  };
  let rosterCalls = 0;
  installFetchStub({
    "GET /connectors": () => {
      rosterCalls += 1;
      if (rosterCalls === 1) {
        return jsonResponse({ data: [] });
      }
      return new Promise((resolve) => {
        deferred.resolve = resolve;
      });
    },
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
  renderConnectAct();
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /Google/ })).not.toBeDisabled(),
  );

  await userEvent.click(screen.getByRole("button", { name: /Any inbox/ }));
  await userEvent.type(screen.getByLabelText("Email"), "me@example.com");
  await userEvent.type(screen.getByLabelText("App password"), "secret");
  await userEvent.click(
    screen.getByRole("button", { name: "Test and connect" }),
  );
  await screen.findByText(/mailbox connected/i);

  // The connect just invalidated the shared roster query; its refetch (call
  // #2, deferred above) is in flight, so Google/Microsoft — still, on the
  // stale data, unconnected — must not be openable until it actually reports
  // back.
  expect(screen.getByRole("button", { name: /Google/ })).toBeDisabled();
  expect(screen.getByRole("button", { name: /Microsoft/ })).toBeDisabled();

  deferred.resolve?.(
    jsonResponse({
      data: [
        {
          id: "c1",
          provider: "imap",
          status: "connected",
          scopes: [],
          backfill: { state: "none" },
        },
      ],
    }),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /Google/ })).toBeDisabled(),
  );
});

// A failed roster read is the ONE mail-gate failure with no other surface to
// explain it: every card renders disabled either way, so a reader facing
// them with no failed read has nothing to act on and no way to tell it apart
// from an ordinary still-loading moment.
it("says why every mail card is disabled when the roster read fails, and offers a retry", async () => {
  let rosterCalls = 0;
  installFetchStub({
    "GET /connectors": () => {
      rosterCalls += 1;
      if (rosterCalls === 1) {
        return jsonResponse({ code: "internal" }, 500);
      }
      return jsonResponse({ data: [] });
    },
  });
  renderConnectAct();

  expect(
    await screen.findByText("Could not check your mailboxes"),
  ).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /Google/ })).toBeDisabled();

  await userEvent.click(screen.getByRole("button", { name: "Retry" }));

  await waitFor(() =>
    expect(screen.getByRole("button", { name: /Google/ })).not.toBeDisabled(),
  );
  expect(
    screen.queryByText("Could not check your mailboxes"),
  ).not.toBeInTheDocument();
});
