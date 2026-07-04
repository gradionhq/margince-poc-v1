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
  // same-origin absolute base: the dev server proxies /v1, the embedded
  // build serves from the api origin itself
  baseUrl:
    typeof window === "undefined" ? "http://localhost" : window.location.origin,
  credentials: "include",
  // resolve the CURRENT global fetch per call (test stubs, SW interception)
  fetch: (request) => globalThis.fetch(request),
});

api.use({
  onRequest({ request }) {
    const slug = workspaceSlug();
    if (slug) {
      request.headers.set("X-Workspace-Slug", slug);
    }
    return request;
  },
});
