/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import {
  Checkbox,
  DataTable,
  Field,
  OverflowMenu,
  Radio,
  Textarea,
  TextInput,
  verticalPlacement,
} from "./atoms";
import { Select } from "./select";

// The dropdown these cases pair with a Field is the Select from select.tsx — a
// button and a portalled listbox, not a native `<select>`. Its own behaviour is
// specified in select.test.tsx; here it stands in for "a control a Field wires
// up", which is the only thing these cases are about.
const STAGES = [
  { value: "won", label: "Won" },
  { value: "lost", label: "Lost" },
];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

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

// A card that hides its own overflow — every Panel does, so full-bleed rows
// respect its radius — used to clip this panel to the card's edge, and a menu
// on the last row lost the actions it exists to offer. The panel is drawn at
// the body and positioned against the trigger, so no ancestor can crop it.
it("draws its panel outside the container that would clip it", async () => {
  const user = userEvent.setup();
  const { container } = render(
    <OverflowMenu label="More actions">
      <button type="button">Archive</button>
    </OverflowMenu>,
  );

  await user.click(screen.getByRole("button", { name: "More actions" }));

  const items = screen.getByRole("button", { name: "Archive" }).parentElement;
  expect(items?.className).toContain("overflow-menu-items");
  expect(container.contains(items)).toBe(false);
  expect(document.body.contains(items)).toBe(true);
  // Positioned, not statically placed: a panel at the document's top-left is a
  // panel that has lost its trigger.
  expect(items?.getAttribute("style")).toContain("top:");
});

// The panel is a sibling of the trigger in the DOM now, so "click outside" has
// to mean outside BOTH — otherwise pressing an item reads as clicking away and
// closes the menu under the reader's finger.
it("stays open when an item inside the portalled panel is clicked", async () => {
  const user = userEvent.setup();
  render(
    <OverflowMenu label="More actions">
      <button type="button">Archive</button>
    </OverflowMenu>,
  );
  const trigger = screen.getByRole("button", { name: "More actions" });
  await user.click(trigger);

  await user.click(screen.getByRole("button", { name: "Archive" }));

  expect(trigger.getAttribute("aria-expanded")).toBe("true");
});

// The panel is FIXED, so the viewport is all the room there is: a menu placed
// below a trigger near the bottom edge puts its actions where no page scrolling
// reaches them. Stated over the measurements themselves, because jsdom gives
// every element a zero-sized rectangle and the rule is arithmetic.
it("opens the panel toward whichever side of the trigger has room", () => {
  const viewport = 800;
  vi.stubGlobal("innerHeight", viewport);
  const near = (top: number) =>
    ({ top, bottom: top + 30 }) as unknown as DOMRect;

  // Room below: the panel hangs from the trigger.
  const down = verticalPlacement(near(100), 200);
  expect(down.top).toBeGreaterThan(130);
  expect(down.maxHeight).toBeGreaterThan(200);

  // A trigger on the last row of a long page: below is 30px, so the panel
  // opens UPWARD and ends above the trigger rather than off the bottom edge.
  const up = verticalPlacement(near(viewport - 40), 200);
  expect(up.top + 200).toBeLessThanOrEqual(viewport - 40);

  // Taller than either side: it takes the roomier one and is capped to it, so
  // it scrolls inside itself instead of running past an edge.
  const squeezed = verticalPlacement(near(500), 2000);
  expect(squeezed.maxHeight).toBeLessThan(viewport);
  expect(squeezed.maxHeight).toBeGreaterThan(0);
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
  render(<Textarea aria-label="Body" className="compose-body" />);

  expect([...screen.getByLabelText("Body").classList].sort()).toEqual([
    "compose-body",
    "textarea",
  ]);
});

