/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ConnectionsCard, layout, relationKeys } from "./connections";

// The connections card's own rules:
//
//   - the diagram is decorative, so everything it draws is also in the list —
//     a reader without the picture loses nothing;
//   - a withheld group says so, and never draws as an account with no
//     contacts;
//   - a capped graph names what it left out;
//   - the layout is deterministic, because a picture that moves on every read
//     is one nobody learns to read;
//   - every row says HOW its record attaches, because the edge meaning is
//     otherwise only in the picture.

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
      { from: ROOT, to: "p-1", kind: "employment" as const, role: "cto" },
      { from: ROOT, to: "d-1", kind: "has_deal" as const },
      {
        from: "d-1",
        to: "p-1",
        kind: "deal_stakeholder" as const,
        role: "champion",
      },
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

// The card names every node from the graph payload's own label, so the ONLY
// request a test should see is the graph read. The stub still answers a record
// read if one is made — and `fetched` is how a test proves none was.
function stub(body: unknown, status = 200) {
  const fetched: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const pathname = new URL(request.url).pathname;
      fetched.push(pathname);
      if (pathname.endsWith("/graph")) {
        return jsonResponse(body, status);
      }
      return jsonResponse({ data: [], page: { has_more: false } });
    }),
  );
  return fetched;
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
    expect(within(list).getByText("Dana Buyer")).toBeTruthy();
    expect(within(list).getByText("Renewal")).toBeTruthy();
    // The account itself is not a row: it is the record the reader is on, and
    // a link back to the current page is a dead end.
    expect(within(list).queryByText("Brandt")).toBeNull();
  });

  it("names its nodes from the payload, without a request per node", async () => {
    const fetched = stub(graph());
    render(<ConnectionsCard orgId={ROOT} />);

    const list = await screen.findByRole("list");
    // Every name is already on screen — no waitFor, because nothing is in
    // flight to wait for.
    expect(within(list).getByText("Dana Buyer")).toBeTruthy();
    expect(within(list).getByText("Renewal")).toBeTruthy();
    // One request for the whole card. A per-node record read would be an N+1
    // fan-out for names this payload already carried, and each row would show
    // its raw uuid until the read landed.
    await waitFor(() => {
      expect(fetched.filter((path) => path.endsWith("/graph"))).toHaveLength(1);
    });
    expect(fetched.filter((path) => !path.endsWith("/graph"))).toEqual([]);
  });

  it("falls back to the id when the payload carries no label", async () => {
    stub(
      graph({
        nodes: [
          node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
          node({ id: "p-1", kind: "person", label: "" }),
        ],
        edges: [{ from: ROOT, to: "p-1", kind: "employment" as const }],
      }),
    );
    render(<ConnectionsCard orgId={ROOT} />);

    const list = await screen.findByRole("list");
    // An unnamed record reads as its id, never as a blank row or a dead link.
    expect(within(list).getByTitle("p-1").textContent).toBe("p-1");
    expect(within(list).queryByRole("button", { name: "" })).toBeNull();
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
        edges: [{ from: ROOT, to: "d-1", kind: "has_deal" as const }],
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
        edges: [{ from: ROOT, to: "p-1", kind: "employment" as const }],
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

describe("connections relations", () => {
  it("names how each node attaches, not just what it is", async () => {
    stub(graph());
    render(<ConnectionsCard orgId={ROOT} />);

    const list = await screen.findByRole("list");
    // The contact is an employee AND a stakeholder on the drawn deal; both
    // relations show, because either alone would misdescribe them.
    for (const relation of [
      "works here",
      "stakeholder on a deal",
      "open deal",
    ]) {
      expect(within(list).getAllByText(relation), relation).toHaveLength(1);
    }
    // The deal is at the FROM end of the stakeholder edge, and that edge says
    // nothing about it: a deal is not "a stakeholder".
    expect(relationKeys(graph(), "d-1")).toEqual(["has_deal"]);
  });

  it("tells a parent from a subsidiary, because the edge runs one way", () => {
    const g = graph({
      nodes: [
        node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
        node({ id: "o-up", kind: "organization", label: "Holding" }),
        node({ id: "o-down", kind: "organization", label: "Subsidiary" }),
      ],
      edges: [
        { from: "o-up", to: ROOT, kind: "parent_of" as const },
        { from: ROOT, to: "o-down", kind: "parent_of" as const },
      ],
    });

    expect(relationKeys(g, "o-up")).toEqual(["parent"]);
    expect(relationKeys(g, "o-down")).toEqual(["child"]);
  });

  it("keeps the two directions of a referral apart", () => {
    const g = graph({
      nodes: [
        node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
        node({ id: "o-them", kind: "organization", label: "Referrer" }),
        node({ id: "o-us", kind: "organization", label: "Referred" }),
      ],
      // referred_by is recorded on the row of the org that WAS referred, and
      // the edge runs from it to the partner who referred it.
      edges: [
        { from: ROOT, to: "o-them", kind: "referred_by" as const },
        { from: "o-us", to: ROOT, kind: "referred_by" as const },
      ],
    });

    expect(relationKeys(g, "o-them")).toEqual(["referred_by.counterparty"]);
    expect(relationKeys(g, "o-us")).toEqual(["referred_by.owner"]);
  });

  it("says a symmetric co-sell edge once, whichever side recorded it", () => {
    const g = graph({
      nodes: [
        node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
        node({ id: "o-1", kind: "organization", label: "Co-seller" }),
      ],
      edges: [{ from: "o-1", to: ROOT, kind: "co_sell_with" as const }],
    });

    expect(relationKeys(g, "o-1")).toEqual(["co_sell_with"]);
  });

  it("says one relation once, however many edges carry it", () => {
    const g = graph({
      nodes: [
        node({ id: ROOT, kind: "organization", label: "Brandt", root: true }),
        node({ id: "d-1", kind: "deal", label: "One" }),
        node({ id: "d-2", kind: "deal", label: "Two" }),
        node({ id: "p-1", kind: "person", label: "Dana" }),
      ],
      edges: [
        { from: ROOT, to: "d-1", kind: "has_deal" as const },
        { from: ROOT, to: "d-2", kind: "has_deal" as const },
        { from: "d-1", to: "p-1", kind: "deal_stakeholder" as const },
        { from: "d-2", to: "p-1", kind: "deal_stakeholder" as const },
      ],
    });

    // Two seats, one word: "stakeholder on a deal, stakeholder on a deal"
    // reads as a rendering bug, not as two facts.
    expect(relationKeys(g, "p-1")).toEqual(["deal_stakeholder"]);
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

describe("company logos on the diagram", () => {
  // A55: a logo is the upgrade, the node is the floor. A company node must
  // read as a node whether or not its image ever paints.
  const withLogo = () =>
    graph({
      nodes: [
        node({
          id: ROOT,
          kind: "organization",
          label: "Brandt",
          root: true,
          logo_url: `/v1/organizations/${ROOT}/logo`,
        }),
      ],
      edges: [],
    });

  it("draws the mark clipped into the node and takes the neutral backing", async () => {
    stub(withLogo());
    const { container } = render(<ConnectionsCard orgId={ROOT} />);
    await waitFor(() =>
      expect(container.querySelector("image.cx-node-logo")).toBeTruthy(),
    );
    const image = container.querySelector("image.cx-node-logo");
    expect(image?.getAttribute("href")).toBe(`/v1/organizations/${ROOT}/logo`);
    // Clipped to its own node, never to a shared path — a shared clip would
    // cut every logo to one node's position.
    expect(image?.getAttribute("clip-path")).toBe(`url(#cx-clip-${ROOT})`);
    expect(container.querySelectorAll("circle.cx-node-marked")).toHaveLength(1);
  });

  it("falls back to the node's own colour when the logo fails to load", async () => {
    stub(withLogo());
    const { container } = render(<ConnectionsCard orgId={ROOT} />);
    const image = await waitFor(() => {
      const found = container.querySelector("image.cx-node-logo");
      expect(found).toBeTruthy();
      return found as SVGImageElement;
    });

    fireEvent.error(image);

    await waitFor(() =>
      expect(container.querySelector("image.cx-node-logo")).toBeNull(),
    );
    // The neutral backing goes with it: a node whose mark never painted must
    // keep its kind colour rather than becoming a pale empty disc.
    expect(container.querySelectorAll("circle.cx-node-marked")).toHaveLength(0);
    expect(
      container.querySelectorAll("circle.cx-node-organization"),
    ).toHaveLength(1);
  });
});
