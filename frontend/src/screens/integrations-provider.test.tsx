/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SLOWEST_MEASURED_TEST_MS } from "../../vitest.budget";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { ProviderCard } from "./integrations-provider";

type Me = components["schemas"]["MeResponse"];
type Grant = components["schemas"]["RbacObjectGrant"];
type SeatType = components["schemas"]["Authorization"]["seat_type"];
type ProviderConnection = components["schemas"]["ProviderConnection"];

// A live connection: a key is in place, so both destructive actions are
// eligible, and the provider has told us a balance — the reading every seat is
// granted and the one the card must keep showing when the writes go away.
const CONNECTION: ProviderConnection = {
  provider: "surfe",
  status: "connected",
  credential_present: true,
  configuration: {
    mode: "automatic_on_create",
    preset: "full",
    automatic_individual_create: true,
    automatic_import: false,
    categories: { professional: true },
  },
  credits: { pools: { email: 120 } },
  version: 4,
  created_at: "2026-01-05T09:00:00Z",
  updated_at: "2026-01-05T09:04:00Z",
};

function meResponse(seat: SeatType, integrations: Grant): Me {
  return {
    user: {
      id: "00000000-0000-4000-8000-000000000001",
      email: "mira@acme.test",
      display_name: "Mira Voss",
      timezone: "UTC",
      status: "active",
      is_agent: false,
    },
    workspace_name: "Acme",
    non_production: true,
    admin_password_link: false,
    roles: [],
    teams: [],
    authorization: { seat_type: seat, objects: { integrations } },
  };
}

// Admin/ops: the seat the seeded grants give the whole object to, because
// connecting a provider spends money.
const ME_OPERATOR = meResponse("full", {
  create: true,
  read: true,
  update: true,
  delete: true,
});

// A rep, exactly as the roster seeds it: read on `integrations` so a dated value
// on a person record has an explanation, and nothing more.
const ME_READER = meResponse("full", {
  create: false,
  read: true,
  update: false,
  delete: false,
});

// The middle case, and the reason the two gates are asked separately: a
// principal who may bind a key but may not destroy what it bought. Nothing
// seeds this, and an operator who edits a role can produce it.
const ME_CONNECT_ONLY = meResponse("full", {
  create: true,
  read: true,
  update: false,
  delete: false,
});

function backend(principal: Me) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input instanceof Request ? input.url : input))
      .pathname;
    const body = routeBody(path, principal);
    return new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
    });
  });
}

function routeBody(path: string, principal: Me): unknown {
  if (path === "/v1/me") {
    return principal;
  }
  if (path === "/v1/provider-connections") {
    return { data: [CONNECTION] };
  }
  throw new Error(`unstubbed request: ${path}`);
}

