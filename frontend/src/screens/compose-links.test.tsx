/** @vitest-environment jsdom */
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
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { ComposeModal } from "./compose";

// What a sent message files under.
//
// The grounding controls and the attribution are one statement: a rep who picks
// "Related to → Acme Renewal" has said what the message is about, and a send
// that files only under the page's own record throws that away. These tests
// assert the request body, because the links array IS the contract.

type Sent = { key: string; body: unknown };

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const PURPOSES = {
  data: [
    {
      id: "p1",
      key: "transactional",
      label: "Deal messages",
      requires_double_opt_in: false,
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
};

// The account view the grounding selects are populated from: one contact and
// two open deals, so a pick is a real choice rather than the only option.
const ORG_VIEW = {
  organization: { id: "org-1", name: "Acme" },
  people: {
    data: [
      { person_id: "per-1", full_name: "Dieter Klein" },
      { person_id: "per-2", full_name: "Sara Vogel" },
    ],
  },
  deals: {
    data: [
      { deal_id: "deal-1", name: "Acme Renewal" },
      { deal_id: "deal-2", name: "Acme Expansion" },
    ],
  },
};

const SENT_ACTIVITY = {
  id: "act-9",
  kind: "email",
  subject: "Hello",
  occurred_at: "2026-07-01T00:00:00Z",
  is_done: false,
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
};

function stubRoutes(
  overrides: Record<string, () => Response | Promise<Response>> = {},
) {
  const sent: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.clone().json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      sent.push({ key, body });
      const override = overrides[key];
      if (override) return override();
      if (key === "GET /consent-purposes") return jsonResponse(PURPOSES);
      if (key === "GET /voice-profiles") return jsonResponse({ data: [] });
      if (key === "GET /organizations/org-1/360") return jsonResponse(ORG_VIEW);
      return jsonResponse({});
    }),
  );
  return sent;
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// The account-started composer carries three dropdowns (recipient, deal,
// purpose), so each pick names the one it means rather than taking the only
// combobox on screen.
async function pickBy(labelText: string, option: string) {
  const select = screen.getByLabelText(labelText);
  await pickOption(userEvent.setup(), select, option);
}

async function fillBody() {
  await userEvent.type(screen.getByLabelText("To"), "dieter@acme.test");
  await userEvent.tab();
  await userEvent.type(screen.getByPlaceholderText("Subject"), "Hello");
  await userEvent.type(screen.getByPlaceholderText("Body"), "Body content");
}

function linksOf(sent: Sent[]) {
  const req = sent.find((r) => r.key === "POST /emails");
  return (req?.body as { links?: unknown[] } | undefined)?.links;
}

describe("what a sent message files under", () => {
  it("sends the deal the rep grounded the draft in, not only the page", async () => {
    const sent = stubRoutes({
      "POST /emails": () => jsonResponse(SENT_ACTIVITY, 202),
    });
    render(
      <ComposeModal
        entityType="organization"
        entityId="org-1"
        personId="per-1"
        open
        onClose={vi.fn()}
      />,
    );

    await screen.findByLabelText("Related to");
    await pickBy("Related to", "Acme Renewal");
    await fillBody();
    await pickBy("Consent purpose", "Deal messages");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(linksOf(sent)).toBeDefined());
    // The deal is the assertion that fails without the fix: before it, the
    // send carried the organization alone and the deal's timeline never saw
    // the message the rep wrote about it.
    expect(linksOf(sent)).toEqual([
      { entity_type: "organization", entity_id: "org-1" },
      { entity_type: "person", entity_id: "per-1" },
      { entity_type: "deal", entity_id: "deal-1" },
    ]);
  });

  it("files under the page and the recipient when no deal was chosen", async () => {
    const sent = stubRoutes({
      "POST /emails": () => jsonResponse(SENT_ACTIVITY, 202),
    });
    render(
      <ComposeModal
        entityType="organization"
        entityId="org-1"
        personId="per-1"
        open
        onClose={vi.fn()}
      />,
    );

    await screen.findByLabelText("Related to");
    await fillBody();
    await pickBy("Consent purpose", "Deal messages");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(linksOf(sent)).toBeDefined());
    // An unchosen deal is absent, not an empty entry: "no deal" is a real
    // answer and must not file the message under a blank id.
    expect(linksOf(sent)).toEqual([
      { entity_type: "organization", entity_id: "org-1" },
      { entity_type: "person", entity_id: "per-1" },
    ]);
  });

  it("never repeats a record the page is already anchored on", async () => {
    const sent = stubRoutes({
      "POST /emails": () => jsonResponse(SENT_ACTIVITY, 202),
    });
    render(
      <ComposeModal
        entityType="organization"
        entityId="org-1"
        personId="per-1"
        open
        onClose={vi.fn()}
      />,
    );

    await screen.findByLabelText("Draft to");
    // Re-picking the contact the composer opened on is a no-op the rep can
    // perform, and it must not produce the same person twice on the wire.
    await pickBy("Draft to", "Dieter Klein");
    await fillBody();
    await pickBy("Consent purpose", "Deal messages");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(linksOf(sent)).toBeDefined());
    const ids = (linksOf(sent) as { entity_id: string }[]).map(
      (l) => l.entity_id,
    );
    expect(new Set(ids).size).toBe(ids.length);
  });
});
