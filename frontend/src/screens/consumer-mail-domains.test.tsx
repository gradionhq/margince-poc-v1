/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { ConsumerMailDomainsCard } from "./consumer-mail-domains";

// The workspace's own consumer-mail list. The server's write split: adding a
// NEW `extra` entry admits on capture_settings create OR update (the upsert
// pair), while the `never` carve-out, kind overwrites and removal demand
// update — so a fixture granting capture_settings delete, or any grant on a
// mail-shaped object, must leave every control inert.
const CAPTURE_EDITOR: GrantSpec = { capture_settings: ["read", "update"] };

function backend(allow: GrantSpec) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    let body: unknown;
    if (url.endsWith("/v1/me")) {
      body = meFixture({ allow });
    } else if (url.includes("/consumer-mail-baseline")) {
      body = { data: [], matched: 0, total: 8758 };
    } else {
      // id is required by the ConsumerMailDomain contract and is what remove
      // sends; a fixture without it would let a broken remove path call
      // mutate(undefined) and still pass.
      body = { data: [{ id: "cmd-1", domain: "gmx.test", kind: "extra" }] };
    }
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    });
  });
}

function Providers({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{children}</LocaleProvider>
    </QueryClientProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const addButton = () =>
  screen.getByRole("button", { name: "Add" }) as HTMLButtonElement;

// The per-row remove control is icon-only; its accessible name is the label.
const removeButton = () =>
  screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement;

describe("ConsumerMailDomainsCard", () => {
  it("enables add and remove on capture_settings:update", async () => {
    vi.stubGlobal("fetch", backend(CAPTURE_EDITOR));
    render(
      <Providers>
        <ConsumerMailDomainsCard />
      </Providers>,
    );

    await waitFor(() => expect(screen.getByText("gmx.test")).toBeTruthy());
    expect(addButton().disabled).toBe(false);
    expect(removeButton().disabled).toBe(false);
  });

  it("leaves both inert without the update grant", async () => {
    vi.stubGlobal("fetch", backend({ capture_settings: ["read"] }));
    render(
      <Providers>
        <ConsumerMailDomainsCard />
      </Providers>,
    );

    await waitFor(() => expect(screen.getByText("gmx.test")).toBeTruthy());
    // Both, not just Add: the two share one grant, so a change that decoupled
    // them would otherwise pass.
    expect(addButton().disabled).toBe(true);
    expect(removeButton().disabled).toBe(true);
  });

  // The rep posture: create without update adds a NEW consumer domain, and
  // nothing else — remove stays inert, mirroring the server's split.
  it("enables add but not remove on capture_settings:create alone", async () => {
    vi.stubGlobal("fetch", backend({ capture_settings: ["read", "create"] }));
    render(
      <Providers>
        <ConsumerMailDomainsCard />
      </Providers>,
    );

    await waitFor(() => expect(screen.getByText("gmx.test")).toBeTruthy());
    expect(addButton().disabled).toBe(false);
    expect(removeButton().disabled).toBe(true);
  });

  // Removal is an UPDATE. A principal holding delete but not update must not
  // reach the remove control — that is the trap this binding exists to avoid.
  it("does not mistake capture_settings:delete for permission to remove", async () => {
    vi.stubGlobal("fetch", backend({ capture_settings: ["read", "delete"] }));
    render(
      <Providers>
        <ConsumerMailDomainsCard />
      </Providers>,
    );

    await waitFor(() => expect(screen.getByText("gmx.test")).toBeTruthy());
    // Both, not just Add: the two share one grant, so a change that decoupled
    // them would otherwise pass.
    expect(addButton().disabled).toBe(true);
    expect(removeButton().disabled).toBe(true);
  });
});
