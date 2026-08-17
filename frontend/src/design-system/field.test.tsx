/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { Field, TextInput } from "./atoms";
import { usePasswordReveal } from "./passwordreveal";

// Field grew three slots so the sign-in screens could stop forking it: a
// leading glyph and a trailing control that sit INSIDE one outline, and a
// refusal that is its own thing rather than a message pushed through `hint`.
// What is tested here is the wiring those slots owe a reader — the id, the
// description, the invalid state — because that is the half a screenshot
// cannot show and the half the fork got wrong.

afterEach(cleanup);

describe("Field", () => {
  it("labels the control by clicking, not only by announcing", async () => {
    const user = userEvent.setup();
    render(
      <Field label="Work address">
        {(control) => <TextInput {...control} />}
      </Field>,
    );
    await user.click(screen.getByText("Work address"));
    expect(screen.getByLabelText("Work address")).toHaveFocus();
  });

  it("emits no shell when nothing has to sit inside the outline", () => {
    const { container } = render(
      <Field label="Deal name">
        {(control) => <TextInput {...control} />}
      </Field>,
    );
    expect(container.querySelector(".field-shell")).toBeNull();
  });

  it.each([
    ["an icon", { icon: <svg aria-hidden /> }],
    ["a trailing control", { trailing: <button type="button">Show</button> }],
  ])("wraps the control in a shell for %s", (_name, slot) => {
    const { container } = render(
      <Field label="Password" {...slot}>
        {(control) => <TextInput {...control} />}
      </Field>,
    );
    const shell = container.querySelector(".field-shell");
    expect(shell).toBeTruthy();
    expect(shell?.querySelector("input.input")).toBeTruthy();
  });

  describe("the refusal", () => {
    it("announces, marks the control invalid, and describes it", () => {
      render(
        <Field label="New password" error="Too short.">
          {(control) => <TextInput {...control} />}
        </Field>,
      );
      const input = screen.getByLabelText("New password");
      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent("Too short.");
      expect(alert).toHaveClass("field-error");
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(input.getAttribute("aria-describedby")).toBe(alert.id);
    });

    // A field that is refused AND still carrying its rule has two things to
    // say. Naming only one of them in `aria-describedby` decides which of them
    // a sighted reader gets and which a screen-reader user gets, which is not a
    // decision anybody made on purpose.
    it("describes the control by BOTH the refusal and the hint", () => {
      render(
        <Field
          label="New password"
          error="These two don't match."
          hint="Both fields have to say the same thing."
        >
          {(control) => <TextInput {...control} />}
        </Field>,
      );
      const described = (
        screen
          .getByLabelText("New password")
          .getAttribute("aria-describedby") ?? ""
      ).split(" ");
      expect(described).toHaveLength(2);
      const spoken = described.map(
        (id) => document.getElementById(id)?.textContent,
      );
      expect(spoken).toContain("These two don't match.");
      expect(spoken).toContain("Both fields have to say the same thing.");
    });

    it("leaves a clean field unmarked", () => {
      render(
        <Field label="New password" hint="At least 12 characters.">
          {(control) => <TextInput {...control} />}
        </Field>,
      );
      const input = screen.getByLabelText("New password");
      expect(input.hasAttribute("aria-invalid")).toBe(false);
      expect(screen.queryByRole("alert")).toBeNull();
    });
  });

  it("keeps labelEnd out of the control's accessible name", () => {
    render(
      <Field label="Password" labelEnd={<button type="button">Forgot?</button>}>
        {(control) => <TextInput {...control} />}
      </Field>,
    );
    // Exactly "Password" — a link that happens to sit on the label's line is
    // not part of what this field is called.
    expect(screen.getByLabelText("Password")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Forgot?" })).toBeTruthy();
  });
});

describe("usePasswordReveal", () => {
  function RevealField() {
    const reveal = usePasswordReveal({ show: "Show password", hide: "Hide" });
    return (
      <Field label="Password" trailing={reveal.trailing}>
        {(control) => <TextInput {...control} type={reveal.type} />}
      </Field>
    );
  }

  it("starts hidden and says which way the control will go", () => {
    render(<RevealField />);
    expect(screen.getByLabelText("Password")).toHaveAttribute(
      "type",
      "password",
    );
    const toggle = screen.getByRole("button", { name: "Show password" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
  });

  // The button's pressed state and the input's type are ONE fact. They are
  // returned together for that reason: a caller handed only the button would
  // hold the other half itself and could get the two out of step, which is a
  // control announcing the opposite of what it is doing.
  it("flips the type and the pressed state together", async () => {
    const user = userEvent.setup();
    render(<RevealField />);
    await user.click(screen.getByRole("button", { name: "Show password" }));
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "text");
    expect(screen.getByRole("button", { name: "Hide" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("sits inside the field's own outline, not beside it", () => {
    const { container } = render(<RevealField />);
    expect(
      container.querySelector(".field-shell > .field-reveal"),
    ).toBeTruthy();
  });
});
