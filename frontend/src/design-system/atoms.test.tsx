/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { Checkbox, OverflowMenu, Radio, Select, Textarea } from "./atoms";

afterEach(cleanup);

// The items are components with their own reads — the company's edit form
// fetches the user roster and the custom-field catalogue — so rendering them
// before the menu is ever opened made every reader of every record page pay
// for actions they did not take.
it("does not mount its items until the menu is first opened", async () => {
  let mounted = 0;
  function CostlyAction() {
    mounted += 1;
    return <button type="button">Merge</button>;
  }
  render(
    <OverflowMenu label="More actions">
      <CostlyAction />
    </OverflowMenu>,
  );
  expect(mounted).toBe(0);

  await userEvent.click(screen.getByRole("button", { name: "More actions" }));
  expect(mounted).toBeGreaterThan(0);

  // And they stay mounted once opened: an item's dialog restores focus to the
  // element that opened it, which must survive the panel being hidden again.
  await userEvent.click(screen.getByRole("button", { name: "More actions" }));
  // hidden: true — the closed panel is `hidden`, and the point is that the
  // item is still in the tree rather than visible.
  expect(
    screen.getByRole("button", { name: "Merge", hidden: true }),
  ).toBeTruthy();
});

// The label is half the control. Every hand-rolled site this atom replaces got
// its accessible name from a <label> wrapping the input, and a reader ticks the
// box by clicking the words — so both are asserted through the label text, not
// through the input node.
it("names a Checkbox by its label, and the label text toggles it", async () => {
  const seen: boolean[] = [];
  render(
    <Checkbox
      label="Include archived records"
      onChange={(event) => seen.push(event.target.checked)}
    />,
  );

  const box = screen.getByRole("checkbox", {
    name: "Include archived records",
  });
  await userEvent.click(screen.getByText("Include archived records"));

  expect(seen).toEqual([true]);
  expect((box as HTMLInputElement).checked).toBe(true);
});

// Radios group by `name`, which the atom must forward untouched — drop it and
// every option becomes independently selectable, which looks like working UI
// until two are on at once.
it("keeps Radios sharing a name mutually exclusive", async () => {
  render(
    <>
      <Radio name="side" label="Owner" defaultChecked />
      <Radio name="side" label="Team" />
    </>,
  );

  const owner = screen.getByRole("radio", {
    name: "Owner",
  }) as HTMLInputElement;
  const team = screen.getByRole("radio", { name: "Team" }) as HTMLInputElement;
  expect(owner.checked).toBe(true);

  await userEvent.click(screen.getByText("Team"));

  expect(team.checked).toBe(true);
  expect(owner.checked).toBe(false);
});

// Screens layer their own layout on top of the atom (`.compose-body`,
// `.share-reason`). Dropping either class silently unstyles the control, so the
// merge is asserted rather than assumed.
it("merges a caller's className with the atom's own", () => {
  render(
    <>
      <Textarea aria-label="Body" className="compose-body" />
      <Select aria-label="Stage" className="stage-picker">
        <option value="won">Won</option>
      </Select>
    </>,
  );

  expect([...screen.getByLabelText("Body").classList].sort()).toEqual([
    "compose-body",
    "textarea",
  ]);
  expect([...screen.getByLabelText("Stage").classList].sort()).toEqual([
    "input",
    "stage-picker",
  ]);
});
