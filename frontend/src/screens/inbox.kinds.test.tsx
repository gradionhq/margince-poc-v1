/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { InboxScreen, usePendingApprovals } from "./inbox";

// Every kind the API serves reaches the reader, including one this build has
// never heard of.
//
// An approval here is confirm-first: it stages, waits for a human, and lapses
// after three days with nothing said. So a row the queue drops is not a display
// bug — it is a decision nobody was asked to make and a staged action that
// silently never happens. The kind vocabulary lives on the server and grows
// there, which means the client is always one deploy away from meeting a kind
// it has no label for; the only safe answer is to render it and let the
// summary the server wrote carry the question.
//
// `archive_record` is the case that made this concrete: it is served with every
// field the other kinds carry and it went missing from the inbox.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Approval = components["schemas"]["Approval"];

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

// No expires_at on either fixture: a countdown is a live clock, and what is
// asserted below is what the queue LISTS, which no date changes.
const draft: Approval = {
  id: "ap-draft",
  kind: "send_email",
  status: "pending",
  proposed_by: "agent:runner",
  summary: "Send the follow-up to Anna Weber",
  proposed_change: { subject: "Follow-up", body: "shall we sync next week?" },
  created_at: "2026-07-05T05:00:00Z",
} as Approval;

// Staged by the governed archive tool against an activity: a target reference
// and a summary, and no prose field of its own. The row that went missing.
const archive: Approval = {
  id: "ap-archive",
  kind: "archive_record",
  status: "pending",
  proposed_by: "agent:runner",
  summary: 'Archive activity "Kickoff Migration Shopsystem"',
  target_entity_type: "activity",
  target_entity_id: "018f3a1b-0000-7000-8000-00000000ac71",
  proposed_change: {
    record_type: "activity",
    id: "018f3a1b-0000-7000-8000-00000000ac71",
  },
  created_at: "2026-07-05T05:01:00Z",
} as Approval;

// A kind added upstream that this build has no label and no editor policy for.
const unknownKind: Approval = {
  ...archive,
  id: "ap-unknown",
  kind: "quarantine_shipment",
  summary: "Hold shipment SH-4417 until the customs paper arrives",
} as Approval;

function pendingBackend(rows: readonly Approval[]) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/agent-tools")) {
      return jsonResponse({ data: [], page: { next_cursor: null } });
    }
    if (/\/approvals(\?|$)/.test(url) && url.includes("status=pending")) {
      return jsonResponse({ data: rows, page: { next_cursor: null } });
    }
    return jsonResponse({ data: [], page: { next_cursor: null } });
  });
}

// What the rail badge beside Inbox reads: App.tsx hands the shell
// `usePendingApprovals().data.data.length` and nothing else, so the hook's own
// length IS the number a reader sees waiting for them.
function PendingCount() {
  const pending = usePendingApprovals();
  return (
    <p data-testid="pending-count">
      {pending.data ? String(pending.data.data.length) : "…"}
    </p>
  );
}

describe("the pending queue lists every kind the API serves", () => {
  it("lists an archive_record proposal beside the rest of the queue", async () => {
    vi.stubGlobal("fetch", pendingBackend([draft, archive]));
    render(<InboxScreen />);
    // The summary the server wrote is the question the reader answers, and the
    // kind label above it says what sort of question it is.
    expect(
      await screen.findByText(
        'Archive activity "Kickoff Migration Shopsystem"',
      ),
    ).toBeTruthy();
    expect(screen.getByText("Archive a record")).toBeTruthy();
    // The neighbour proves the queue rendered rather than that it rendered
    // only this row: dropping one of two is the defect.
    expect(screen.getByText("Send the follow-up to Anna Weber")).toBeTruthy();
  });

  it("counts an archive_record proposal in the number waiting", async () => {
    vi.stubGlobal("fetch", pendingBackend([draft, archive]));
    render(<PendingCount />);
    await waitFor(() =>
      expect(screen.getByTestId("pending-count").textContent).toBe("2"),
    );
  });

  it("renders a kind it has no label for rather than dropping it", async () => {
    vi.stubGlobal("fetch", pendingBackend([unknownKind]));
    render(<InboxScreen />);
    expect(
      await screen.findByText(
        "Hold shipment SH-4417 until the customs paper arrives",
      ),
    ).toBeTruthy();
    // Degraded to its own words, never to the wire token: a reader deciding
    // this cannot be shown an identifier only the server's author recognises.
    expect(screen.getByText("quarantine shipment")).toBeTruthy();
    expect(screen.queryByText("quarantine_shipment")).toBeNull();
  });
});
