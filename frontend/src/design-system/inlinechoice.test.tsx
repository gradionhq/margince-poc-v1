/** @vitest-environment jsdom */
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { InlineChoice } from "./inlinechoice";

// The four rules this control keeps are failure modes, not polish. Each one
// here is a way a reader gets told something untrue about their own edit.

afterEach(cleanup);

const OPTIONS = [
  { value: "prospect", label: "Prospect" },
  { value: "customer", label: "Customer" },
];

function renderChoice(
  props: Partial<React.ComponentProps<typeof InlineChoice>> = {},
) {
  const onSave = props.onSave ?? vi.fn(async () => {});
  render(
    <LocaleProvider initial="en">
      <InlineChoice
        label="Account lifecycle"
        value="prospect"
        options={OPTIONS}
        canEdit
        render={(v) => <>{v}</>}
        onSave={onSave}
        {...props}
      />
    </LocaleProvider>,
  );
  return { onSave };
}

describe("editing a value where it is read", () => {
  it("shows a reader who may not edit the VALUE, not a disabled control", () => {
    renderChoice({
      canEdit: false,
      readOnlyReason: "This company is archived.",
    });
    // A disabled control says "you could do this" and then refuses. Plain text
    // says what is true.
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("prospect")).toBeTruthy();
  });

  it("names the field on the trigger rather than reading out the value alone", async () => {
    renderChoice();
    // Without an aria-label a screen reader announces "prospect, button" — the
    // state, with no hint that pressing it changes anything.
    expect(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    ).toBeTruthy();
  });

  it("does not write when the reader picks the value already set", async () => {
    const { onSave } = renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.click(screen.getByRole("combobox"));
    await userEvent.click(screen.getByRole("option", { name: "Prospect" }));
    // Choosing what is already set is not an edit; sending it would write an
    // audit row for a change that did not happen.
    expect(onSave).not.toHaveBeenCalled();
  });

  it("keeps the reader's choice on screen when the save is refused", async () => {
    const onSave = vi.fn(async () => {
      throw new Error("Somebody else changed this record.");
    });
    renderChoice({ onSave });
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.click(screen.getByRole("combobox"));
    await userEvent.click(screen.getByRole("option", { name: "Customer" }));

    // The refusal renders beside the control that caused it, and the picker
    // stays open on what they chose. Snapping back would discard their answer
    // and tell them nothing.
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "Somebody else changed this record.",
      ),
    );
    expect(screen.getByRole("combobox")).toBeTruthy();
  });
});
