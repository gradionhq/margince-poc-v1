/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { EmbedReindexCard, embedReindexStatusQueryKey } from "./embedreindex";

type Handler = (body: unknown) => Response | Promise<Response>;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const STATUS_NEEDED = {
  configured_identity: "anthropic/voyage-3@1024",
  populated_identity: "anthropic/voyage-2@1024",
  status: "idle",
  updated_at: "2026-07-21T12:00:00Z",
  reindex_needed: true,
  entities_pending: 42,
  per_workspace: [
    {
      entities_pending: 42,
    },
  ],
};

const STATUS_IDLE = {
  ...STATUS_NEEDED,
  populated_identity: "anthropic/voyage-3@1024",
  reindex_needed: false,
  entities_pending: 0,
  per_workspace: [
    {
      entities_pending: 0,
    },
  ],
};

// A marker stuck at reembedding for well over a day — the F2 stuck-job
// scenario: a drift-cancelled or retry-discarded job left no live worker
// behind it, and the SPA is the only way an operator can even notice.
const STATUS_STUCK_REEMBEDDING = {
  ...STATUS_NEEDED,
  status: "reembedding",
  updated_at: "2026-07-20T00:00:00Z",
};

const PREVIEW = {
  entities_pending: 42,
  estimated_ai_tokens: 12_000,
  estimated_cost_minor: 350,
  estimate_quality: "heuristic",
  currency: "USD",
  computed_at: "2026-07-22T00:00:00Z",
  utilization_impact: "degraded",
};

// The card gates its status QUERY on read and its rebuild actions on update —
// two grants, because a viewer may be entitled to see the state without being
// entitled to change it.
const REINDEX_OPERATOR: GrantSpec = { embedding_reindex: ["read", "update"] };

function mount(
  allow: GrantSpec,
  routes: Record<string, Handler>,
  requests: { method: string; url: string; body: unknown }[] = [],
) {
  const fetchMock = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test",
      );
      const method = request?.method ?? init?.method ?? "GET";
      let body: unknown = null;
      const rawBody = request
        ? await request.clone().text()
        : String(init?.body ?? "");
      if (rawBody) {
        try {
          body = JSON.parse(rawBody);
        } catch {
          body = null;
        }
      }
      const path = url.pathname.replace(/^\/v1/, "");
      requests.push({ method, url: path, body });
      if (path.endsWith("/me")) {
        return json(meFixture({ allow }));
      }
      const key = `${method} ${path}`;
      const handler = routes[key];
      return handler ? handler(body) : json({ detail: "unhandled" }, 404);
    },
  );
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <EmbedReindexCard />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { fetchMock, requests };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows the estimate + utilization disclosure and disables confirm until the estimate loads", async () => {
  let resolvePreview: (value: Response) => void = () => {};
  const previewPromise = new Promise<Response>((resolve) => {
    resolvePreview = resolve;
  });
  mount(REINDEX_OPERATOR, {
    "GET /embeddings/reindex/status": () => json(STATUS_NEEDED),
    "GET /embeddings/reindex/preview": () => previewPromise,
  });

  await userEvent.click(await screen.findByText("Review & reindex"));

  const confirmButton = await screen.findByRole("button", {
    name: "Start reindex",
  });
  expect(confirmButton).toBeDisabled();

  resolvePreview(json(PREVIEW));

  await waitFor(() => expect(confirmButton).toBeEnabled());
  expect(screen.getByText(/12,000/)).toBeTruthy();
  expect(screen.getByText(/\$3\.50|US\$3\.50/)).toBeTruthy();
  expect(screen.getByText(/heuristic/i)).toBeTruthy();
  // The utilization disclosure (AIRT-PARAM-9..11 band).
  expect(screen.getByText(/would enter economy mode|degraded/i)).toBeTruthy();
});

