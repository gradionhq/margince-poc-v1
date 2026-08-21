/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { en } from "../i18n/en";
import { PersonGraphPanel } from "./persongraph";

// The panel renders beside a sibling, because the failure this file exists to
// prevent is the panel taking the REST of the page down with it. An empty
// container proves nothing on its own: a crashed React tree and a panel that
// correctly rendered nothing look identical from the outside.
function renderPanel(personId = "p-1") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const tree = (id: string): ReactNode => (
    <QueryClientProvider client={client}>
      <div>
        <PersonGraphPanel personId={id} />
        <p>the rest of the record page</p>
      </div>
    </QueryClientProvider>
  );
  const view = render(tree(personId));
  // Re-rendering rather than re-mounting is the point of the contact-switch
  // test: React keeps the panel and its selection state alive when only the
  // prop changes, which is exactly the case a fresh mount would hide.
  return { ...view, showContact: (id: string) => view.rerender(tree(id)) };
}

function stub(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
}

describe("PersonGraphPanel", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  // The arrays are required by the contract, and a response can still arrive
  // without them: a proxy error page, a version-skewed server, or a request
  // that never reached the handler. Reading `.find` off undefined took the
  // WHOLE record page down, not just this panel — the tab rendered an empty
  // body and the relationship list vanished with it.
  it("renders nothing rather than crashing the page on a response with no nodes", async () => {
    stub({ person_id: "p-1" });
    renderPanel();
    // Wait until the read has RESOLVED — the loading line is gone — because
    // the crash happens on the render that receives the data, and asserting
    // before then would pass on the loading frame alone.
    await waitFor(() =>
      expect(
        screen.queryByText("Reading the network around this contact…"),
      ).toBeNull(),
    );
    // The sibling is the assertion. Reading `.find` off undefined unmounted
    // the whole tree, and the relationship list on the same tab vanished with
    // the graph.
    expect(screen.getByText("the rest of the record page")).toBeTruthy();
    expect(screen.queryByText("The warmest way in")).toBeNull();
  });

  // The answer leads. A reader who reads nothing else should still leave
  // knowing who to ask and why.
  it("leads with the recommended route and its proof line", async () => {
    stub({
      person_id: "p-1",
      nodes: [
        {
          id: "person:p-1",
          type: "contact",
          group: "anchor",
          label: "Anna Weber",
        },
        {
          id: "user:u-1",
          type: "colleague",
          group: "direct",
          label: "Direct Dana",
        },
      ],
      edges: [
        {
          from: "user:u-1",
          to: "person:p-1",
          strength_bucket: "strong",
          interactions_90d: 6,
          inbound_90d: 3,
          outbound_90d: 3,
        },
      ],
      groups_omitted: [],
      route: {
        via_user_id: "u-1",
        via_display_name: "Direct Dana",
        why: "6 two-way exchanges in 90 days · last contact yesterday",
      },
    });
    renderPanel();
    await waitFor(() =>
      expect(
        screen.getByText("Direct Dana already corresponds with them."),
      ).toBeTruthy(),
    );
    expect(
      screen.getByText(
        "6 two-way exchanges in 90 days · last contact yesterday",
      ),
    ).toBeTruthy();
  });

  // A group withheld for lack of a grant says so. Rendering it as empty would
  // tell the reader nobody knows this contact when the truth is that they
  // cannot see who does.
  it("says a group was withheld rather than showing it as empty", async () => {
    stub({
      person_id: "p-1",
      nodes: [
        {
          id: "person:p-1",
          type: "contact",
          group: "anchor",
          label: "Anna Weber",
        },
      ],
      edges: [],
      groups_omitted: ["direct"],
    });
    renderPanel();
    await waitFor(() =>
      expect(
        screen.getByText(
          "Part of this is hidden because you do not have the grant for it.",
        ),
      ).toBeTruthy(),
    );
  });

  // A refusal reaches the reader as words, and it only does so because the
  // failure carries the problem BODY rather than a copy of its text on a plain
  // Error — the message of one of those is indistinguishable from a JavaScript
  // bug's, so nothing may show it, and the panel would say only that something
  // failed. The words themselves are the catalog's: a 403 carries the
  // permission sentinel and nothing a reader could have used.
  it("says a refusal is a refusal when the read is denied", async () => {
    stub({ code: "permission_denied", detail: "permission denied" }, 403);
    renderPanel();

    expect(await screen.findByText(en["common.permissionDenied"])).toBeTruthy();
  });

  // The account arm holds edges between two COLLEAGUES, neither of them the
  // contact whose page this is. Reading only the edge's `to` end named whichever
  // node the edge happened to point at — so selecting that node produced a panel
  // saying they correspond with themselves.
  it("names the colleague at the other end of an account-arm edge, not the selected node", async () => {
    const user = userEvent.setup();
    stub({
      person_id: "p-1",
      nodes: [
        { id: "person:p-1", type: "contact", group: "anchor", label: "Anna" },
        { id: "user:u-1", type: "colleague", group: "account", label: "Cara" },
        { id: "user:u-2", type: "colleague", group: "account", label: "Bo" },
      ],
      edges: [
        {
          from: "user:u-1",
          to: "user:u-2",
          strength_bucket: "moderate",
          interactions_90d: 4,
        },
      ],
      groups_omitted: [],
    });
    renderPanel();

    await user.click(await screen.findByRole("button", { name: "Bo" }));

    expect(await screen.findByText("with Cara")).toBeTruthy();
    expect(screen.queryByText("with Bo")).toBeNull();
    // The anchor's own sentence belongs to an edge that reaches the anchor.
    // This one does not touch them at all.
    expect(screen.queryByText("with this contact")).toBeNull();
  });

  it("names the anchor as the page's subject when the edge reaches them", async () => {
    const user = userEvent.setup();
    stub({
      person_id: "p-1",
      nodes: [
        { id: "person:p-1", type: "contact", group: "anchor", label: "Anna" },
        { id: "user:u-1", type: "colleague", group: "direct", label: "Dana" },
      ],
      edges: [
        {
          from: "user:u-1",
          to: "person:p-1",
          strength_bucket: "strong",
          interactions_90d: 6,
        },
      ],
      groups_omitted: [],
    });
    renderPanel();

    await user.click(await screen.findByRole("button", { name: "Dana" }));

    expect(await screen.findByText("with this contact")).toBeTruthy();
  });

  // The panel outlives the contact: moving between records on the Relationships
  // tab re-renders it rather than remounting it, so a selection made on the
  // previous contact stayed set. The detail region then described a
  // relationship belonging to a record the reader had already left.
  it("drops a selected node the newly loaded graph does not contain", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const forFirst = request.url.includes("/people/p-1/");
        const body = {
          person_id: forFirst ? "p-1" : "p-2",
          nodes: [
            {
              id: forFirst ? "person:p-1" : "person:p-2",
              type: "contact",
              group: "anchor",
              label: forFirst ? "Anna" : "Bruno",
            },
            forFirst
              ? {
                  id: "user:u-1",
                  type: "colleague",
                  group: "direct",
                  label: "Dana",
                }
              : {
                  id: "user:u-9",
                  type: "colleague",
                  group: "direct",
                  label: "Elif",
                },
          ],
          edges: forFirst
            ? [
                {
                  from: "user:u-1",
                  to: "person:p-1",
                  strength_bucket: "strong",
                  interactions_90d: 6,
                },
              ]
            : [],
          groups_omitted: [],
        };
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }),
    );
    const { showContact } = renderPanel();

    await user.click(await screen.findByRole("button", { name: "Dana" }));
    expect(await screen.findByText("with this contact")).toBeTruthy();

    showContact("p-2");

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Elif" })).toBeTruthy(),
    );
    expect(screen.queryByText("with this contact")).toBeNull();
    expect(screen.queryByText("No recorded correspondence with .")).toBeNull();
  });

  it("falls back to the shared line for a failure nobody phrased", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.reject(new TypeError("ECONNREFUSED: connection refused")),
      ),
    );
    renderPanel();

    expect(
      await screen.findByText("The request failed. No cause reported."),
    ).toBeTruthy();
    expect(screen.queryByText(/ECONNREFUSED/)).toBeNull();
  });
});
