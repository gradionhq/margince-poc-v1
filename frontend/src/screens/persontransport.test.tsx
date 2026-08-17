/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { PersonBriefCard } from "./personcards";
import { PersonMemory } from "./personmemory";
import { PersonRail } from "./personrail";
import { installFetchStub, jsonResponse, meRoute } from "./story-utils";

// What the person page says about the transport that carried a message.
//
// Since ADR-0107/A158 the interaction KIND and the transport that carried it
// are separate axes, and every surface here used to read the second off the
// first. On a contact captured over a chat channel — no address, no number —
// that produced a page that claimed mail three times over: an envelope on the
// timeline, "Email thread" under the brief, and a consent block reporting the
// person as reachable by email while showing no row at all for the channel the
// CRM can actually reach them on.
//
// Every assertion below is made in BOTH directions. A page that stopped drawing
// envelopes by drawing nothing would pass a one-directional suite, and a mail
// contact still owes the reader an envelope.

type Person360 = components["schemas"]["Person360"];
type PersonBrief = components["schemas"]["PersonBrief"];
type Activity = components["schemas"]["Activity"];

const AT = "2026-08-15T09:00:00Z";

function anActivity(over: Partial<Activity>): Activity {
  return {
    id: "a-1",
    kind: "email",
    direction: "inbound",
    subject: "Re: the pilot",
    body: "they wrote",
    occurred_at: AT,
    created_at: AT,
    updated_at: AT,
    is_done: false,
    source: "gmail",
    version: 1,
    ...over,
  } as Activity;
}

// A chat message, carried by a provider the directory below knows a name for.
const aChatMessage = anActivity({
  id: "a-chat",
  kind: "message",
  channel_provider: "zalo_oa",
  subject: null,
  body: "họ đã nhắn tin",
  source: "ext:zalo-oa:zalo",
});

// Everything the three surfaces read, and nothing inherited from a fixture that
// might change for another test's reasons.
function viewWith(
  options: Readonly<{
    emails?: boolean;
    phones?: boolean;
    reachability?: { provider: string; reachable: boolean }[];
    activities?: Activity[];
  }>,
): Person360 {
  return {
    as_of: AT,
    person: {
      id: "p-1",
      full_name: "Dana Buyer",
      version: 1,
      emails: options.emails
        ? [{ id: "pe-1", email: "dana@brandt.example", is_primary: true }]
        : [],
      phones: options.phones ? [{ id: "pp-1", phone: "+49 30 111" }] : [],
      reachability: (options.reachability ?? []).map((entry) => ({
        ...entry,
        since: AT,
      })),
    },
    activities: { data: options.activities ?? [] },
    sections_omitted: [],
  } as unknown as Person360;
}

// A brief whose one sentence cites one record — the shape the chip row reads.
function briefCiting(entityType: string, entityId: string): PersonBrief {
  return {
    person_id: "p-1",
    generated_at: AT,
    generated_by: "deterministic",
    sentences: [
      {
        text: "They asked about the pilot.",
        evidence: [{ entity_type: entityType, entity_id: entityId }],
      },
    ],
  } as unknown as PersonBrief;
}

