/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { OverflowMenu } from "./atoms";

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
