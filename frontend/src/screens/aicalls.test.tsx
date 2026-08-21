/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
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

// The trace is gated on automation:update — the server treats the runtime's calls as
// operator information — so a stub that never answers /me leaves the caller holding
// no grant and the card correctly says it is withheld instead of rendering rows.
const OPERATOR: GrantSpec = { automation: ["read", "update"] };

function mount(
  captureEnabled = true,
  withPayload = true,
  allow: GrantSpec = OPERATOR,
) {
  const seen: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(
        input instanceof Request ? input.url : String(input),
        "https://test",
      ).pathname;
      seen.push(path);
      if (path.endsWith("/v1/me")) {
        return new Response(JSON.stringify(meFixture({ allow })), {
          headers: { "Content-Type": "application/json" },
        });
      }
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
  return { seen };
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
  // The disclosure is a real button now, not the row: a `<tr onClick>`
  // could only ever be reached by pointer.
  const toggle = screen.getByRole("button", {
    name: /show the attempt trail/i,
  });
  // The chevron is turned by this attribute (aicalls.css), so what the reader
  // sees and what a screen reader hears are one fact rather than two that can
  // disagree. It is also why this stays a button and not a `Disclosure`: what
  // opens is the NEXT table row, which no element can contain from inside a
  // cell of the row above it.
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  await userEvent.click(toggle);
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  expect(await screen.findByText(/retry_on_5xx/)).toBeTruthy();
  expect(screen.getByText("Request payload")).toBeTruthy();
  expect(screen.getByText("Export as cert scenario")).toBeTruthy();
});

// The filter is a settings row now: the row draws the label and the Select is
// named BY it, so the combobox still says what it narrows while carrying no
// second name of its own — one visible label naming one control is the whole
// reason `control` is a function here.
it("names the task filter from its row, and stacks the trace under its own label", async () => {
  mount();
  const filter = await screen.findByRole("combobox", { name: "Task" });
  expect(filter.getAttribute("aria-label")).toBeNull();
  expect(filter.getAttribute("aria-labelledby")).toBeTruthy();
  // The trace is the subject of its row, not an answer beside it, so it stacks
  // under a label of its own rather than sharing the filter's.
  expect(screen.getByText("Recent calls")).toBeTruthy();
});

it("distinguishes capture disabled from a call without payload", async () => {
  mount(false, false);
  await userEvent.click(
    await screen.findByRole("button", { name: /show the attempt trail/i }),
  );
  expect(await screen.findByText(/Payload capture is off/)).toBeTruthy();
  cleanup();
  mount(true, false);
  await userEvent.click(
    await screen.findByRole("button", { name: /show the attempt trail/i }),
  );
  expect(
    await screen.findByText("No payload captured for this call."),
  ).toBeTruthy();
});

it("withholds the trace from a principal without the automation grant, and asks the server for nothing", async () => {
  // Withheld, not absent: an absent trace claims the installation made no model
  // calls. The card keeps its title and says whose record this is — and the list
  // read never fires, because the denial is already known.
  const { seen } = mount(true, true, { automation: ["read"] });

  expect(
    await screen.findByText(/only an operator can read the per-call trace/i),
  ).toBeTruthy();
  expect(screen.getByText("AI call trace")).toBeTruthy();
  expect(seen.some((path) => path.includes("/ai/calls"))).toBe(false);
});
