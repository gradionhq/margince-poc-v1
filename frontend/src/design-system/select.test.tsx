/** @vitest-environment jsdom */
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Field } from "./atoms";
import type { SelectOption, SelectProps } from "./select";
import { Select } from "./select";
import { pickOption } from "./select-testing";

// The specs for the control that replaced the native <select>. They are grouped
// by the promise each group keeps, because those promises are what the ~30 screen
// call sites and their suites depend on: the trigger is findable as a combobox,
// the popup is a real listbox, the keyboard drives all of it, and nothing commits
// a value the reader did not choose.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const STAGES: readonly SelectOption[] = [
  { value: "qualify", label: "Qualify" },
  { value: "proposal", label: "Proposal" },
  { value: "won", label: "Won" },
];

// A list with a hole in the middle: the disabled entry sits between two enabled
// ones, so a keyboard walk that fails to skip it stops on it rather than landing
// on the next real option.
const WITH_DISABLED: readonly SelectOption[] = [
  { value: "qualify", label: "Qualify" },
  { value: "blocked", label: "Blocked", disabled: true },
  { value: "won", label: "Won" },
];

function renderSelect(props: Partial<SelectProps> = {}) {
  const changes: string[] = [];
  const merged: SelectProps = {
    "aria-label": "Stage",
    options: STAGES,
    value: "",
    onChange: (next: string) => {
      changes.push(next);
    },
    ...props,
  };
  render(<Select {...merged} />);
  return {
    changes,
    trigger: screen.getByRole("combobox", { name: "Stage" }),
  };
}

// jsdom lays nothing out, so every box it reports is zeros. A test that needs the
// geometry decisions — flip above, close when gone — states the box the browser
// would have measured.
function rectAt(left: number, top: number, width: number, height: number) {
  return {
    x: left,
    y: top,
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
    toJSON: () => ({}),
  } satisfies DOMRect;
}

// A controlled harness for the cases that need the trigger face to follow the
// commit — the component is controlled, so a test that never feeds the value back
// can only ever prove the callback fired.
function ControlledSelect({ start = "" }: Readonly<{ start?: string }>) {
  const [value, setValue] = useState(start);
  return (
    <Select
      aria-label="Stage"
      options={STAGES}
      value={value}
      onChange={setValue}
      placeholder="Pick a stage"
    />
  );
}

describe("the trigger", () => {
  it("is a combobox naming the selected option, and shows the placeholder when nothing is selected", () => {
    const { trigger } = renderSelect({ placeholder: "Pick a stage" });

    expect(trigger.tagName).toBe("BUTTON");
    expect(trigger.getAttribute("type")).toBe("button");
    expect(trigger.getAttribute("aria-haspopup")).toBe("listbox");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(trigger.textContent).toContain("Pick a stage");

    cleanup();
    renderSelect({ value: "won" });
    expect(
      screen.getByRole("combobox", { name: "Stage" }).textContent,
    ).toContain("Won");
  });

  // A value the option list no longer offers (a stage removed from the pipeline,
  // a stale query param) must not render as an empty control that looks chosen.
  it("falls back to the placeholder when the value matches no option", () => {
    const { trigger } = renderSelect({
      value: "retired",
      placeholder: "Pick a stage",
    });
    expect(trigger.textContent).toContain("Pick a stage");
  });

  // The Field atom hands its control an id, the required flag and the hint to be
  // described by — every screen spreads that object whole, so all three have to
  // land on the trigger, and the <label> has to name it.
  it("takes the id, required state and description a Field hands it", () => {
    render(
      <Field label="Stage" hint="Only closed stages freeze the rate" required>
        {(control) => (
          <Select
            {...control}
            options={STAGES}
            value="won"
            onChange={() => {}}
          />
        )}
      </Field>,
    );

    const trigger = screen.getByRole("combobox", { name: "Stage" });
    expect(trigger.getAttribute("aria-required")).toBe("true");
    const describedBy = trigger.getAttribute("aria-describedby") ?? "";
    expect(document.getElementById(describedBy)?.textContent).toBe(
      "Only closed stages freeze the rate",
    );
  });

  it("merges a caller's className with its own, and carries aria-invalid through", () => {
    const { trigger } = renderSelect({
      className: "stage-picker",
      "aria-invalid": true,
    });
    expect([...trigger.classList].sort()).toEqual([
      "input",
      "select-control",
      "stage-picker",
    ]);
    expect(trigger.getAttribute("aria-invalid")).toBe("true");
  });

  // A real <form> reads values off form controls, and a button is not one. The
  // hidden input is how a screen that posts a form still submits this choice.
  it("mirrors the value onto a hidden input when a name is given", () => {
    render(
      <form aria-label="Deal">
        <Select
          aria-label="Stage"
          name="stage"
          options={STAGES}
          value="won"
          onChange={() => {}}
        />
      </form>,
    );
    const form = screen.getByRole("form", { name: "Deal" });
    expect(new FormData(form as HTMLFormElement).get("stage")).toBe("won");
  });

  it("renders no hidden input when no name is given", () => {
    renderSelect();
    expect(document.querySelector("input[type=hidden]")).toBeNull();
  });
});

