/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "./App";
import { LocaleProvider } from "./i18n";

// B-EP09.17: the locale switch flips the whole UI between DE and EN. The app
// mounts in the A24 default (de); one click renders the English chrome. The
// browser-level e2e twin of this test rides the 09.22 harness.

afterEach(() => {
  cleanup();
  window.location.hash = "";
});

describe("locale switch", () => {
  it("mounts in German (A24) and flips the chrome to English on switch", async () => {
    render(
      <LocaleProvider>
        <App />
      </LocaleProvider>,
    );
    // German default: the rail carries German labels
    expect(screen.getByRole("link", { name: "Kontakte" })).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: "Auf Englisch umschalten" }),
    );
    expect(screen.getByRole("link", { name: "Contacts" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Kontakte" })).toBeNull();
  });
});
