/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { meFixture } from "../app/mefixture";
import { steppedClock } from "../testing/steppedclock";
import { SEARCH_DEBOUNCE_MS } from "./listquery";
import { AuditLogCard } from "./settings";
import { auditEntry, jsonResponse, render } from "./settings.testkit";

// The audit trail card, read on its own rather than through the Privacy & audit
// entry that hosts it: one card carrying its own filters, the wire as the only
// honest witness that a typed filter narrowed the question, and a change detail
// that stays folded away until a reader asks for it.

// No shared fetch stub: the backend a claim needs is installed beside the claim,
// so what answered it is readable where it is asserted.
beforeEach(() => {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  globalThis.localStorage.clear();
});

function auditLogBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    // The trail is the admin's alone, so every case below needs a principal who
    // may read it — an anonymous fixture would only ever exercise the withheld
    // rung, which has a case of its own on the Privacy & audit page.
    if (url.endsWith("/v1/me")) {
      return jsonResponse(meFixture({ roles: ["admin"] }));
    }
    if (url.includes("/audit-log")) {
      return jsonResponse({
        data: [auditEntry],
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// Which /audit-log URLs a backend was actually asked for, newest last — the
// wire is the only honest witness that a typed filter narrowed the question.
function auditLogUrls(backend: ReturnType<typeof auditLogBackend>) {
  return backend.mock.calls
    .map(([input]) => String(input instanceof Request ? input.url : input))
    .filter((url) => url.includes("/audit-log"));
}

describe("AuditLogCard", () => {
  // The dials and the list they narrow are ONE surface, the way every other
  // filtered list in this product draws them. Two cards made the filter row a
  // subject in the page outline, level with the trail it narrows, and left a
  // reader scanning two boxes to answer one question.
  //
  // Inside that one card the dials are the SECONDARY half — a reader arrives to
  // read what happened and narrows it second — so they sit in a disclosure that
  // is closed on arrival. Closed, not gone: the fields are in the card, under a
  // summary that says what opening it gets you.
  it("puts the filters inside the log's own card, in a disclosure closed on arrival", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    await screen.findByText("update");

    const actorFilter = screen.getByLabelText("Actor");
    const entryAction = screen.getByText("update");
    const card = actorFilter.closest("section");
    expect(card).not.toBeNull();
    expect(entryAction.closest("section")).toBe(card);
    // One card, named for the log.
    expect(card).toContainElement(
      screen.getByRole("heading", { level: 2, name: "Audit log" }),
    );
    // And the dials inside it, behind a summary rather than above the trail:
    // the group is a <details> that has not been opened.
    const disclosure = actorFilter.closest("details");
    expect(disclosure).not.toBeNull();
    expect(disclosure).not.toHaveAttribute("open");
    expect(card).toContainElement(disclosure);
    expect(disclosure?.querySelector("summary")?.textContent).toContain(
      "Filters",
    );
  });

  it("narrows the request to the filters, keeping the page size and dropping the cursor", async () => {
    const user = steppedClock();
    const backend = auditLogBackend();
    vi.stubGlobal("fetch", backend);
    render(<AuditLogCard />);
    await screen.findByText("update");
    expect(auditLogUrls(backend)[0]).toContain("limit=20");

    await user.type(screen.getByLabelText("Actor"), "agent:sdr");
    await user.type(screen.getByLabelText("Entity type"), "person");

    // The card debounces what it asks, so the narrowed request exists only once
    // the debounce has elapsed — and that is stepped rather than waited out. On
    // the real clock this assertion races a scheduler it does not control, and
    // the failure it produces says the query params are wrong when what actually
    // happened is that the machine was busy.
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS);
    await waitFor(() => {
      const latest = auditLogUrls(backend).at(-1) ?? "";
      expect(latest).toContain("actor=agent%3Asdr");
      expect(latest).toContain("entity_type=person");
    });
    const latest = auditLogUrls(backend).at(-1) ?? "";
    expect(latest).toContain("limit=20");
    // A filter change is a new question, so the narrowed request starts the
    // keyset chain over instead of resuming the unfiltered one's cursor.
    expect(latest).not.toContain("cursor=");
  });

  it("says the log is empty rather than showing an empty entries card", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) =>
        String(input instanceof Request ? input.url : input).endsWith("/v1/me")
          ? jsonResponse(meFixture({ roles: ["admin"] }))
          : jsonResponse({
              data: [],
              page: { next_cursor: null, has_more: false },
            }),
      ),
    );
    render(<AuditLogCard />);
    expect(await screen.findByText("Nothing here yet.")).toBeInTheDocument();
  });

  it("offers a retry when the log fails to load", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["admin"] }));
        }
        if (url.includes("/audit-log")) {
          return jsonResponse({ title: "Upstream is down" }, 500);
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    render(<AuditLogCard />);
    expect(
      await screen.findByRole("button", { name: "Retry" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Couldn't load this view.")).toBeInTheDocument();
    // The filter row survives the failure — a failed page must not take the
    // controls that could ask a different question with it.
    expect(screen.getByLabelText("Actor")).toBeInTheDocument();
  });

  it("keeps the before/after diff hidden until the row is expanded", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    const user = userEvent.setup();
    await screen.findByText("update");
    // Hidden by default — the diff values never render before the toggle.
    expect(screen.queryByText("new")).toBeNull();
    expect(screen.queryByText("qualified")).toBeNull();
    expect(screen.queryByText("pp-9")).toBeNull();

    await user.click(
      screen.getByRole("button", { name: "Show change detail" }),
    );

    expect(await screen.findByText("new")).toBeTruthy();
    expect(screen.getByText("qualified")).toBeTruthy();
    expect(screen.getByText("pp-9")).toBeTruthy();
  });

  it("renders from/to date filters alongside the existing text filters", async () => {
    vi.stubGlobal("fetch", auditLogBackend());
    render(<AuditLogCard />);
    await screen.findByText("update");
    // Read through the attribute rather than a narrowed element: a query that
    // has to be asserted into an input tells you nothing about the input, and
    // the claim here is what the control IS.
    expect(screen.getByLabelText("From")).toHaveAttribute("type", "date");
    expect(screen.getByLabelText("To")).toHaveAttribute("type", "date");
  });

  it("renders a non-scalar before/after value as its JSON string, not [object Object]", async () => {
    const objectValuedEntry = {
      ...auditEntry,
      id: "al-2",
      before: { address: { city: "Berlin" } },
      after: { address: { city: "Munich" } },
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["admin"] }));
        }
        if (url.includes("/audit-log")) {
          return jsonResponse({
            data: [objectValuedEntry],
            page: { next_cursor: null, has_more: false },
          });
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    const user = userEvent.setup();
    render(<AuditLogCard />);
    await screen.findByText("update");

    await user.click(
      screen.getByRole("button", { name: "Show change detail" }),
    );

    expect(await screen.findByText('{"city":"Berlin"}')).toBeTruthy();
    expect(screen.getByText('{"city":"Munich"}')).toBeTruthy();
    expect(screen.queryByText("[object Object]")).toBeNull();
  });
});
