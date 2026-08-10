/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { DossierPanel } from "./companydossier";

// The dossier's whole claim is that every sentence can be checked. So the tests
// are about what it refuses to render: a section it cannot name, a payload it
// cannot parse, and staleness it would rather hide.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type Dossier = components["schemas"]["OrganizationDossier"];

// A COMPLETE OrganizationDossier, not a cast one — a fixture asserted into the
// contract type can drop a required field and still compile.
const DESCRIBED: Dossier = {
  organization_id: "o-1",
  generated_at: "2026-08-08T09:00:00Z",
  generated_by: "deterministic",
  sections: [
    {
      kind: "summary",
      sentences: [
        {
          text: "What they offer: load-shifting software.",
          nature: "fact",
          evidence: [{ entity_type: "profile_field", entity_id: "p-1" }],
        },
      ],
    },
    {
      kind: "markets",
      sentences: [
        {
          text: "Ideal customer: energy-intensive manufacturers.",
          nature: "fact",
          evidence: [{ entity_type: "profile_field", entity_id: "p-2" }],
        },
      ],
    },
  ],
};

function serving(body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    ),
  );
}

function show() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <DossierPanel orgId="o-1" enabled />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("what this company is", () => {
  it("reads as prose, without a heading over every few sentences", async () => {
    serving(DESCRIBED);
    show();

    expect(
      await screen.findByText(/What they offer: load-shifting software/),
    ).toBeTruthy();
    // The card has ONE heading — its own. The sections still order the prose;
    // they no longer announce themselves, because a label over three sentences
    // about one company turned a reading into a form.
    expect(screen.queryByRole("heading", { level: 3 })).toBeNull();
    expect(screen.queryByText("In short")).toBeNull();
  });

  it("renders every sentence it was given, and invents none", async () => {
    // The server omits a section whose sentences all fell out of the grounding
    // filter, so the panel is handed only populated ones. This pins that the
    // panel renders exactly those — prose it composed for a kind with nothing
    // under it would read as a finding of nothing.
    serving(DESCRIBED);
    show();

    await screen.findByText(/What they offer: load-shifting software/);
    // The sentences, not every row: the card renders one collected sources row
    // underneath them, which is a receipt rather than a claim.
    const sentences = screen
      .getAllByRole("listitem")
      .filter((item) => !item.classList.contains("co-brief-sources"))
      .map((item) => item.textContent?.trim());
    expect(sentences).toEqual([
      expect.stringContaining("What they offer: load-shifting software."),
      expect.stringContaining(
        "Ideal customer: energy-intensive manufacturers.",
      ),
    ]);
  });

  it("says a dossier is stale beside the content, never instead of it", async () => {
    serving({ ...DESCRIBED, needs_refresh: true });
    show();

    expect(await screen.findByText("Read over a month ago")).toBeTruthy();
    // A stale dossier is more useful than none, so the content stays.
    expect(
      screen.getByText(/What they offer: load-shifting software/),
    ).toBeTruthy();
  });

  it("distinguishes a company nobody has described from one it cannot read", async () => {
    serving({ ...DESCRIBED, sections: [] });
    show();

    expect(
      await screen.findByText(/Nothing has been recorded about this company/),
    ).toBeTruthy();
    expect(screen.queryByText(/could not be read/)).toBeNull();
  });

  it("reports a payload it cannot parse as exactly that", async () => {
    // A schema skew. Rendering it as "nothing recorded" would send the reader
    // off to gather facts that are already there.
    serving({ organization_id: "o-1" });
    show();

    expect(
      await screen.findByText(/This description could not be read/),
    ).toBeTruthy();
    expect(screen.queryByText(/Nothing has been recorded/)).toBeNull();
  });

  it("is absent, not empty, for a workspace reading from an incumbent", () => {
    serving(DESCRIBED);
    const { container } = render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <LocaleProvider initial="en">
          <DossierPanel orgId="o-1" enabled={false} />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    expect(container.textContent).toBe("");
  });
});
