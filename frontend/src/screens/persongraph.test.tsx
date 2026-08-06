/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PersonGraphPanel } from "./persongraph";

// The panel renders beside a sibling, because the failure this file exists to
// prevent is the panel taking the REST of the page down with it. An empty
// container proves nothing on its own: a crashed React tree and a panel that
// correctly rendered nothing look identical from the outside.
function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <div>
        <PersonGraphPanel personId="p-1" />
        <p>the rest of the record page</p>
      </div>
    </QueryClientProvider>,
  );
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

  // A refusal the server phrased for a reader is the reader's answer, and it
  // only survives the read because the failure carries the problem body rather
  // than a copy of its text on a plain Error — the message of one of those is
  // indistinguishable from a JavaScript bug's, so nothing may show it.
  it("says the server's own cause when the read is refused", async () => {
    stub(
      {
        code: "permission_denied",
        detail: "You do not have the grant for this network.",
      },
      403,
    );
    renderPanel();

    expect(
      await screen.findByText("You do not have the grant for this network."),
    ).toBeTruthy();
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
