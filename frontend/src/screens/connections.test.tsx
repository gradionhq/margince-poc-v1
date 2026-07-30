/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ConnectionsCard, layout } from "./connections";

// The connections card's own rules:
//
//   - the diagram is decorative, so everything it draws is also in the list —
//     a reader without the picture loses nothing;
//   - a withheld group says so, and never draws as an account with no
//     contacts;
//   - a capped graph names what it left out;
//   - the layout is deterministic, because a picture that moves on every read
//     is one nobody learns to read.

const ROOT = "org-root";

// node is one graph node with the two fields the contract always carries, so a
// fixture states only what the case under test is about.
function node(overrides: Record<string, unknown>) {
  return {
    id: "n",
    kind: "person" as const,
    label: "Node",
    root: false,
    ...overrides,
  };
}

function graph(overrides: Record<string, unknown> = {}) {
  return {
    as_of: "2026-06-01T09:00:00Z",
    root_id: ROOT,
    nodes: [
      node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
      node({
        id: "p-1",
        kind: "person",
        label: "Dana Buyer",
        detail: "CTO",
        strength: 71,
        strength_bucket: "strong",
      }),
      node({ id: "d-1", kind: "deal", label: "Renewal", detail: "Proposal" }),
    ],
    edges: [
      { from: ROOT, to: "p-1", kind: "employment", role: "cto" },
      { from: ROOT, to: "d-1", kind: "has_deal" },
      { from: "d-1", to: "p-1", kind: "deal_stakeholder", role: "champion" },
    ],
    dropped_count: 0,
    groups_omitted: [],
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

// The card renders EntityRef per node, and each one reads the record it names
// to resolve a display name. The stub answers those too, so a test asserting
// on names is asserting on the card rather than on a pending fetch.
function stub(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      if (pathname.endsWith("/graph")) {
        return jsonResponse(body, status);
      }
      if (pathname.endsWith("/people/p-1")) {
        return jsonResponse({ id: "p-1", full_name: "Dana Buyer" });
      }
      if (pathname.endsWith("/deals/d-1")) {
        return jsonResponse({ id: "d-1", name: "Renewal" });
      }
      if (pathname.endsWith("/organizations/org-parent")) {
        return jsonResponse({ id: "org-parent", display_name: "Holding" });
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

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

describe("connections card", () => {
  it("lists every node the diagram draws, so the picture is never the only way in", async () => {
    stub(graph());
    render(<ConnectionsCard orgId={ROOT} />);

    const list = await screen.findByRole("list");
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    await waitFor(() => {
      expect(within(list).getByText("Dana Buyer")).toBeTruthy();
    });
    expect(within(list).getByText("Renewal")).toBeTruthy();
    // The account itself is not a row: it is the record the reader is on, and
    // a link back to the current page is a dead end.
    expect(within(list).queryByText("Brandt")).toBeNull();
  });

  it("hides the diagram from assistive technology, because the list is the content", async () => {
    stub(graph());
    const { container } = render(<ConnectionsCard orgId={ROOT} />);
    await screen.findByRole("list");

    const svg = container.querySelector("svg.cx-diagram");
    expect(svg).toBeTruthy();
    expect(svg?.getAttribute("aria-hidden")).toBe("true");
    // Nothing inside the picture is focusable or clickable: a decorative
    // element that takes a tab stop is a trap with nothing behind it.
    expect(svg?.querySelectorAll("a, button, [tabindex]")).toHaveLength(0);
  });

  it("draws one line per edge, including the one that does not start at the account", async () => {
    stub(graph());
    const { container } = render(<ConnectionsCard orgId={ROOT} />);
    await screen.findByRole("list");

    expect(container.querySelectorAll("svg.cx-diagram line")).toHaveLength(3);
    expect(
      container.querySelectorAll("line.cx-edge-deal_stakeholder"),
    ).toHaveLength(1);
  });

  it("names a withheld group rather than drawing an account with no contacts", async () => {
    stub(
      graph({
        nodes: [
          node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
          node({ id: "d-1", kind: "deal", label: "Renewal" }),
        ],
        edges: [{ from: ROOT, to: "d-1", kind: "has_deal" }],
        groups_omitted: ["contacts", "intro_path"],
      }),
    );
    render(<ConnectionsCard orgId={ROOT} />);

    expect(
      await screen.findByText("Hidden from you: contacts, the warm intro"),
    ).toBeTruthy();
    // The empty state must not appear alongside it: the account HAS nodes, and
    // "nothing linked" would be a claim about the part nobody answered for.
    expect(
      screen.queryByText("Nothing linked to this account yet."),
    ).toBeNull();
  });

  it("says an empty account is empty — but only when the read succeeded", async () => {
    stub(
      graph({
        nodes: [
          node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
        ],
        edges: [],
      }),
    );
    render(<ConnectionsCard orgId={ROOT} />);

    expect(
      await screen.findByText("Nothing linked to this account yet."),
    ).toBeTruthy();
  });

  it("reports a failed read as unavailable, never as an empty account", async () => {
    stub({ title: "boom" }, 500);
    render(<ConnectionsCard orgId={ROOT} />);

    expect(
      await screen.findByText(
        "Could not be loaded — this may not be the whole picture",
      ),
    ).toBeTruthy();
    expect(
      screen.queryByText("Nothing linked to this account yet."),
    ).toBeNull();
  });

  it("names what the caps left out, so a top slice never reads as the whole neighbourhood", async () => {
    stub(graph({ dropped_count: 4 }));
    render(<ConnectionsCard orgId={ROOT} />);

    expect(await screen.findByText("4 more not shown here.")).toBeTruthy();
  });

  it("marks the route in on the node the server named", async () => {
    stub(
      graph({
        nodes: [
          node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
          node({
            id: "p-1",
            kind: "person",
            label: "Dana Buyer",
            strength: 71,
            strength_bucket: "strong",
            intro_path: true,
          }),
        ],
        edges: [{ from: ROOT, to: "p-1", kind: "employment" }],
        intro_path: { signal_id: "s-1", contact_id: "p-1" },
      }),
    );
    const { container } = render(<ConnectionsCard orgId={ROOT} />);

    expect(await screen.findByText("Route in")).toBeTruthy();
    expect(container.querySelectorAll("circle.cx-node-intro")).toHaveLength(1);
  });

  it("opens the same graph in a wide dialog", async () => {
    stub(graph());
    render(<ConnectionsCard orgId={ROOT} />);
    const expand = await screen.findByRole("button", { name: "See it larger" });

    expand.click();

    const dialog = await screen.findByRole("dialog", { name: "Connections" });
    expect(dialog.className).toContain("modal-wide");
    // The dialog carries the list too, not just a bigger picture: the reader
    // who needed the list in the rail still needs it here.
    expect(within(dialog).getAllByRole("listitem")).toHaveLength(2);
  });
});

describe("connections layout", () => {
  it("puts the account at the centre and the neighbours on the ring", () => {
    const placed = layout(graph().nodes);

    expect(placed).toHaveLength(3);
    const [centre, first] = placed;
    expect(centre.node.id).toBe(ROOT);
    expect(centre.x).toBe(centre.y);
    // The first neighbour sits at twelve o'clock: same x as the centre,
    // directly above it.
    expect(first.x).toBeCloseTo(centre.x);
    expect(first.y).toBeLessThan(centre.y);
  });

  it("is deterministic — the same payload lays out identically twice", () => {
    const once = layout(graph().nodes);
    const twice = layout(graph().nodes);

    expect(twice).toEqual(once);
  });

  it("places nothing when the payload carries no root", () => {
    const placed = layout([node({ id: "p-1" })]);

    // A ring node still gets a point; the centre simply has no occupant. A
    // fabricated root would draw a company the payload never named.
    expect(placed).toHaveLength(1);
    expect(placed[0].node.id).toBe("p-1");
  });
});