it("states a failed estimate as the dialog's own refusal rather than as red text", async () => {
  mount(REINDEX_OPERATOR, {
    "GET /embeddings/reindex/status": () => json(STATUS_NEEDED),
    "GET /embeddings/reindex/preview": () =>
      json(
        {
          title: "Service Unavailable",
          detail: "the estimator could not be reached",
          status: 503,
          code: "unavailable",
        },
        503,
      ),
  });

  await userEvent.click(await screen.findByText("Review & reindex"));

  // The server's own sentence, and it is what the dialog says ABOUT ITSELF —
  // the reason Confirm is refused — so it is spoken as an alert rather than
  // carried in a colour a reader may not perceive.
  const refusal = await screen.findByText("the estimator could not be reached");
  expect(refusal.closest('[role="alert"]')).not.toBeNull();
  expect(
    await screen.findByRole("button", { name: "Start reindex" }),
  ).toBeDisabled();
});

it("posts previewed_identity from the status read and force:false on a plain confirm", async () => {
  const { requests } = mount(REINDEX_OPERATOR, {
    "GET /embeddings/reindex/status": () => json(STATUS_NEEDED),
    "GET /embeddings/reindex/preview": () => json(PREVIEW),
    "POST /embeddings/reindex": () =>
      json({ ...STATUS_NEEDED, status: "reembedding" }, 202),
  });

  await userEvent.click(await screen.findByText("Review & reindex"));
  const confirmButton = await screen.findByRole("button", {
    name: "Start reindex",
  });
  await waitFor(() => expect(confirmButton).toBeEnabled());
  await userEvent.click(confirmButton);

  await waitFor(() =>
    expect(
      requests.some(
        (r) => r.method === "POST" && r.url === "/embeddings/reindex",
      ),
    ).toBe(true),
  );
  const post = requests.find((r) => r.url === "/embeddings/reindex");
  expect(post?.body).toEqual({
    previewed_identity: "anthropic/voyage-3@1024",
    force: false,
  });
  // The dialog closes and the card now reflects the reembedding status.
  expect(await screen.findByText("Reindexing…")).toBeTruthy();
});

it("Rebuild index stays available even when no reindex is needed, and posts force:true", async () => {
  const { requests } = mount(REINDEX_OPERATOR, {
    "GET /embeddings/reindex/status": () => json(STATUS_IDLE),
    "GET /embeddings/reindex/preview": () => json(PREVIEW),
    "POST /embeddings/reindex": () => json({ ...STATUS_IDLE }, 202),
  });

  expect(await screen.findByText("Rebuild index")).toBeTruthy();
  // The naming of the row the button answers, not only the button: an action
  // row is a label, a help line and a verb, and a verb standing in the list
  // without the two says nothing about what it will do.
  expect(screen.getByText("Rebuild the whole index")).toBeTruthy();
  // The "Review & reindex" trigger only appears when a reindex is actually
  // needed — Rebuild is the always-available affordance instead. Its whole ROW
  // goes with it: a naming line left behind would offer an action nothing can
  // start.
  expect(screen.queryByText("Review & reindex")).toBeNull();
  expect(screen.queryByText("Reindex what changed")).toBeNull();

  await userEvent.click(screen.getByText("Rebuild index"));
  const confirmButton = await screen.findByRole("button", {
    name: "Rebuild now",
  });
  await waitFor(() => expect(confirmButton).toBeEnabled());
  await userEvent.click(confirmButton);

  await waitFor(() =>
    expect(
      requests.some(
        (r) => r.method === "POST" && r.url === "/embeddings/reindex",
      ),
    ).toBe(true),
  );
  const post = requests.find((r) => r.url === "/embeddings/reindex");
  expect(post?.body).toEqual({
    previewed_identity: "anthropic/voyage-3@1024",
    force: true,
  });
});

