import createClient from "openapi-fetch";
import type { paths } from "./schema";

// The ONE API seam (architecture/01: the frontend depends on the generated
// contract, never Go internals). Types come from src/api/schema.d.ts —
// regenerate with `pnpm gen:api` after a crm.yaml change; never hand-edit.
//
// Workspace resolution: prod uses the subdomain; local dev sends the
// X-Workspace-Slug header (same convention as curl against :8080). The slug
// is a dev-side setting, persisted locally — it is not tenant authority
// (the session cookie is; the backend enforces).

const WORKSPACE_KEY = "margince.workspaceSlug";

export function workspaceSlug(): string | null {
  return globalThis.localStorage?.getItem(WORKSPACE_KEY) ?? null;
}

export function setWorkspaceSlug(slug: string): void {
  globalThis.localStorage?.setItem(WORKSPACE_KEY, slug);
}

export const api = createClient<paths>({
  // same-origin absolute base + the /v1 mount: contract paths are
  // unprefixed, the server serves them under /v1 (same as curl :8080/v1/me)
  baseUrl:
    typeof globalThis.window === "undefined"
      ? "http://localhost/v1"
      : `${globalThis.location.origin}/v1`,
  credentials: "include",
  // resolve the CURRENT global fetch per call (test stubs, SW interception)
  fetch: (request) => globalThis.fetch(request),
});

api.use({
  onRequest({ request }) {
    // /v1/public/* is the anonymous surface (security: [] in the contract):
    // the host_slug in the path is the whole address — no session, no
    // workspace header. Everything else carries the dev-side slug.
    if (new URL(request.url).pathname.startsWith("/v1/public/")) {
      return request;
    }
    const slug = workspaceSlug();
    if (slug) {
      request.headers.set("X-Workspace-Slug", slug);
    }
    return request;
  },
});