// The label/control pairing is the whole reason Field exists, and the failure
// it prevents is silent: a mistyped id in either half leaves a control that
// looks labelled and is not. Asserting through getByLabelText proves the
// association rather than the markup.
it("pairs a Field's label with its control, and two Fields never collide", () => {
  render(
    <>
      <Field label="Deal name">
        {(control) => <TextInput {...control} defaultValue="Globex" />}
      </Field>
      <Field label="Stage">
        {(control) => (
          <Select
            {...control}
            options={STAGES}
            value="won"
            onChange={() => {}}
          />
        )}
      </Field>
    </>,
  );

  expect((screen.getByLabelText("Deal name") as HTMLInputElement).value).toBe(
    "Globex",
  );
  // Two instances of the same component must not share an id — the second
  // label would then point at the first control, and typing into one would
  // read as the other.
  expect(screen.getByLabelText("Deal name").id).not.toBe(
    screen.getByLabelText("Stage").id,
  );
});

// A hint has to describe the control without becoming part of its name: read
// as a name, the whole help text is announced on every focus.
it("describes a Field's control by its hint without naming it that", () => {
  render(
    <Field label="Reason" hint="Shown to the person you are sharing with">
      {(control) => <Textarea {...control} />}
    </Field>,
  );

  const control = screen.getByLabelText("Reason");
  const describedBy = control.getAttribute("aria-describedby");
  expect(describedBy).toBeTruthy();
  expect(document.getElementById(describedBy ?? "")?.textContent).toBe(
    "Shown to the person you are sharing with",
  );
});

// `required` is one prop, spent in two places — the visible marker and the
// control's own state. The marker is aria-hidden so the requirement is
// announced once, by the control, not twice.
it("marks a required Field once for the eye and once for the control", () => {
  render(
    <Field label="Role" required>
      {(control) => (
        <Select
          {...control}
          options={[{ value: "admin", label: "Admin" }]}
          value="admin"
          onChange={() => {}}
        />
      )}
    </Field>,
  );

  // getByRole resolves the accessible name the way an assistive technology
  // does, so the aria-hidden asterisk is excluded and the name is still "Role".
  // Queried by label TEXT it would read "Role *" — which is the visible string,
  // not the announced one.
  const control = screen.getByRole("combobox", { name: "Role" });
  // aria-required, not `required`: the trigger is a button, and a button carries
  // no constraint validation for the attribute to drive.
  expect(control.getAttribute("aria-required")).toBe("true");
  expect(screen.getByText("*").getAttribute("aria-hidden")).toBe("true");
});

// A table that scrolls sideways holds columns a pointer can drag to and a
// keyboard cannot reach at all, so the box takes a tab stop and a name — and
// takes neither while it fits, because a tab stop in front of every table in
// the product is a cost every keyboard reader pays for the few that overflow.
// jsdom lays nothing out, so the two widths the decision reads are stubbed on
// the prototype: that is the whole input to it.
function stubBoxWidths(scrollWidth: number, clientWidth: number) {
  for (const [property, value] of [
    ["scrollWidth", scrollWidth],
    ["clientWidth", clientWidth],
  ] as const) {
    vi.spyOn(HTMLDivElement.prototype, property, "get").mockReturnValue(value);
  }
}

const PRODUCT_COLUMNS = [
  { key: "name", header: "Name", render: (row: { name: string }) => row.name },
];
const PRODUCT_ROWS = [{ name: "Consulting Day" }];

it("makes a table's scroll box reachable and named once it overflows", () => {
  stubBoxWidths(930, 654);
  render(
    <DataTable
      label="Products"
      columns={PRODUCT_COLUMNS}
      rows={PRODUCT_ROWS}
      rowKey={(row) => row.name}
    />,
  );
  const box = screen.getByRole("region", { name: "Products" });
  expect(box.className).toContain("table-scroll");
  expect(box.getAttribute("tabindex")).toBe("0");
});

it("leaves a table that fits its box out of the tab order", () => {
  stubBoxWidths(654, 654);
  const { container } = render(
    <DataTable
      label="Products"
      columns={PRODUCT_COLUMNS}
      rows={PRODUCT_ROWS}
      rowKey={(row) => row.name}
    />,
  );
  expect(screen.queryByRole("region")).toBeNull();
  const box = container.querySelector(".table-scroll");
  expect(box?.getAttribute("tabindex")).toBeNull();
});
