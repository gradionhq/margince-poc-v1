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
import { LocaleProvider } from "../i18n";
import { CompanyApprovalsPanel } from "./companyapprovals";

// The Decisions queue's failure line has to name the right cause: a role that
// cannot see this account's queue is a settled boundary, distinct from a
// queue read that simply failed and may resolve on a retry.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
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

describe("CompanyApprovalsPanel — a refused read is not a failed one", () => {
  it("states the restricted boundary, with no retry wording, on a 403", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (new URL(request.url).pathname.endsWith("/approvals")) {
          return jsonResponse(
            { title: "Forbidden", code: "permission_denied" },
            403,
          );
        }
        return jsonResponse({ data: [] });
      }),
    );
    render(<CompanyApprovalsPanel orgId="o-1" onClose={() => {}} />);

    await waitFor(() =>
      expect(
        screen.getByText("Hidden — your role cannot read this"),
      ).toBeTruthy(),
    );
    expect(
      screen.queryByText(
        "Could not be loaded — this may not be the whole picture",
      ),
    ).toBeNull();
  });

  it("states the retryable failure, not the restricted line, on a transient error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (new URL(request.url).pathname.endsWith("/approvals")) {
          return jsonResponse({ title: "Internal", detail: "boom" }, 500);
        }
        return jsonResponse({ data: [] });
      }),
    );
    render(<CompanyApprovalsPanel orgId="o-1" onClose={() => {}} />);

    await waitFor(() => expect(screen.getByText("boom")).toBeTruthy());
    expect(
      screen.queryByText("Hidden — your role cannot read this"),
    ).toBeNull();
  });
});
