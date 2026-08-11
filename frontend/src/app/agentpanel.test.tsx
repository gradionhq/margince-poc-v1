/** @vitest-environment jsdom */
import {
  cleanup,
  render as rtlRender,
  screen,
  within,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../i18n";
import { AgentStrip } from "./agentpanel";

afterEach(cleanup);

const render = (ui: ReactNode) =>
  rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);

describe("AgentStrip", () => {
  it("names its region so the head row's agent block is reachable as one landmark", () => {
    render(<AgentStrip />);
    const strip = screen.getByRole("region", { name: "Margince AI status" });
    expect(within(strip).getByText("Margince AI")).toBeTruthy();
  });

  // The runtime knows routing is CONFIGURED; it does not continuously prove a
  // provider is reachable. Three things carry that limit and all three are
  // asserted here — the sentence, the Core's beat, and its feed — because a
  // surface that quietly starts looking busy claims liveness just as loudly as
  // one that says "Connected".
  it("states configuration and shows no sign of work in flight", () => {
    const { container } = render(<AgentStrip />);
    const strip = screen.getByRole("region", { name: "Margince AI status" });
    expect(within(strip).getByText("Configured")).toBeTruthy();
    expect(strip.textContent).not.toMatch(/connected|online|live|running/i);

    expect(
      container.querySelector(".agentorb")?.getAttribute("data-core-state"),
    ).toBe("quiet");
    // Motes arriving at the Core would say context is landing right now.
    expect(container.querySelector(".core-feed")).toBeNull();
  });

  // Activity, spend and routing have no handler behind them. The marker is what
  // keeps them from passing as real, so it has to be readable rather than
  // announced-only — a marker clipped to `.sr-only` labels the block for a
  // screen reader and leaves everyone else looking at invented numbers.
  it("shows the example-data marker with the example values it covers", () => {
    render(<AgentStrip />);
    const strip = screen.getByRole("region", { name: "Margince AI status" });
    for (const example of [
      "Enriching 4 new contacts",
      "€2.41 today",
      "Local + cloud",
    ]) {
      expect(within(strip).getByText(example)).toBeTruthy();
    }
    const marker = within(strip).getByText("Example data");
    expect(marker.className).not.toContain("sr-only");
    expect(marker.closest("[aria-hidden]")).toBeNull();
  });
});
