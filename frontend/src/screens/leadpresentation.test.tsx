/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { LeadBoard, scoreTone } from "./leadpresentation";

type Lead = components["schemas"]["Lead"];

function renderBoard(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

const lead: Lead = {
  id: "00000000-0000-0000-0000-000000000001",
  full_name: "Jonas Petersen",
  company_name: "Nordwind Logistik",
  title: "VP Sales",
  status: "new",
  score: 72,
  score_reason: "manual:employees",
  sla_state: "breached",
  source: "webform",
  next_task_subject: "Call about the pilot",
  open_task_count: 1,
  captured_by: "human:user-1",
  version: 1,
  created_at: "2026-08-18T08:00:00Z",
  updated_at: "2026-08-18T08:00:00Z",
};

afterEach(cleanup);

describe("lead work-board presentation", () => {
  it("uses neutral styling for a low score", () => {
    expect(scoreTone(0)).toBeUndefined();
  });

  it("uses lead-specific counts and shows work context on every card", () => {
    renderBoard(
      <LeadBoard
        rows={[lead]}
        onMoved={() => undefined}
        hasMore={false}
        loadMore={() => undefined}
      />,
    );

    expect(screen.getByText("1 leads")).toBeTruthy();
    expect(screen.queryByText(/deals/i)).toBeNull();
    expect(screen.getByText("Overdue")).toBeTruthy();
    expect(screen.getByText("Web form")).toBeTruthy();
    expect(screen.getByText(/Call about the pilot · 1 open task/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /next page/i })).toBeNull();
  });
});