it("F2: a stuck reembedding marker shows the age of the last progress and keeps Rebuild enabled", () => {
  // A drift-cancelled/retry-discarded job can leave the marker stuck at
  // reembedding with no live worker behind it — the SPA must still let an
  // operator judge "stuck" and re-kick it, not just show a spinner forever.
  // Fake timers + a pre-seeded cache (inbox.test.tsx's own AC-7 idiom):
  // Date.now() must be pinned for a deterministic duration, and seeding the
  // cache directly means no async fetch race to unwind under fake timers.
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-07-22T00:00:00Z"));
  try {
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
      },
    });
    client.setQueryData(["me"], meFixture({ allow: REINDEX_OPERATOR }));
    client.setQueryData(embedReindexStatusQueryKey, STATUS_STUCK_REEMBEDDING);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ detail: "unused in this test" }, 404)),
    );

    render(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <EmbedReindexCard />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    expect(screen.getByText("Reindexing…")).toBeTruthy();
    // updated_at is 2026-07-20T00:00:00Z, system time is 2026-07-22T00:00:00Z
    // — the run last reported progress 2 days ago, formatDuration's
    // absolute-day rendering. A running pass refreshes that stamp as it
    // embeds, so two days of it is a run nothing is working, which is exactly
    // the judgment the Rebuild action below is for.
    expect(screen.getByText("Last progress 2d ago")).toBeTruthy();

    // The Rebuild action stays enabled (not disabled) while reembedding —
    // that's the re-kick affordance (force:true), not blocked by isRunning.
    const rebuildButton = screen.getByRole("button", {
      name: "Rebuild index",
    });
    expect(rebuildButton).toBeEnabled();
  } finally {
    vi.useRealTimers();
  }
});

// read and update are separate grants: the card gates its status QUERY on the
// read and its rebuild actions on the update, so a read↔update swap must fail.
it("renders the status but no rebuild actions on the read grant alone", async () => {
  mount(
    { embedding_reindex: ["read"] },
    {
      "GET /embeddings/reindex/status": () => json(STATUS_NEEDED),
      "GET /embeddings/reindex/preview": () => json(PREVIEW),
    },
  );
  // Positively, and on the STATUS rather than on the card's own heading: the
  // heading and sub render for a withheld card too, so only a rendered status
  // proves the read grant admitted the query. Asserting the absence of the write
  // controls alone would pass just as well on the withheld card — which is what
  // a broken READ binding produces, and is exactly the case this test exists to
  // distinguish.
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
  // The status row keeps its own naming, so the reading is still labelled for a
  // seat that may only read it.
  expect(screen.getByText("Index status")).toBeTruthy();
  expect(screen.queryByText("Review & reindex")).toBeNull();
  expect(screen.queryByRole("button", { name: /Rebuild/ })).toBeNull();
  // Both ACTION rows go whole. A naming line whose control the update grant
  // withheld would describe a rebuild this reader cannot start, which reads as a
  // broken card rather than as a boundary.
  expect(screen.queryByText("Reindex what changed")).toBeNull();
  expect(screen.queryByText("Rebuild the whole index")).toBeNull();
});

// Withheld, not absent: the card shares the maintenance page with sections a
// non-ops seat does read, and beside a job-health card that explains its own
// emptiness. A card that vanished would read as "the index is fine".
it("says the search index is withheld, and asks the server for nothing", async () => {
  const { requests } = mount(
    {},
    {
      "GET /embeddings/reindex/status": () => json(STATUS_NEEDED),
    },
  );

  // A rep holds no grant on embedding_reindex at all (migration 0115).
  expect(
    await screen.findByText(/only an admin or ops can see the search index/i),
  ).toBeTruthy();
  expect(screen.getByText("Search index")).toBeTruthy();
  // No status and no actions — and the half of the old behaviour worth keeping:
  // the denial is already known, so the status query never fires rather than
  // turning it into an "unavailable" the reader cannot act on.
  expect(screen.queryByText("Reindex needed")).toBeNull();
  expect(screen.queryByText("Index status")).toBeNull();
  expect(screen.queryByText("Review & reindex")).toBeNull();
  expect(screen.queryByText("Rebuild index")).toBeNull();
  expect(screen.queryByText("Rebuild the whole index")).toBeNull();
  expect(requests.some((r) => r.url === "/embeddings/reindex/status")).toBe(
    false,
  );
});
