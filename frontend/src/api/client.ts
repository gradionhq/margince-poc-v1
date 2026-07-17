import createClient from "openapi-fetch";
import type { paths } from "./schema";

// The ONE API seam (architecture/01: the frontend depends on the generated
// contract, never Go internals). Types come from src/api/schema.d.ts —
// regenerate with `pnpm gen:api` after a crm.yaml change; never hand-edit.
//
// One installation serves one organization (A107/ADR-0061): the server
// resolves its singleton organization itself — the client sends no tenant
// selector, only the session cookie.

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
