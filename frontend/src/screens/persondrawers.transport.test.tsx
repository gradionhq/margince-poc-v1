/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PersonComposer } from "./persondrawers";
import { installFetchStub, jsonResponse, meRoute } from "./story-utils";

// Which way a message leaves, and when the composer bothers to ask.
//
// The rule under test is adaptive rather than fixed: a contact reachable one
// way gets the composer they have always had, and the choice appears only when
// there is a choice to make. A picker that always rendered would put a
// one-option control on every mail-only contact in the installation.
//
// The second rule is what a channel composer must NOT show. Mail's subject,
// bcc, send-later and AI draft are each absent for their own reason — a
// messaging channel has no subject line, send-message takes no schedule, and
// the model lane drafts mail only — so a channel composer that rendered them
// would be offering fields the send cannot carry.

type View = components["schemas"]["Person360"];
type Activity = components["schemas"]["Activity"];

const anEmail = {
  id: "pe-1",
  person_id: "p-1",
  email: "dana@brandt.example",
  email_type: "work" as const,
  is_primary: true,
  position: 0,
  source: "manual",
  captured_by: "human:u1",
};

function aMessage(provider: string, at: string, id: string): Activity {
  return {
    id,
    kind: "message",
    channel_provider: provider,
    direction: "inbound",
    body: "they wrote",
    occurred_at: at,
    created_at: at,
    updated_at: at,
    is_done: false,
    source: "ext:dispact-connector:dispact",
    version: 1,
  } as Activity;
}

// A view with exactly the reachability and conversations a case needs. Every
// field the composer reads is stated; nothing is inherited from a fixture that
// might change for another test's reasons.
function viewWith(
  options: Readonly<{
    email?: boolean;
    reachable?: { provider: string; reachable: boolean }[];
    activities?: Activity[];
  }>,
): View {
  return {
    as_of: "2026-08-15T09:00:00Z",
    person: {
      id: "p-1",
      full_name: "Dana Buyer",
      emails: options.email ? [anEmail] : [],
      reachability: (options.reachable ?? []).map((entry) => ({
        ...entry,
        since: "2026-08-15T08:00:00Z",
      })),
    },
    activities: { data: options.activities ?? [] },
    sections_omitted: [],
  } as unknown as View;
}

function render(view: View) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const Wrapper = ({ children }: Readonly<{ children: ReactNode }>) => (
    <QueryClientProvider client={client}>
      <LocaleProvider>{children}</LocaleProvider>
    </QueryClientProvider>
  );
  return rtlRender(
    <PersonComposer
      personId="p-1"
      view={view}
      guard={undefined}
      open={true}
      onClose={() => {}}
    />,
    { wrapper: Wrapper },
  );
}

describe("the person composer's transport choice", () => {
  beforeEach(() => {
    installFetchStub({
      "GET /me": meRoute({}),
      "GET /consent-purposes": () => jsonResponse({ data: [] }),
      "GET /channel-providers": () =>
        jsonResponse({
          data: [
            { provider: "dispact", label: "Dispact", supplies_transport: true },
          ],
        }),
    });
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  // The common contact: one address, no channel. Nothing about the composer
  // changes, which is the whole point of making the picker conditional.
  it("asks nothing when the only way to reach them is mail", () => {
    render(viewWith({ email: true }));

    expect(screen.queryByLabelText("How to send")).toBeNull();
    // And it is still the mail composer: the subject line is the cheapest
    // proof, since a channel composer withholds it.
    expect(screen.getByLabelText("Subject")).toBeTruthy();
  });

  // Two ways to reach them, so the rep decides. Inferring it from what they
  // typed would be guessing at the one choice that cannot be undone.
  it("offers the choice when there is one to make", async () => {
    render(
      viewWith({
        email: true,
        reachable: [{ provider: "dispact", reachable: true }],
        activities: [aMessage("dispact", "2026-08-15T08:00:00Z", "a-1")],
      }),
    );

    // The picker exists, which is the rule under test. Its options live in a
    // listbox that opens on click, so this asserts the CONTROL rather than
    // driving it open — what the two options are is settled by the two
    // withholding cases below, which prove the list is built from
    // reachability and anchors rather than from everything.
    const picker = await screen.findByLabelText("How to send");
    expect(picker).toBeTruthy();
    // Mail leads, because it is the transport that can open a conversation.
    expect(screen.getByText("Email")).toBeTruthy();
  });

  // The contact this whole change exists for: no address anywhere, and a
  // conversation on a unit-supplied transport. Before this, the composer
  // offered mail to somebody who has no mailbox.
  it("opens on the channel when that is the only way to reach them", async () => {
    render(
      viewWith({
        reachable: [{ provider: "dispact", reachable: true }],
        activities: [aMessage("dispact", "2026-08-15T08:00:00Z", "a-1")],
      }),
    );

    // The recipient names the conversation rather than an address, because the
    // server resolves it from the conversation being answered.
    expect(
      await screen.findByDisplayValue("Continues your Dispact conversation"),
    ).toBeTruthy();
    // One transport, so no picker — and it is the CHANNEL composer, which
    // withholds every field mail has and a channel does not.
    expect(screen.queryByLabelText("How to send")).toBeNull();
    expect(screen.queryByLabelText("Subject")).toBeNull();
    expect(screen.queryByLabelText("Bcc")).toBeNull();
    expect(screen.queryByLabelText("Send later (optional)")).toBeNull();
  });

  // Reachability and an anchor answer different questions, and BOTH are
  // required. A live identity with no conversation has nothing to continue —
  // there is no endpoint that opens one — so offering it would be a choice
  // that fails at the send.
  it("withholds a transport the person is reachable on but has no conversation on", () => {
    render(
      viewWith({
        email: true,
        reachable: [{ provider: "dispact", reachable: true }],
      }),
    );

    expect(screen.queryByLabelText("How to send")).toBeNull();
    expect(screen.queryByText("Dispact")).toBeNull();
  });

  // The mirror: a conversation exists, but the identity is blocked or archived.
  // A reply staged against it would be refused at the send, so the composer
  // does not offer the transport at all.
  it("withholds a transport whose identity is no longer reachable", () => {
    render(
      viewWith({
        email: true,
        reachable: [{ provider: "dispact", reachable: false }],
        activities: [aMessage("dispact", "2026-08-15T08:00:00Z", "a-1")],
      }),
    );

    expect(screen.queryByLabelText("How to send")).toBeNull();
    expect(screen.queryByText("Dispact")).toBeNull();
  });
});
