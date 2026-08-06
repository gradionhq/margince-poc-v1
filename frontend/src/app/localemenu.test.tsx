/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LOCALES, LocaleProvider, localeNameKey, translate } from "../i18n";
import { LocaleMenu } from "./localemenu";

afterEach(cleanup);

const mount = (ui: ReactNode) =>
  render(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("LocaleMenu", () => {
  it("is a single collapsed control until opened", () => {
    mount(<LocaleMenu className="iconbtn" />);
    expect(screen.getAllByRole("button")).toHaveLength(1);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  // The trigger's visible face is a two-letter code, so its accessible name is
  // the only place a screen-reader or voice-control user learns which language
  // is currently on.
  it("announces the current language, not just that it switches one", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    expect(screen.getByRole("button").getAttribute("aria-label")).toContain(
      translate("en", localeNameKey("en")),
    );

    await userEvent.click(screen.getByRole("button"));
    await userEvent.click(
      screen.getByRole("menuitemradio", { name: /Tiếng Việt/ }),
    );
    expect(screen.getByRole("button").getAttribute("aria-label")).toContain(
      translate("vi", localeNameKey("vi")),
    );
  });

  it("lists every shipped locale and marks the current one", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await userEvent.click(screen.getByRole("button"));
    const items = screen.getAllByRole("menuitemradio");
    expect(items).toHaveLength(LOCALES.length);
    expect(
      items.filter((item) => item.getAttribute("aria-checked") === "true"),
    ).toHaveLength(1);
  });

  it("switches the locale and closes", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await userEvent.click(screen.getByRole("button"));
    await userEvent.click(
      screen.getByRole("menuitemradio", { name: /Deutsch/ }),
    );
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("button").textContent).toContain("DE");
  });

  it("closes on Escape without changing the locale", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await userEvent.click(screen.getByRole("button"));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("button").textContent).toContain("EN");
  });
});
