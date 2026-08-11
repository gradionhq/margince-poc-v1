import type { paths } from "@composition/schema";
import createClient from "openapi-fetch";

// The ONE API seam (architecture/01: the frontend depends on the generated
// contract, never Go internals). Types come from src/api/schema.d.ts —
// regenerate with `pnpm gen:api` after a crm.yaml change; never hand-edit.
//
// The specifier is the composition alias, not "./schema", and that is the whole
// of what makes an extension unit's screen able to use this client (ADR-0069).
// The VANILLA lane resolves it to src/api/schema.d.ts — the base contract — so
// `api.GET("/ext/notes")` is correctly a type error there: that
// installation does not serve the route. The COMPOSED lane
// (tsconfig.composed.json, `make fe-typecheck-composed`) resolves it to the
// types generated from the MERGED contract, where the route exists and is
// typed. One client, no wrapper, no cast, and the vanilla tree still refuses to
// compile a call it could not answer.
//
// It is a TYPE-ONLY import, so nothing changes at runtime and no bundler alias
// is needed: `verbatimModuleSyntax` erases the line entirely.
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
