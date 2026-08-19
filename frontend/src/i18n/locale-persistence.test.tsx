/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { LocaleProvider, storedLocale, useLocale } from "./index";

// A language a reader picks has to survive a reload, or the switcher reads as
// broken: the next load falls back to the browser's preference and silently
// undoes the choice. It also has to survive it WITHOUT freezing anything the
// reader did not choose — a stored browser default would outlive a change to
// the browser itself.

const STORAGE_KEY = "margince.locale";

function Probe() {
  const { locale, setLocale } = useLocale();
  return (
    <>
      <span data-testid="locale">{locale}</span>
      <button type="button" onClick={() => setLocale("de")}>
        pick de
      </button>
    </>
  );
}

const mount = (initial?: "en" | "de" | "vi") =>
  render(
    <LocaleProvider initial={initial}>
      <Probe />
    </LocaleProvider>,
  );

const shown = () => screen.getByTestId("locale").textContent;

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe("a locale a reader picked", () => {
  it("is read back by the next mount, which is what a reload is", async () => {
    mount();
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "pick de" }));
    expect(shown()).toBe("de");
    cleanup();

    mount();
    expect(shown()).toBe("de");
  });

  it("is the value that reaches storage, under a namespaced key", async () => {
    mount();
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "pick de" }));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("de");
  });

  it("yields to an explicit initial, which is the more authoritative source", () => {
    localStorage.setItem(STORAGE_KEY, "de");
    mount("vi");
    expect(shown()).toBe("vi");
  });
});

describe("a locale nobody picked", () => {
  // Storing the detected default would freeze it: a reader who later changes
  // their browser's language would keep being served the old one from a value
  // they never set.
  it("is not written to storage by a plain mount", () => {
    mount();
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("is what a stale or hand-edited stored value falls back to", () => {
    // A stored string outlives the release that wrote it, so a locale we have
    // stopped shipping must not reach the catalogs as a key they cannot serve.
    localStorage.setItem(STORAGE_KEY, "fr");
    expect(storedLocale()).toBeNull();
    mount();
    expect(shown()).not.toBe("fr");
  });

  it("is what junk in the slot falls back to", () => {
    localStorage.setItem(STORAGE_KEY, "{}");
    expect(storedLocale()).toBeNull();
  });

  it("reads as absent when nothing is stored", () => {
    expect(storedLocale()).toBeNull();
  });
});
