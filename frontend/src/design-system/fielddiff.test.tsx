/** @vitest-environment jsdom */
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { FieldDiff, PassportChip } from "./trust";

afterEach(cleanup);
const render = (ui: React.ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("FieldDiff", () => {
  it("shows old struck through and new highlighted", () => {
    render(
      <FieldDiff
        oldValue="Globex Renewal"
        newValue="Globex Renewal (updated)"
      />,
    );
    expect(screen.getByText("Globex Renewal")).toBeTruthy();
    expect(screen.getByText("Globex Renewal (updated)")).toBeTruthy();
  });
  it("renders an empty origin when there is no old value", () => {
    render(<FieldDiff oldValue={null} newValue="Carol Wagner" />);
    expect(screen.getByText("— created —")).toBeTruthy();
    expect(screen.getByText("Carol Wagner")).toBeTruthy();
  });
  it("renders a cleared marker when the new value is null", () => {
    render(<FieldDiff oldValue="x" newValue={null} />);
    expect(screen.getByText("— cleared —")).toBeTruthy();
  });
});

describe("PassportChip", () => {
  it("renders the passport id in mono", () => {
    render(<PassportChip id="psp_7Q3fa91" />);
    expect(screen.getByText(/psp_7Q3fa91/)).toBeTruthy();
  });
});
