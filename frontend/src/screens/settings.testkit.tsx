import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderResult, render as rtlRender } from "@testing-library/react";
import type { ReactNode } from "react";
import { vi } from "vitest";
import type { RbacObject } from "../app/capability";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { SettingsRail } from "../app/shell";
import { LocaleProvider } from "../i18n";
import { SettingsScreen } from "./settings";

// The render helpers and grant fixtures every `settings*.test.tsx` suite needs,
// in one place. Settings is ONE route carrying fourteen entries, so its coverage
// is split by subject across several files — and each of them wants the same
// three things: a fetch mock that answers per endpoint, a render that carries
// the query client and the locale the screen reads, and the grant fixtures that
// decide what a principal is shown.
//
// It is NOT a *.test.* file, on purpose: the design-system and lint gates skip
// test files, and a helper that renders the real screen should answer to the
// same rules the screen does.

// The content type is part of the answer, not decoration: the API client reads
// it before it parses, so a mock that omits it is not the response the product
// receives.
export function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// What a settings render hands back, written out rather than inferred, and the
// annotation is load-bearing: this module is compiled by the app project, which
// emits declarations, and an inferred return type here reaches a transitive
// dependency of the testing library that tsc cannot name from outside it.
export type SettingsRender = RenderResult & { client: QueryClient };

// The client comes back with the render so a test can read a query's settled
// state, not just the DOM: "the answer is in the cache" is the fact a nav
// assertion about an absent tab has to stand on.
export const render = (ui: ReactNode): SettingsRender => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return {
    ...rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">{ui}</LocaleProvider>
      </QueryClientProvider>,
    ),
    client,
  };
};

// A settings route renders in two halves, and the sidebar owns one of them: the
// tabs are the shell's SECOND NAVIGATION LEVEL, fed by the section this screen
// publishes (useSettingsSection). So a claim about which tabs a principal is
// offered renders the real rail — the production wiring, not a copy of it — and
// a claim about a tab's content renders the screen.
const railFor = (tab?: string) => (
  <SettingsRail route={{ screen: "settings", id: tab }} />
);

export const renderNav = (tab?: string): SettingsRender => render(railFor(tab));

// Both halves, for a claim that spans them: the tab is in the nav AND its cards
// are on the page.
export const renderSettings = (tab?: string): SettingsRender =>
  render(
    <>
      {railFor(tab)}
      <SettingsScreen tab={tab} />
    </>,
  );

// The Organization tab group is composed from its MEMBERS, and OPENING AN ENTRY
// IS A READ: every predicate asks for a read grant on something the entry shows,
// while the write affordances inside it gate themselves. So a fixture that wants
// an entry in the nav has to name the READ, and one that also wants the authoring
// controls names the write on top — two separate claims, and a grant list holding
// writes alone reaches no entry at all.
export const PIPELINE_ADMIN: GrantSpec = {
  pipeline: ["read", "create", "update"],
};
const ORG_ADMIN: GrantSpec = {
  ...PIPELINE_ADMIN,
  custom_field: ["read", "create", "update"],
  // The consent registry's own gate (consent/store.go demands person:read), which
  // every seeded role holds — so a fixture standing in for a real principal has to
  // carry it or Privacy & audit disappears for reasons the test is not about.
  person: ["read"],
};

// The read grant on ONE object, as a GrantSpec.
//
// Built by assignment rather than as a literal, because a computed key whose own
// type is a union widens the object to `{ [x: string]: string[] }` — which does
// not satisfy GrantSpec, and only fails in `tsc -b`, where test files are
// typechecked, rather than under the app project alone.
export function readOn(object: RbacObject): GrantSpec {
  // `person:read` rides along because Privacy asks for it, and every seeded role
  // holds it — so a case about ONE object's entry is not also a case about losing
  // the consent registry. Isolating the object under test means holding the floor
  // steady, not stripping it.
  const spec: GrantSpec = { person: ["read"] };
  spec[object] = ["read"];
  return spec;
}

// Routed by URL so every card on the screen gets an honest per-endpoint
// answer; the cards not under test render their empty states.
export function settingsBackend() {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      const me = meFixture({
        roles: ["admin", "field_marketing"],
        allow: ORG_ADMIN,
      });
      return jsonResponse({
        ...me,
        user: { ...me.user, email: "ada@acme.test" },
      });
    }
    if (url.includes("/passports")) {
      return jsonResponse({
        data: [
          {
            id: "pp-1",
            label: "Scout",
            scopes: ["read"],
            created_at: "2026-07-01T08:00:00Z",
            expires_at: null,
            revoked_at: null,
          },
        ],
        page: { next_cursor: null, has_more: false },
      });
    }
    return jsonResponse({
      data: [],
      page: { next_cursor: null, has_more: false },
    });
  });
}

// One audit-log entry carrying a full attribution trail (before/after diff,
// agent passport, on-behalf-of human, authorization rule, and evidence) so
// the expand panel has every field to render honestly.
export const auditEntry = {
  id: "al-1",
  actor_type: "agent",
  actor_id: "agent:sdr",
  passport_id: "pp-9",
  on_behalf_of: "u-1",
  action: "update",
  entity_type: "person",
  entity_id: "p-1",
  before: { stage: "new" },
  after: { stage: "qualified" },
  authorization_rule: "role:admin",
  evidence: { snippet: "Reply confirmed budget", source: "email:msg-1" },
  occurred_at: "2026-07-10T09:00:00Z",
};

// A background system with nothing queued and nothing failed — GET
// /admin/job-health's honest quiet answer.
export const IDLE_JOB_HEALTH = {
  generated_at: "2026-08-13T09:30:00Z",
  kinds: [],
  recent_failures: [],
};