describe("the popup", () => {
  it("is not in the document at all while closed", () => {
    renderSelect();
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.queryAllByRole("option")).toEqual([]);
  });

  it("opens as a listbox the trigger controls, marking the selected option", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect({ value: "proposal" });

    await user.click(trigger);

    const listbox = screen.getByRole("listbox");
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(trigger.getAttribute("aria-controls")).toBe(listbox.id);
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Qualify",
      "Proposal",
      "Won",
    ]);
    expect(
      screen
        .getByRole("option", { name: "Proposal" })
        .getAttribute("aria-selected"),
    ).toBe("true");
    expect(
      screen.getByRole("option", { name: "Won" }).getAttribute("aria-selected"),
    ).toBe("false");
  });

  // aria-controls may only point at an element that is in the document; a
  // dangling reference is an axe violation and a screen reader cannot follow it.
  it("advertises no aria-controls while there is no listbox to control", () => {
    const { trigger } = renderSelect();
    expect(trigger.hasAttribute("aria-controls")).toBe(false);
  });

  it("is portalled out of its own subtree so an ancestor scroller cannot clip it", async () => {
    const user = userEvent.setup();
    render(
      <div className="scroll" style={{ overflowY: "auto", height: "40px" }}>
        <Select
          aria-label="Stage"
          options={STAGES}
          value=""
          onChange={() => {}}
        />
      </div>,
    );

    await user.click(screen.getByRole("combobox", { name: "Stage" }));

    const listbox = screen.getByRole("listbox");
    expect(listbox.closest(".scroll")).toBeNull();
    expect(listbox.parentElement?.parentElement).toBe(document.body);
    // The geometry arrives as inline coordinates measured against the viewport
    // (`.select-popup` declares `position: fixed`, which jsdom applies no
    // stylesheet to assert) — an absolutely positioned popup would instead be
    // laid out against `.scroll` and scroll away from its own trigger.
    const box = listbox.parentElement;
    expect(box?.className).toBe("select-popup");
    expect(box?.style.top).not.toBe("");
    expect(box?.style.left).not.toBe("");
    expect(box?.style.maxHeight).not.toBe("");
  });

  it("closes when the trigger scrolls out of view", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect();
    await user.click(trigger);
    expect(screen.getByRole("listbox")).toBeTruthy();

    // A toolbar scrolled past the top of the window reports exactly this box.
    vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue(
      rectAt(0, -80, 200, 34),
    );
    act(() => {
      globalThis.dispatchEvent(new Event("scroll"));
    });

    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("flips above the trigger when there is no room below it", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect();
    // A trigger near the bottom of the window: measured room below is a few
    // pixels, so the popup has to open upwards to be readable at all.
    vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue(
      rectAt(40, globalThis.innerHeight - 40, 200, 34),
    );

    await user.click(trigger);

    const box = screen.getByRole("listbox").parentElement;
    expect(box?.dataset.above).toBe("true");
    expect(box?.style.bottom).not.toBe("");
    expect(box?.style.top).toBe("");
    // Matches the trigger's width so the popup reads as the same control.
    expect(box?.style.width).toBe("200px");
  });
});

