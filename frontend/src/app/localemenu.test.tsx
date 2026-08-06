/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LOCALES, LocaleProvider } from "../i18n";
import { LocaleMenu } from "./localemenu";

afterEach(cleanup);

const mount = (ui: ReactNode) =>
  render(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("LocaleMenu", () => {
  it("is a single collapsed control until opened", () => {
    mount(<LocaleMenu />);
    expect(screen.getAllByRole("button")).toHaveLength(1);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("lists every shipped locale and marks the current one", async () => {
    mount(<LocaleMenu />);
    await userEvent.click(screen.getByRole("button"));
    const items = screen.getAllByRole("menuitemradio");
    expect(items).toHaveLength(LOCALES.length);
    expect(
      items.filter((item) => item.getAttribute("aria-checked") === "true"),
    ).toHaveLength(1);
  });

  it("switches the locale and closes", async () => {
    mount(<LocaleMenu />);
    await userEvent.click(screen.getByRole("button"));
    await userEvent.click(
      screen.getByRole("menuitemradio", { name: /Deutsch/ }),
    );
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("button").textContent).toContain("DE");
  });

  it("closes on Escape without changing the locale", async () => {
    mount(<LocaleMenu />);
    await userEvent.click(screen.getByRole("button"));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("button").textContent).toContain("EN");
  });
});
