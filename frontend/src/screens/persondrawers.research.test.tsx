/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { PersonResearchDrawer } from "./persondrawers";
import { providerCompletedProfile } from "./personprovider.fixtures";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// Two capabilities share this drawer: contact data BOUGHT from a licensed
// provider at the top, and a public-source research run under it. Each has its
// own connection state, and for a while both spoke about "a data provider" — so
// a reader with Surfe connected and eight purchased claims on screen read, one
// paragraph below them, that no data provider was connected.
//
// The invariant this pins is a vocabulary one: the research empty state names
// the thing that is actually missing, and leaves "data provider" to the section
// that means the licensed contact-data sense.

afterEach(() => {
  cleanup();
});

function mountWithBoughtData() {
  installFetchStub({
    "GET /me": meRoute({ person: ["read"] }),
    "POST /people/p-1/research": () =>
      jsonResponse({
        person_id: "p-1",
        state: "not_connected",
        generated_at: "2026-08-18T09:00:00Z",
        claims: [],
      }),
  });
  render(
    <StoryProviders>
      <PersonResearchDrawer
        personId="p-1"
        personName="Dana Buyer"
        providerProfile={providerCompletedProfile}
        open
        onClose={() => undefined}
      />
    </StoryProviders>,
  );
}

describe("the research drawer with no research provider bound", () => {
  it("names the research provider as what is missing, not the data provider whose purchase is on screen", async () => {
    mountWithBoughtData();

    // The purchase, above: what a licensed contact-data provider was paid for.
    expect(await screen.findByText("Bought contact data")).toBeDefined();

    const empty = await screen.findByText(/No research provider is connected/);
    // "data provider" belongs to the section above. Reusing it here is the
    // contradiction — the drawer would deny the connection whose fruit it is
    // displaying.
    expect(empty.textContent ?? "").not.toContain("data provider");
  });

  it("still says Margince does not research a person on its own authority", async () => {
    // The reword changes which capability the sentence is about, not the promise
    // it carries: no provider means no crawl, and that is a guarantee of the
    // research port itself rather than a line about licensed contact data.
    mountWithBoughtData();

    expect(
      await screen.findByText(/never researches a person on its own authority/),
    ).toBeDefined();
  });
});
