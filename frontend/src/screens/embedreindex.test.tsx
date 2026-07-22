/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { EmbedReindexCard } from "./embedreindex";

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
  reindex_needed: true,
  entities_pending: 42,
  per_workspace: [
    {
      workspace_id: "018f3a1b-0000-7000-8000-000000000001",
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
      workspace_id: "018f3a1b-0000-7000-8000-000000000001",
      entities_pending: 0,
    },
  ],
};

const PREVIEW = {
  entities_pending: 42,
  estimated_ai_tokens: 12_000,
  estimated_cost_minor: 350,
  estimate_quality: "heuristic",
  currency: "USD",
  computed_at: "2026-07-22T00:00:00Z",
  per_workspace: [
    {
      workspace_id: "018f3a1b-0000-7000-8000-000000000001",
      entities_pending: 42,
      estimated_ai_tokens: 12_000,
      utilization_impact: "degraded",
    },
  ],
};

function mount(
  roles: string[],
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
        : (init?.body as string | undefined);
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
        return json({
          user: { id: "u1", email: "a@example.test", display_name: "A" },
          roles,
        });
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

it("shows the per-workspace estimate + utilization disclosure and disables confirm until the estimate loads", async () => {
  let resolvePreview: (value: Response) => void = () => {};
  const previewPromise = new Promise<Response>((resolve) => {
    resolvePreview = resolve;
  });
  mount(["admin"], {
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
  // The per-workspace utilization disclosure (AIRT-PARAM-9..11 band).
  expect(screen.getByText(/would enter economy mode|degraded/i)).toBeTruthy();
});

it("posts previewed_identity from the status read and force:false on a plain confirm", async () => {
  const { requests } = mount(["ops"], {
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
  const { requests } = mount(["admin"], {
    "GET /embeddings/reindex/status": () => json(STATUS_IDLE),
    "GET /embeddings/reindex/preview": () => json(PREVIEW),
    "POST /embeddings/reindex": () => json({ ...STATUS_IDLE }, 202),
  });

  expect(await screen.findByText("Rebuild index")).toBeTruthy();
  // The "Review & reindex" trigger only appears when a reindex is actually
  // needed — Rebuild is the always-available affordance instead.
  expect(screen.queryByText("Review & reindex")).toBeNull();

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

it("hides the confirm/rebuild actions for a role without the embedding_reindex write grant", async () => {
  mount(["rep"], {
    "GET /embeddings/reindex/status": () => json(STATUS_NEEDED),
  });

  // The status read is wide-granted — a rep still sees it...
  expect(await screen.findByText("Reindex needed")).toBeTruthy();
  // ...but never the write affordances, which would only ever 403 for them.
  expect(screen.queryByText("Review & reindex")).toBeNull();
  expect(screen.queryByText("Rebuild index")).toBeNull();
});
