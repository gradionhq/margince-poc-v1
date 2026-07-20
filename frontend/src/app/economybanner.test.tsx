/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { EconomyBanner } from "./economybanner";

function mount(roles: string[], band: string) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(
      input instanceof Request ? input.url : String(input),
      "https://test",
    ).pathname;
    const body = path.endsWith("/me")
      ? {
          user: { id: "u1", email: "a@example.test", display_name: "A" },
          roles,
        }
      : { days: [], budget: { monthly_tokens: 100, spent_tokens: 80, band } };
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <EconomyBanner />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return fetchMock;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("does not probe usage for a non-admin", async () => {
  const fetchMock = mount(["rep"], "degraded");
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  expect(
    fetchMock.mock.calls.some(([input]) => String(input).includes("/ai/usage")),
  ).toBe(false);
  expect(screen.queryByText("AI running in economy mode.")).toBeNull();
});

it("shows and dismisses economy mode for an admin", async () => {
  mount(["admin"], "degraded");
  expect(await screen.findByText("AI running in economy mode.")).toBeTruthy();
  await userEvent.click(screen.getByLabelText("Dismiss"));
  expect(screen.queryByText("AI running in economy mode.")).toBeNull();
});

it("shows queued while normal stays silent", async () => {
  mount(["admin"], "queued");
  expect(
    await screen.findByText("AI budget reached — background AI is queued."),
  ).toBeTruthy();
  cleanup();
  const fetchMock = mount(["admin"], "normal");
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole("status")).toBeNull();
});