describe("committing a choice", () => {
  it("reports the chosen VALUE, not an event, and closes back onto the trigger", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect();

    await pickOption(user, trigger, "Won");

    expect(changes).toEqual(["won"]);
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("shows the commit on the trigger face once the caller feeds the value back", async () => {
    const user = userEvent.setup();
    render(<ControlledSelect />);
    const trigger = screen.getByRole("combobox", { name: "Stage" });
    expect(trigger.textContent).toContain("Pick a stage");

    await pickOption(user, trigger, "Proposal");

    expect(trigger.textContent).toContain("Proposal");
  });

  it("toggles shut on a second click of the trigger, committing nothing", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect();

    await user.click(trigger);
    await user.click(trigger);

    expect(screen.queryByRole("listbox")).toBeNull();
    expect(changes).toEqual([]);
  });

  it("closes on a pointer press outside, committing nothing", async () => {
    const user = userEvent.setup();
    render(
      <>
        <ControlledSelect />
        <button type="button">Elsewhere</button>
      </>,
    );
    const trigger = screen.getByRole("combobox", { name: "Stage" });
    await user.click(trigger);

    await user.click(screen.getByRole("button", { name: "Elsewhere" }));

    expect(screen.queryByRole("listbox")).toBeNull();
    expect(trigger.textContent).toContain("Pick a stage");
  });
});

describe("the keyboard", () => {
  it("opens on Enter, Space, ArrowDown and ArrowUp", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect();

    for (const key of ["{Enter}", " ", "{ArrowDown}", "{ArrowUp}"]) {
      trigger.focus();
      await user.keyboard(key);
      expect(screen.getByRole("listbox")).toBeTruthy();
      await user.keyboard("{Escape}");
      expect(screen.queryByRole("listbox")).toBeNull();
    }
  });

  // The active option is tracked by aria-activedescendant, and DOM focus stays
  // on the trigger: moving focus per option would take the reader out of the
  // combobox and break the typeahead they are in the middle of.
  it("moves the active option without moving focus off the trigger", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect();

    trigger.focus();
    await user.keyboard("{ArrowDown}");
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Qualify" }).id,
    );

    await user.keyboard("{ArrowDown}");
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Proposal" }).id,
    );
    expect(document.activeElement).toBe(trigger);

    await user.keyboard("{ArrowUp}");
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Qualify" }).id,
    );
  });

  it("jumps to the ends with Home and End", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect();

    trigger.focus();
    await user.keyboard("{ArrowDown}{End}");
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Won" }).id,
    );

    await user.keyboard("{Home}");
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Qualify" }).id,
    );
  });

  it("opens on the selected option rather than the top of the list", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect({ value: "won" });

    trigger.focus();
    await user.keyboard("{Enter}");

    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Won" }).id,
    );
  });

  it("commits the active option on Enter and on Space", async () => {
    const user = userEvent.setup();
    const first = renderSelect();
    first.trigger.focus();
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(first.changes).toEqual(["proposal"]);

    cleanup();
    const second = renderSelect();
    second.trigger.focus();
    await user.keyboard("{ArrowDown} ");
    expect(second.changes).toEqual(["qualify"]);
  });

  it("finds an option by typing its label", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect();

    trigger.focus();
    await user.keyboard("{ArrowDown}");
    // "pr" rather than "p": one character would match Proposal here anyway, and
    // the buffer is the behaviour worth pinning.
    await user.keyboard("pr");
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Proposal" }).id,
    );

    await user.keyboard("{Enter}");
    expect(changes).toEqual(["proposal"]);
  });

  it("closes on Escape without committing, and hands focus back", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect();

    trigger.focus();
    await user.keyboard("{ArrowDown}{ArrowDown}{Escape}");

    expect(screen.queryByRole("listbox")).toBeNull();
    expect(changes).toEqual([]);
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps a press it claimed away from whatever is listening around it", async () => {
    // A dropdown is nearly always inside something that watches the document for
    // the same keys: `Modal` closes on Escape, a form submits on Enter. A press
    // meant for the open list must end at the list — otherwise abandoning a
    // dropdown takes the whole dialog with it, and choosing an option submits the
    // form around it. Both are the same defect, so both are pinned here.
    const user = userEvent.setup();
    const heard: string[] = [];
    const listen = (event: KeyboardEvent) => heard.push(event.key);
    document.addEventListener("keydown", listen);
    try {
      const { trigger } = renderSelect();
      trigger.focus();
      await user.keyboard("{ArrowDown}");
      await user.keyboard("{Enter}");
      expect(heard).toEqual([]);

      await user.keyboard("{ArrowDown}{Escape}");
      expect(heard).toEqual([]);
      // The press still did its own job — this is not a swallowed keystroke.
      expect(screen.queryByRole("listbox")).toBeNull();

      // And a press the control does NOT claim carries on as normal, or a
      // surrounding shortcut would be dead while the control merely has focus.
      await user.keyboard("{PageDown}");
      expect(heard).toEqual(["PageDown"]);
    } finally {
      document.removeEventListener("keydown", listen);
    }
  });

  it("closes on Tab without committing, and lets focus carry on", async () => {
    const user = userEvent.setup();
    const changes: string[] = [];
    render(
      <>
        <Select
          aria-label="Stage"
          options={STAGES}
          value=""
          onChange={(next) => changes.push(next)}
        />
        <button type="button">Next field</button>
      </>,
    );
    const trigger = screen.getByRole("combobox", { name: "Stage" });

    trigger.focus();
    await user.keyboard("{ArrowDown}{ArrowDown}");
    await user.tab();

    expect(screen.queryByRole("listbox")).toBeNull();
    expect(changes).toEqual([]);
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Next field" }),
    );
  });

  it("keeps the closed control to one tab stop", async () => {
    const user = userEvent.setup();
    render(
      <>
        <ControlledSelect />
        <button type="button">Next field</button>
      </>,
    );

    await user.tab();
    expect(document.activeElement).toBe(
      screen.getByRole("combobox", { name: "Stage" }),
    );
    await user.tab();
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: "Next field" }),
    );
  });
});