function Providers({ children }: Readonly<{ children: ReactNode }>) {
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

// Two round trips settle before anything is on screen — the connection list and
// the /me snapshot the affordances are scoped by — and neither waits on a clock.
// The default one-second budget is not enough when the whole suite runs in
// parallel, so this states one that survives a loaded machine.
const SETTLE_MS = 10_000;

// `renderAs` runs one waiter at SETTLE_MS, and each case below awaits it. A
// test may spend the SUM of its waiters' budgets without any one of them
// failing, so its own ceiling has to cover that — and the suite's is derived
// for tests that wait at the default. Stated here rather than borrowed, with
// the SAME measured allowance for the work between the waits that the suite
// ceiling uses, so the two rest on one measurement rather than on a round
// number picked here. Without it these fail while the settle is still inside
// its budget, and the failure names the test rather than the round trip that
// was slow (issue 1144).
const RENDER_TEST_MS = SETTLE_MS + SLOWEST_MEASURED_TEST_MS;

// The card sits on the Settings → Integrations entry, whose predicate opens for
// all five roles, and the reads behind it are granted to all five. The writes
// are not: connecting spends money and destroying the data is irreversible, and
// the server admits neither for a manager, a rep or a read_only seat.
describe("ProviderCard write posture", () => {
  const READ_ONLY =
    "Read-only view — connecting a provider spends money, so it is an admin or ops action.";
  const CONNECT = "Replace the key";
  const DISCONNECT = "Disconnect";
  const DELETE_DATA = "Delete bought data";
  const KEY_FIELD = "Replace the API key";
  // Disconnect and delete-data live behind the overflow, because neither is the
  // same weight as Connect: one is recoverable and the other irreversibly
  // destroys purchased contact data. The trigger's presence is what the grant
  // decides; the two verbs are then inside it.
  const MORE = "More actions";

  async function openDestructiveMenu(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByRole("button", { name: MORE }));
  }

  async function renderAs(principal: Me) {
    vi.stubGlobal("fetch", backend(principal));
    render(
      <Providers>
        <ProviderCard />
      </Providers>,
    );
    // The provider's own row, waited on rather than assumed: every assertion
    // below is about a LOADED card, and a card still fetching offers nothing
    // either way.
    await screen.findByRole(
      "heading",
      { name: CONNECTION.provider },
      { timeout: SETTLE_MS },
    );
  }

  it(
    "withholds every write from a seat that holds none, and says so",
    async () => {
      await renderAs(ME_READER);

      // The reading is granted, so it stays: a card that vanished would say this
      // installation buys no contact data, which is a claim about the DATA.
      expect(screen.getByRole("meter", { name: "email" })).toBeTruthy();
      // Stated once, at the surface, rather than annotated onto each absent
      // control.
      expect(screen.getByText(READ_ONLY)).toBeTruthy();
      expect(screen.queryByRole("button", { name: CONNECT })).toBeNull();
      expect(screen.queryByRole("button", { name: MORE })).toBeNull();
      // The key box exists only to feed the submit that is gone, and it is
      // behind a verb this seat does not have — so there is nothing to open.
      expect(screen.queryByLabelText(KEY_FIELD)).toBeNull();
    },
    RENDER_TEST_MS,
  );

  it(
    "offers every write to the seat that pays the provider",
    async () => {
      const user = userEvent.setup();
      await renderAs(ME_OPERATOR);

      // Without this arm the test above would pass on a card that renders no
      // controls for anybody.
      const connect = screen.getByRole("button", { name: CONNECT });
      expect(connect).toBeTruthy();
      // The key is two inputs' worth of commitment — the secret and the
      // acknowledgement that using it spends money — so it lives behind that
      // verb rather than standing open on the card. The row is an answer; the
      // form is not.
      expect(screen.queryByLabelText(KEY_FIELD)).toBeNull();
      await user.click(connect);
      expect(screen.getByLabelText(KEY_FIELD)).toBeTruthy();
      // And the confirm is refused until the field it needs is filled, so an
      // empty dialog cannot POST an empty key.
      const confirms = screen.getAllByRole("button", { name: CONNECT });
      expect(confirms[confirms.length - 1].hasAttribute("disabled")).toBe(true);
      await user.keyboard("{Escape}");

      await openDestructiveMenu(user);
      expect(screen.getByRole("button", { name: DISCONNECT })).toBeTruthy();
      expect(screen.getByRole("button", { name: DELETE_DATA })).toBeTruthy();
      // A reader who may write is told nothing about a posture they do not have.
      expect(screen.queryByText(READ_ONLY)).toBeNull();
    },
    RENDER_TEST_MS,
  );

  it(
    "keeps the destructive pair behind its own grant",
    async () => {
      await renderAs(ME_CONNECT_ONLY);

      expect(screen.getByRole("button", { name: CONNECT })).toBeTruthy();
      // `delete` is what the server demands for both of these, and this seat does
      // not hold it — so neither may ride in on the grant that binds a key, and
      // the overflow that would hold them is not offered at all.
      expect(screen.queryByRole("button", { name: MORE })).toBeNull();
      expect(screen.queryByRole("button", { name: DISCONNECT })).toBeNull();
      expect(screen.queryByRole("button", { name: DELETE_DATA })).toBeNull();
      // Not a read-only view: something here is still writable.
      expect(screen.queryByText(READ_ONLY)).toBeNull();
    },
    RENDER_TEST_MS,
  );
});
