/** @vitest-environment jsdom */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FileChip } from "./filechip";

// The card IS the download, and its name is the filename — the name the saved
// copy carries, and the only thing that tells two files on one row apart.

describe("FileChip", () => {
  it("downloads under the filename it is named by", () => {
    render(<FileChip href="/v1/attachments/a-1" filename="GR-2026-0092.pdf" />);
    const link = screen.getByRole("link", { name: "GR-2026-0092.pdf" });
    expect(link.getAttribute("href")).toBe("/v1/attachments/a-1");
    expect(link.getAttribute("download")).toBe("GR-2026-0092.pdf");
  });

  it("gives a file that is not a PDF the neutral mark rather than a guessed one", () => {
    const { container } = render(
      <FileChip href="/v1/attachments/a-2" filename="terms-redline.docx" />,
    );
    const pdf = render(
      <FileChip href="/v1/attachments/a-1" filename="signed.PDF" />,
    );

    // The glyph is decorative, so it is compared as markup rather than looked
    // up by name: what matters is that the two kinds do not draw the same one,
    // and that the extension is read case-insensitively — a scanner writes
    // .PDF as readily as .pdf.
    const other = container.querySelector("svg")?.innerHTML;
    const asPdf = pdf.container.querySelector("svg")?.innerHTML;
    expect(other).toBeTruthy();
    expect(asPdf).toBeTruthy();
    expect(other).not.toBe(asPdf);
  });
});