describe("disabled states", () => {
  it("does not open a disabled control", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect({ disabled: true });

    await user.click(trigger);
    trigger.focus();
    await user.keyboard("{Enter}");

    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("skips a disabled option on the keyboard and never commits it", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect({ options: WITH_DISABLED });

    trigger.focus();
    await user.keyboard("{ArrowDown}{ArrowDown}");

    // Second stop is Won, not Blocked — the hole in the middle is stepped over.
    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Won" }).id,
    );
    await user.keyboard("{Enter}");
    expect(changes).toEqual(["won"]);
  });

  it("lists a disabled option, marked, but does not commit it on a click", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect({ options: WITH_DISABLED });

    await user.click(trigger);
    const blocked = screen.getByRole("option", { name: "Blocked" });
    expect(blocked.getAttribute("aria-disabled")).toBe("true");

    await user.click(blocked);

    expect(changes).toEqual([]);
    // Still open: nothing was chosen, so the reader has not finished.
    expect(screen.getByRole("listbox")).toBeTruthy();
  });

  it("does not typeahead onto a disabled option", async () => {
    const user = userEvent.setup();
    const { trigger } = renderSelect({ options: WITH_DISABLED });

    trigger.focus();
    await user.keyboard("{ArrowDown}");
    await user.keyboard("bl");

    expect(trigger.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: "Qualify" }).id,
    );
  });
});

describe("reduced motion", () => {
  // jsdom's own matchMedia always answers false, so the reduced arm needs it
  // stubbed to answer true, listener included.
  function stubReducedMotion(reduce: boolean) {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: reduce && query.includes("prefers-reduced-motion"),
      media: query,
      onchange: null,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => false,
    }));
  }

  it("opens the popup with no animation for a reader who asked for less motion", async () => {
    stubReducedMotion(true);
    const user = userEvent.setup();
    const { trigger } = renderSelect();

    await user.click(trigger);

    const box = screen.getByRole("listbox").parentElement;
    expect(box?.dataset.motion).toBe("none");
    // The end state, not nothing: the list is there to be read.
    expect(screen.getAllByRole("option")).toHaveLength(3);
  });

  it("animates it in otherwise", async () => {
    stubReducedMotion(false);
    const user = userEvent.setup();
    const { trigger } = renderSelect();

    await user.click(trigger);

    expect(screen.getByRole("listbox").parentElement?.dataset.motion).toBe(
      "in",
    );
  });
});

describe("pickOption", () => {
  it("is the one way a suite drives this control, and it fails loudly on a label that is not offered", async () => {
    const user = userEvent.setup();
    const { trigger, changes } = renderSelect();

    await expect(pickOption(user, trigger, "Renewal")).rejects.toThrow();
    expect(changes).toEqual([]);
  });
});
