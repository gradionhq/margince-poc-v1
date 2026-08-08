/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import {
  EvidenceVerdict,
  factClaim,
  profileFieldClaim,
} from "./evidenceverdict";

// ADR-0085 gave a human two verbs over a machine's claim, and for a while
// nothing in the product could call either. These pin that the buttons reach
// the right endpoint with the right body — a confirm that quietly PATCHed, or a
// correction addressed by the wrong key, looks identical on screen.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const ORG = "o-1";

const field: components["schemas"]["CompanyProfileField"] = {
  field: "industry",
  value: "Fahrzeugbau",
  source: "site_read",
  captured_by: "agent:deepread",
  updated_at: "2026-08-01T09:00:00Z",
};

const fact: components["schemas"]["OrganizationFact"] = {
  category: "company",
  field: "phone",
  value: "+49 30 1234",
  value_key: "",
  source: "site_read",
  captured_by: "agent:deepread",
  id: "f-1",
  updated_at: "2026-08-01T09:00:00Z",
};

// The calls the component made, so a test can assert the METHOD and the PATH
// rather than that something happened.
function recordCalls() {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push({
        method: request.method,
        path: new URL(request.url).pathname,
        body: request.body ? await request.clone().json() : undefined,
      });
      return new Response("{}", {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }),
  );
  return calls;
}

function wrap(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("a human's verdict on a machine's claim", () => {
  it("confirms a profile field without changing its value", async () => {
    const calls = recordCalls();
    wrap(
      <EvidenceVerdict
        orgId={ORG}
        claim={profileFieldClaim(ORG, field)}
        canEdit
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    // POST to the confirm sub-path, with no body: a confirmation that carried a
    // value would be a correction wearing the wrong verb.
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].method).toBe("POST");
    expect(calls[0].path).toBe(
      "/v1/organizations/o-1/profile-fields/industry/confirm",
    );
    expect(calls[0].body).toBeUndefined();
  });

  it("corrects a profile field by sending the new value", async () => {
    const calls = recordCalls();
    wrap(
      <EvidenceVerdict
        orgId={ORG}
        claim={profileFieldClaim(ORG, field)}
        canEdit
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Correct" }));
    const input = screen.getByRole("textbox", { name: "Corrected value" });
    await userEvent.clear(input);
    await userEvent.type(input, "Automotive");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].method).toBe("PATCH");
    expect(calls[0].path).toBe("/v1/organizations/o-1/profile-fields/industry");
    expect(calls[0].body).toEqual({ value: "Automotive" });
  });

  it("addresses a single-value fact by its bare-colon key", async () => {
    const calls = recordCalls();
    wrap(<EvidenceVerdict orgId={ORG} claim={factClaim(ORG, fact)} canEdit />);

    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    // `phone:` — the field, the separator, and an empty value_key. A key that
    // lost either half addresses a different row or none at all, and the server
    // answers 404 or 422 for what looks to the user like a working button.
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].path).toBe("/v1/organizations/o-1/facts/phone%3A/confirm");
  });

  it("offers no verdict to a reader who cannot update the company", () => {
    wrap(
      <EvidenceVerdict
        orgId={ORG}
        claim={profileFieldClaim(ORG, field)}
        canEdit={false}
      />,
    );
    expect(screen.queryByRole("button", { name: "Confirm" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Correct" })).toBeNull();
  });

  it("says who stood behind a value rather than offering to confirm it again", () => {
    wrap(
      <EvidenceVerdict
        orgId={ORG}
        claim={profileFieldClaim(ORG, {
          ...field,
          source: "human",
          verified_at: "2026-08-02T09:00:00Z",
        })}
        canEdit
      />,
    );
    // A value a person already stood behind is not a claim awaiting a verdict.
    expect(screen.queryByRole("button", { name: "Confirm" })).toBeNull();
    expect(screen.getByText(/Confirmed by a person/)).toBeTruthy();
  });
});