function render(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{node}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// The transport directory, which is what names a provider for a human. It is a
// deployment fact, so the label can only come from here — a switch in a screen
// would be right for whatever this build happens to ship and wrong for the next
// unit somebody drops under extensions/.
beforeEach(() => {
  installFetchStub({
    "GET /me": meRoute({}),
    "GET /channel-providers": () =>
      jsonResponse({
        data: [
          {
            provider: "zalo_oa",
            label: "Zalo OA",
            credential_model: "workspace_bot",
            supplies_transport: true,
          },
        ],
      }),
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

// The transport cell of a timeline row or a brief chip — both draw the icon and
// the name in the same slot. Read by class rather than by text: the assertion is
// about what the cell SAYS, so a query that had to know the answer first could
// only ever confirm itself.
function transportCell(container: HTMLElement): HTMLElement {
  const cell = container.querySelector<HTMLElement>(".pe-memory-channel");
  if (!cell) {
    throw new Error("no transport cell rendered");
  }
  return cell;
}

describe("the conversation timeline", () => {
  it("draws a chat message as a message, named by its transport", async () => {
    const { container } = render(
      <PersonMemory view={viewWith({ activities: [aChatMessage] })} />,
    );
    // The name arrives with the transport directory, which is a fetch.
    await screen.findAllByText("Zalo OA");

    const cell = transportCell(container);
    expect(cell.textContent).toBe("Zalo OA");
    expect(cell.querySelector(".lucide-message-square")).toBeTruthy();
    // The reported symptom: an envelope on a contact with no email address.
    expect(cell.querySelector(".lucide-mail")).toBeNull();
  });

  it("still draws mail as mail", () => {
    const { container } = render(
      <PersonMemory view={viewWith({ activities: [anActivity({})] })} />,
    );

    const cell = transportCell(container);
    expect(cell.textContent).toBe("Email");
    expect(cell.querySelector(".lucide-mail")).toBeTruthy();
    expect(cell.querySelector(".lucide-message-square")).toBeNull();
  });
});

describe("the relationship brief's source chips", () => {
  it("names the transport of a cited chat message", async () => {
    const { container } = render(
      <PersonBriefCard
        brief={briefCiting("activity", "a-chat")}
        loading={false}
        view={viewWith({ activities: [aChatMessage] })}
      />,
    );
    await screen.findByText("Zalo OA");

    const chip = transportCell(container);
    expect(chip.textContent).toBe("Zalo OA");
    expect(chip.querySelector(".lucide-message-square")).toBeTruthy();
    expect(chip.querySelector(".lucide-mail")).toBeNull();
  });

  it("names a cited mail thread as mail", () => {
    const { container } = render(
      <PersonBriefCard
        brief={briefCiting("activity", "a-1")}
        loading={false}
        view={viewWith({ activities: [anActivity({})] })}
      />,
    );

    const chip = transportCell(container);
    expect(chip.textContent).toBe("Email");
    expect(chip.querySelector(".lucide-mail")).toBeTruthy();
  });

  // The brief cites what the WRITER read, which is not bounded by the page of
  // activities this reader was handed. A citation the page cannot resolve is
  // named for the one thing it certainly is.
  it("claims no transport for a citation it cannot resolve", () => {
    const { container } = render(
      <PersonBriefCard
        brief={briefCiting("activity", "a-not-on-this-page")}
        loading={false}
        view={viewWith({ activities: [aChatMessage] })}
      />,
    );

    const chip = transportCell(container);
    expect(chip.textContent).toBe("Conversation");
    expect(chip.querySelector(".lucide-mail")).toBeNull();
    expect(chip.querySelector(".lucide-message-square")).toBeNull();
  });
});

describe("consent and channels", () => {
  function renderRail(view: Person360) {
    render(
      <PersonRail
        view={view}
        guard={
          {
            person_id: "p-1",
            entries: [
              {
                purpose_key: "business_correspondence",
                purpose_class: "business_correspondence",
                channel: "email",
                verdict: "allowed",
                reason: "she wrote to you on 15 Aug",
              },
            ],
          } as unknown as components["schemas"]["PersonConsentGuard"]
        }
        firstName="Dana"
        onExplain={() => {}}
      />,
    );
    return within(screen.getByTestId("person-rail"));
  }

  it("asserts no verdict for a transport the record does not carry", () => {
    const rail = renderRail(
      viewWith({
        reachability: [{ provider: "zalo_oa", reachable: true }],
        activities: [aChatMessage],
      }),
    );

    // The reported symptom: "Email — Allowed" on a contact with no address.
    expect(rail.getByText("No address on file")).toBeTruthy();
    expect(rail.getByText("No number on file")).toBeTruthy();
  });

  it("gives the channel they are actually reached on its own row", async () => {
    const rail = renderRail(
      viewWith({
        reachability: [{ provider: "zalo_oa", reachable: true }],
        activities: [aChatMessage],
      }),
    );

    // Named by the directory and carrying the correspondence verdict, which the
    // send gate answers per purpose and not per transport.
    expect(await rail.findByText("Zalo OA")).toBeTruthy();
    expect(rail.getByText("Allowed")).toBeTruthy();
  });

  it("keeps the row when the identity can no longer be delivered to", async () => {
    const rail = renderRail(
      viewWith({
        reachability: [{ provider: "zalo_oa", reachable: false }],
        activities: [aChatMessage],
      }),
    );

    expect(await rail.findByText("Zalo OA")).toBeTruthy();
    expect(rail.getByText("Not deliverable")).toBeTruthy();
    // A blocked channel must not read as a granted one.
    expect(rail.queryByText("Allowed")).toBeNull();
  });

  it("still reports the verdict for a contact who has an address", () => {
    const rail = renderRail(
      viewWith({ emails: true, activities: [anActivity({})] }),
    );

    expect(rail.getByText("Allowed")).toBeTruthy();
    expect(rail.queryByText("No address on file")).toBeNull();
    expect(rail.getByText("she wrote to you on 15 Aug")).toBeTruthy();
  });
});
