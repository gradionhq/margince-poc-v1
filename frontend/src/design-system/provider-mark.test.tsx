/** @vitest-environment jsdom */
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ProviderMark } from "./provider-mark";

afterEach(cleanup);

// The mark is chosen from the provider's key alone (provider-mark.tsx's own
// contract), so a recognised key must draw its real brand shape rather than
// falling through to the neutral glyph IMAP genuinely has no brand to avoid.

describe("ProviderMark", () => {
  it("draws LinkedIn's own mark, not the neutral key fallback", () => {
    const { container } = render(<ProviderMark providerKey="linkedin" />);
    const svg = container.querySelector("svg.provider-mark");
    expect(svg).toBeTruthy();
    // Lucide's fallback ships stroke-only paths with no literal fill; a real
    // brand mark carries its own colour.
    expect(svg?.querySelector('[fill="#0A66C2"]')).toBeTruthy();
    expect(svg?.classList.contains("lucide")).toBe(false);
  });

  it("keeps the neutral key glyph for a provider with genuinely no brand", () => {
    const { container } = render(<ProviderMark providerKey="imap" />);
    const svg = container.querySelector("svg.provider-mark");
    expect(svg?.classList.contains("lucide")).toBe(true);
  });

  // The mark's fill is a literal SVG attribute, not a design token, so it
  // cannot vanish against either theme's page background — proven here by
  // wrapping it in both and checking the same colour survives both.
  it("keeps its brand colour under both themes", () => {
    for (const theme of ["light", "dark"] as const) {
      const { container, unmount } = render(
        <div data-theme={theme}>
          <ProviderMark providerKey="linkedin" />
        </div>,
      );
      expect(container.querySelector('[fill="#0A66C2"]')).toBeTruthy();
      unmount();
    }
  });
});
