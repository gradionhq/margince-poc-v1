/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { AiCallsCard } from "./aicalls";

const summary = {
  id: "019f7e65-fbf7-7114-b114-40af4af63ae8",
  occurred_at: "2026-07-20T10:00:00Z",
  task: "capture_classify",
  tier: "cheap_cloud",
  provider: "gemini",
  model_id: "configured",
  served_model: "served",
  calls_attempted: 2,
  tokens_in: 100,
  tokens_out: 20,
  reasoning_tokens: 0,
  cached_tokens: 0,
  latency_ms: 900,
  cache_hit: false,
  degraded: true,
  error_sentinel: "provider_unavailable",
  has_payload: true,
};

function mount(captureEnabled = true, withPayload = true) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(
        input instanceof Request ? input.url : String(input),
        "https://test",
      ).pathname;
      const body = path.endsWith(summary.id)
        ? {
            ...summary,
            served_identity_source: "response",
            context_scopes: [],
            context_fingerprint: "",
            attempts: [
              {
                attempt: 1,
                is_terminal: false,
                attempt_reason: "",
                tokens_in: 100,
                tokens_out: 0,
                latency_ms: 400,
                occurred_at: summary.occurred_at,
              },
              {
                attempt: 2,
                is_terminal: true,
                attempt_reason: "retry_on_5xx",
                tokens_in: 100,
                tokens_out: 20,
                latency_ms: 900,
                occurred_at: summary.occurred_at,
              },
            ],
            payload_captured: withPayload,
            payload: withPayload
              ? { request: { system: "safe", messages: [] }, response: "ok" }
              : null,
          }
        : {
            data: [summary],
            page: { has_more: false },
            payload_capture_enabled: captureEnabled,
            tasks: [summary.task],
          };
      return new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <AiCallsCard />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("renders call badges and expands the attempt and payload detail", async () => {
  mount();
  expect(await screen.findByText("provider_unavailable")).toBeTruthy();
  expect(screen.getByText("retry ×2")).toBeTruthy();
  // One element, not the second of two: the task name used to appear in the
  // filter's option list as well as in the row, and the row is what expands.
  await userEvent.click(screen.getByText("capture_classify"));
  expect(await screen.findByText(/retry_on_5xx/)).toBeTruthy();
  expect(screen.getByText("Request payload")).toBeTruthy();
  expect(screen.getByText("Export as cert scenario")).toBeTruthy();
});

// The filter stands alone above the table with no visible label beside it, so
// its name has to come from the control itself — an unnamed combobox tells a
// screen reader nothing about what it narrows.
it("names the task filter", async () => {
  mount();
  expect(await screen.findByRole("combobox", { name: "Task" })).toBeTruthy();
});

it("distinguishes capture disabled from a call without payload", async () => {
  mount(false, false);
  await userEvent.click(await screen.findByText("capture_classify"));
  expect(await screen.findByText(/Payload capture is off/)).toBeTruthy();
  cleanup();
  mount(true, false);
  await userEvent.click(await screen.findByText("capture_classify"));
  expect(
    await screen.findByText("No payload captured for this call."),
  ).toBeTruthy();
});
