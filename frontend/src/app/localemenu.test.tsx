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

// `role="menu"` is a promise about the keyboard, not a class name. This is the
// only in-app control for changing language, so a reader who loses focus to
// <body> here has no way back to it — the plain button this replaced kept
// focus for free, and these cases are what stops that being a regression.
describe("LocaleMenu keyboard model", () => {
  const nameOf = (locale: (typeof LOCALES)[number]) =>
    translate("en", localeNameKey(locale));

  const open = async () => {
    const trigger = screen.getByRole("button");
    await userEvent.click(trigger);
    return trigger;
  };

  it("moves focus into the list, onto the language already in force", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await open();
    expect(document.activeElement).toBe(
      screen.getByRole("menuitemradio", { name: nameOf("en") }),
    );
  });

  it("walks the list with the arrows, wrapping, and jumps with Home and End", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await open();
    const items = screen.getAllByRole("menuitemradio");
    const last = items.length - 1;

    await userEvent.keyboard("{ArrowDown}");
    expect(document.activeElement, "one down from the first").toBe(items[1]);
    await userEvent.keyboard("{ArrowUp}{ArrowUp}");
    expect(document.activeElement, "up past the first wraps to the last").toBe(
      items[last],
    );
    await userEvent.keyboard("{ArrowDown}");
    expect(document.activeElement, "down past the last wraps round").toBe(
      items[0],
    );
    await userEvent.keyboard("{End}");
    expect(document.activeElement).toBe(items[last]);
    await userEvent.keyboard("{Home}");
    expect(document.activeElement).toBe(items[0]);
  });

  it("carries one tabstop for the whole list, following the focused row", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await open();
    const items = screen.getAllByRole("menuitemradio");
    await userEvent.keyboard("{ArrowDown}");
    expect(items.map((item) => item.getAttribute("tabindex"))).toEqual(
      items.map((_, index) => (index === 1 ? "0" : "-1")),
    );
  });

  it("hands focus back to the trigger when Escape closes it", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    const trigger = await open();
    await userEvent.keyboard("{Escape}");
    expect(document.activeElement).toBe(trigger);
  });

  it("hands focus back to the trigger when a language is chosen by keyboard", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    const trigger = await open();
    await userEvent.keyboard("{ArrowDown}{Enter}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(trigger.textContent).toContain("DE");
  });

  it("hands focus back to the trigger when a click outside closes it", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    const trigger = await open();
    await userEvent.click(document.body);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("closes when the reader tabs out, rather than staying expanded behind them", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    const trigger = await open();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    await userEvent.tab();
    expect(screen.queryByRole("menu")).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("names the list it opens, so it is not announced as just 'menu'", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await open();
    expect(
      screen.getByRole("menu", { name: translate("en", "locale.switchLabel") }),
    ).toBeTruthy();
  });

  // The document is declared to be in ONE language (LocaleProvider, WCAG
  // 3.1.1). Every name here is in a different one, so each has to say so or a
  // screen reader reads "Tiếng Việt" with English phonemes.
  it("voices each language name in its own language", async () => {
    mount(<LocaleMenu className="iconbtn" />);
    await open();
    for (const option of LOCALES) {
      const name = nameOf(option);
      const item = screen.getByRole("menuitemradio", { name });
      expect(item.querySelector(`[lang="${option}"]`)?.textContent, name).toBe(
        name,
      );
    }
  });
});
