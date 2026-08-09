/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { Globe, MapPin } from "lucide-react";
import { afterEach, describe, expect, it } from "vitest";
import { Chip, Meter, Sparkline } from "./readings";

afterEach(cleanup);

function fillWidth(container: HTMLElement) {
  return container.querySelector<HTMLElement>(".meterbar span")?.style.width;
}

describe("Meter draws a proportion a reader can also hear", () => {
  it("carries the raw pair, not a percentage, on the ARIA node", () => {
    render(<Meter value={7} max={9} label="Dossier completeness" />);
    const meter = screen.getByRole("meter", { name: "Dossier completeness" });
    expect(meter.getAttribute("aria-valuenow")).toBe("7");
    expect(meter.getAttribute("aria-valuemax")).toBe("9");
  });

  it("fills the bar to the value's share of the max", () => {
    const { container } = render(<Meter value={1} max={4} label="Coverage" />);
    expect(fillWidth(container)).toBe("25%");
  });

  // A zero max is "nothing has been measured", not a division. It must read
  // as an empty bar rather than a NaN width the browser silently drops.
  it("draws an empty bar when nothing has been measured", () => {
    const { container } = render(<Meter value={0} max={0} label="Coverage" />);
    expect(fillWidth(container)).toBe("0%");
  });

  // A value beyond its max is a data fault, not a reason to draw a bar wider
  // than its track.
  it("clamps a value that overruns its max", () => {
    const { container } = render(<Meter value={12} max={9} label="Facts" />);
    expect(fillWidth(container)).toBe("100%");
  });

  it("colours a low-is-bad reading with its own tone", () => {
    const { container } = render(
      <Meter value={2} max={10} label="Payment" tone="danger" />,
    );
    expect(
      container
        .querySelector(".meterbar")
        ?.classList.contains("meterbar-danger"),
    ).toBe(true);
  });
});

describe("Sparkline is a glyph, not a chart", () => {
  it("draws one point per reading, scaled into the box", () => {
    render(<Sparkline points={[0, 10]} label="Days paid after due" />);
    const line = screen
      .getByRole("img", { name: "Days paid after due" })
      .querySelector("polyline");
    // Low sits at the bottom inset, high at the top one; x spans the width.
    expect(line?.getAttribute("points")).toBe("0.0,29.0 120.0,3.0");
  });

  // A flat series has no range to scale into. It reads as unchanged rather
  // than dividing by zero.
  it("draws a flat series down the middle", () => {
    render(<Sparkline points={[8, 8, 8]} label="Unchanged" />);
    const line = screen
      .getByRole("img", { name: "Unchanged" })
      .querySelector("polyline");
    expect(line?.getAttribute("points")).toBe("0.0,29.0 60.0,29.0 120.0,29.0");
  });

  // One point is a dot, and a dot reads as a flat trend — a claim a single
  // reading does not support.
  it("draws nothing from fewer than two points", () => {
    const { container } = render(<Sparkline points={[4]} label="One" />);
    expect(container.querySelector("svg")).toBeNull();
  });
});

describe("Chip is a fact, and a link when the fact has somewhere to go", () => {
  it("renders a plain chip with no destination", () => {
    render(<Chip icon={MapPin}>London, UK</Chip>);
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("London, UK")).toBeTruthy();
  });

  it("opens an off-origin destination in a new tab without a referrer", () => {
    render(
      <Chip icon={Globe} href="https://glazedfrog.example">
        glazedfrog.example
      </Chip>,
    );
    const link = screen.getByRole("link", { name: "glazedfrog.example" });
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noreferrer");
  });
});
