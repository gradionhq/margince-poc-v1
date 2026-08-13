/** @vitest-environment jsdom */
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { InlineChoice, InlineText } from "./inlinechoice";

// The rules this control keeps are failure modes, not polish. Each one here
// is a way a reader gets told something untrue about their own edit.

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

  it("opens the list on the one click that started editing, not a second one", async () => {
    renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    // The click that turned the value into a control already meant "show me
    // the options" — a caller that has to click the combobox again before an
    // option appears is exactly the extra press the edit-in-place pattern
    // exists to remove.
    expect(screen.getByRole("option", { name: "Customer" })).toBeTruthy();
  });

  it("does not write when the reader picks the value already set", async () => {
    const { onSave } = renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
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

  // The counterpart of the Escape case below: leaving the editing view is what
  // returns the reader to the trigger, so the answer they left with does not
  // change where they land. Picking a value dropped them on the document body
  // while backing out did not, which is the same loss of place either way.
  it("lands focus back on the trigger after a value is picked", async () => {
    renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.click(screen.getByRole("option", { name: "Customer" }));

    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole("button", { name: "Change Account lifecycle" }),
      ),
    );
  });

  it("lets Escape back out of an open list without changing anything", async () => {
    const { onSave } = renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.keyboard("{Escape}");
    // Back to the resting trigger, not stranded on a closed combobox with
    // nowhere left to go — and focus lands ON it, not merely present in the
    // document: a keyboard user who backed out with Escape must not also
    // lose their place on the page.
    const trigger = screen.getByRole("button", {
      name: "Change Account lifecycle",
    });
    expect(trigger).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("does not pull focus back to the trigger when Tab moves it forward", async () => {
    const { onSave } = renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.tab();
    // Tab already moved the reader on — reclaiming focus here would fight
    // the very key that just moved it, the opposite of what Escape does.
    const trigger = screen.getByRole("button", {
      name: "Change Account lifecycle",
    });
    expect(document.activeElement).not.toBe(trigger);
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("lets Escape back out even once a save has already failed", async () => {
    const onSave = vi.fn(async () => {
      throw new Error("Somebody else changed this record.");
    });
    renderChoice({ onSave });
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.click(screen.getByRole("option", { name: "Customer" }));
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());

    // The popup itself is already closed at this point — Select's own Escape
    // handling only ever claims a key press while its list is open, so this
    // is the one exit a failed save would otherwise have none of.
    await userEvent.keyboard("{Escape}");
    expect(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("abandons an unpicked choice when the reader clicks away", async () => {
    const { onSave } = renderChoice();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    );
    await userEvent.click(document.body);
    expect(
      screen.getByRole("button", { name: "Change Account lifecycle" }),
    ).toBeTruthy();
    expect(onSave).not.toHaveBeenCalled();
  });
});

// InlineText's own commit moment: there is no Save button, so Enter and
// losing focus are the two ways a typed value ever reaches `onSave`.
function renderText(
  props: Partial<React.ComponentProps<typeof InlineText>> = {},
) {
  const onSave = props.onSave ?? vi.fn(async () => {});
  render(
    <LocaleProvider initial="en">
      <InlineText
        label="Industry"
        value="Automotive"
        placeholder="Add industry"
        canEdit
        onSave={onSave}
        {...props}
      />
    </LocaleProvider>,
  );
  return { onSave };
}

describe("editing free text where it is read", () => {
  it("opens with the caret already in the field", async () => {
    renderText();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    // Every rule below — Enter, Escape, the blur-commit — is a keyboard or
    // focus event ON this input, so an input the browser never focused obeys
    // none of them: the box would sit open behind a reader who had already
    // clicked into the next field.
    expect(document.activeElement).toBe(screen.getByLabelText("Industry"));
  });

  it("closes when focus moves away, leaving no editor open behind the reader", async () => {
    renderText();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    await userEvent.click(document.body);
    expect(screen.queryByLabelText("Industry")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Change Industry" }),
    ).toHaveTextContent("Automotive");
  });

  it("puts the reader back on the value after Escape", async () => {
    renderText();
    const trigger = screen.getByRole("button", { name: "Change Industry" });
    await userEvent.click(trigger);
    await userEvent.type(screen.getByLabelText("Industry"), "{Escape}");
    // Backing out of an edit must not drop a keyboard reader onto the body:
    // they land on the value they opened, the same as InlineChoice's own
    // revert does.
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole("button", { name: "Change Industry" }),
      ),
    );
  });

  it("commits on Enter, with no Save button anywhere", async () => {
    const { onSave } = renderText();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    const input = screen.getByLabelText("Industry");
    await userEvent.clear(input);
    await userEvent.type(input, "Aerospace{Enter}");
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("Aerospace"));
  });

  it("commits on losing focus, with no explicit commit press", async () => {
    const { onSave } = renderText();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    const input = screen.getByLabelText("Industry");
    await userEvent.clear(input);
    await userEvent.type(input, "Aerospace");
    await userEvent.click(document.body);
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("Aerospace"));
  });

  it("does not write on a blur that changed nothing, however often it fires", async () => {
    const { onSave } = renderText();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    const input = screen.getByLabelText("Industry");
    await userEvent.click(input);
    await userEvent.click(document.body);
    // A blur with the value untouched is not an edit — sending it would write
    // an audit row for a change that did not happen, and blur now fires on
    // every exit rather than only on an explicit Save press.
    expect(onSave).not.toHaveBeenCalled();
  });

  it("reverts the draft on Escape without committing it", async () => {
    const { onSave } = renderText();
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    const input = screen.getByLabelText("Industry");
    await userEvent.clear(input);
    await userEvent.type(input, "Aerospace{Escape}");
    expect(
      screen.getByRole("button", { name: "Change Industry" }),
    ).toHaveTextContent("Automotive");
    expect(onSave).not.toHaveBeenCalled();
  });

  it("keeps the input mounted with the typed text on a refused save, rather than pulling focus back", async () => {
    const onSave = vi.fn(async () => {
      throw new Error("Somebody else changed this record.");
    });
    renderText({ onSave });
    await userEvent.click(
      screen.getByRole("button", { name: "Change Industry" }),
    );
    const input = screen.getByLabelText("Industry");
    await userEvent.clear(input);
    await userEvent.type(input, "Aerospace{Enter}");
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "Somebody else changed this record.",
      ),
    );
    // Still the editor, still the reader's own typed text — a failed save
    // must not also lose what they wrote or drop them out of the field.
    expect(screen.getByLabelText("Industry")).toHaveValue("Aerospace");
  });
});
