/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { ExtensionDescriptor } from "@composition/extensions";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ExtensionUnitsCard } from "./extension-units";

// The card reads the COMPOSED registry, which is empty in this lane and in
// every core developer's checkout — so the populated case is reached by
// standing in for that module. The alternative, threading a registry prop
// through the card purely so a test could pass one, would put a seam in
// production code that only tests use.
//
// app/extensions.ts is mocked rather than @composition/extensions, because the
// alias resolves to the committed empty stub whose emptiness is itself pinned
// (extensions.test.ts); mocking the module under it would make this suite pass
// while the real filter was never called.
const { unitsForSecretScope } = vi.hoisted(() => ({
  unitsForSecretScope:
    vi.fn<(scope: "user" | "workspace") => readonly ExtensionDescriptor[]>(),
}));

vi.mock("../app/extensions", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../app/extensions")>()),
  unitsForSecretScope,
}));

const DISPACT: ExtensionDescriptor = {
  name: "dispact-connector",
  secretScope: "user",
  verbs: [],
};

function renderCard(scope: "user" | "workspace") {
  return render(
    <LocaleProvider>
      <ExtensionUnitsCard scope={scope} />
    </LocaleProvider>,
  );
}

afterEach(() => {
  cleanup();
  unitsForSecretScope.mockReset();
});

describe("the units offered on a settings page", () => {
  it("names the unit and links to its own screen", () => {
    unitsForSecretScope.mockReturnValue([DISPACT]);
    renderCard("user");
    // The link is NAMED by the unit, not by a shared verb: a list of "Open"
    // links reads as "Open, Open, Open" to a screen reader, and the unit name
    // is the one word that tells them apart.
    const link = screen.getByRole("link", { name: "dispact-connector" });
    expect(link.getAttribute("href")).toBe("#/ext/dispact-connector");
  });

  it("asks for the scope of the page it was placed on", () => {
    unitsForSecretScope.mockReturnValue([]);
    renderCard("workspace");
    expect(unitsForSecretScope).toHaveBeenCalledWith("workspace");
  });

  // A heading over an empty list is a promise the installation did not make —
  // and it is the state of every vanilla checkout, so it is the common case
  // rather than an edge one.
  it("renders nothing at all when no unit is offered", () => {
    unitsForSecretScope.mockReturnValue([]);
    const { container } = renderCard("user");
    expect(container.innerHTML).toBe("");
  });

  it("gives every offered unit its own row", () => {
    unitsForSecretScope.mockReturnValue([
      DISPACT,
      { name: "notes", secretScope: "user", verbs: [] },
    ]);
    renderCard("user");
    expect(screen.getAllByRole("link")).toHaveLength(2);
    expect(
      screen.getByRole("link", { name: "notes" }).getAttribute("href"),
    ).toBe("#/ext/notes");
  });
});
